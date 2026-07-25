# ai-adapter 重试与 Key 管理策略

> 更新日期：2026-07-26
> 参数明细见 [retry-config.md](retry-config.md)，修改前问题与实施边界见 [16-渠道内重试与流式转换处理专项分析.md](16-渠道内重试与流式转换处理专项分析.md)。

## 1. 分层模型

系统包含三层相互独立的容错状态：

| 层级 | 状态 | 生命周期 | 用途 |
|---|---|---|---|
| 请求内 Key 轮转 | `RetryState` | 单次 Channel dispatch | 完整轮次、429 cooldown、总超时、连续网络/5xx |
| Key 健康 | `KeyState` | 跨请求，可持久化 | 401 永久跳过、错误计数、暂停、平均延迟 |
| Channel 健康 | `ChannelHealth` | 跨请求 | 仅按连接错误和上游 5xx 暂停整个渠道 |

外层 `failoverLoop` 负责候选 Channel 切换，内层串行状态机负责同一 Channel 的 Key 轮转。Fanout 是独立的并发竞速模式，不执行串行轮次。

## 2. 完整 Key 轮次

每轮为当前全局可用的 Key 建立候选集合。选择策略先过滤：

- 当前轮已经尝试的 Key；
- 本请求中仍处于 429 cooldown 的 Key；
- 跨请求已永久跳过或仍在暂停期的 Key。

一轮内每个 Key 最多尝试一次。当前轮没有候选后，若尚未达到 `max_rotation_rounds`，清空“当前轮已尝试”集合并进入下一轮。5xx 和连接错误 Key 会重新候选；401 Key 不会；429 Key 只有在 cooldown 到期后才会候选。

所有选择策略均遵守同一排除集合：

| `key_strategy` | 选择规则 |
|---|---|
| `round-robin` | 轮询 |
| `random` | 随机 |
| `least-errors` | 历史错误最少 |
| `least-latency` | 单 Key attempt 平均延迟最低 |
| `least-rate-limited` | 最近限流评分最低 |

## 3. 错误决策表

| 上游结果 | 是否换 Key | 是否可进入下一轮 | 是否跨 Channel | ChannelHealth |
|---|---|---|---|---|
| 401 | 是 | 否，永久跳过 | Key 耗尽后可 | 不影响 |
| 429 | 是 | cooldown 到期后可 | 轮次/超时耗尽后可 | 不影响 |
| 400 | 否 | 否 | 否，直接返回客户端 429 | 不影响 |
| 403/404/其他 4xx | 否 | 否 | 返回 `FailoverError` 后可 | 不影响 |
| 5xx | 是 | 是 | 连续失败阈值或轮次耗尽后可 | 计失败 |
| 连接错误 | 是 | 是 | 与 5xx 相同 | 计失败 |
| 流解析/写入错误 | 提交 200 前视路径而定；提交后不能 | 否 | 提交 200 后不能 | 不计渠道失败 |

### 3.1 401

401 调用 `On401`：增加 `RequestCount`、401 和总错误计数，并设置 `PermanentlySkipped=true`。它同时把本请求的连续网络/5xx 计数清零，随后选择其他 Key。

恢复永久跳过 Key 需要显式 Resume/状态重置；普通 cooldown 到期不会恢复它。

### 3.2 429

429 不再阻塞整个循环固定 Sleep。当前 Key 加入请求级 cooldown 后，状态机先尝试其他 Key；没有候选时才使用 Context 感知的 timer 等待最早 cooldown 到期。

429 还会累加 Key 的 `ConsecErrors` 和五分钟限流窗口。达到 `consec_error_threshold` 后触发跨请求暂停，暂停时长按线性公式增长。

### 3.3 400

上游 400 保持项目既有业务解释：它被视为上游侧的限流/兼容性错误。客户端收到 HTTP 429，错误代码仍为 `upstream_bad_request`，错误体写入深度日志；Key 侧也按 429 统计。该路径直接结束，不切换 Channel。

### 3.4 其他 4xx

其他 4xx 被视为不能通过同渠道换 Key 解决的错误。记录当前 Key 后立即返回 `FailoverError`。启用多 Channel Failover 时外层可尝试下一渠道，但这些错误不会累计 ChannelHealth。

### 3.5 5xx 与连接错误

当前 Key 只在本轮标记为已尝试，状态机立即选择下一 Key；下一轮可以再次使用。两者增加请求级 `consecFails`，达到 `failover.consecutive_fail_threshold` 时可在完整轮次结束前提前切换 Channel。

只有这两类明确设置 `FailoverError.AffectsChannelHealth=true`。

## 4. Key 暂停与恢复

除 401 外的错误通过统一计数逻辑增加 `ConsecErrors`：

```text
pause_seconds = (ConsecErrors - threshold + 1) * pause_multiplier_sec
pause_seconds = min(pause_seconds, pause_max_sec)
```

达到阈值的当次错误立即触发暂停。成功请求会：

- 增加 `RequestCount`；
- 清零 `ConsecErrors`；
- 清除 `Paused` 和 `PauseUntil`；
- 更新 `LastSuccessTime`。

## 5. 超时

渠道内状态机使用 `max_total_wait_ms` 创建 Context deadline，可中断 HTTP 请求、响应读取和 429 等待。外层还有 `failover.total_timeout_ms`，Channel HTTP client 还有 `request_timeout_ms`，三者以最先到期者为准。

实时流已经向客户端提交 HTTP 200 后，任何解析、网络或写入错误都只能终止当前流，不能切换 Key 后重新发送，否则会在同一响应中混入两次生成结果。

## 6. 延迟和请求计数

系统端到端延迟从 Handler 接受请求开始，到响应处理完成结束，用于请求日志和汇总指标。

Key attempt 延迟只覆盖某个 Key 的单次 HTTP 调用及该次响应处理，用于 Key 平均延迟和 `least-latency`。每个成功或失败 attempt 都先记录延迟，再由 `ReportSuccess` / `ReportError` 增加一次 `RequestCount`，使平均值分子和分母一致。

Fanout 每个并发 attempt 单独统计；赢家取消导致的落选请求不记错误。

## 7. 流式处理

跨协议流使用实时增量转换，不再完整缓存后伪造流：

```text
source SSE -> Chat SSE -> target SSE
```

中间通过 `io.Pipe` 提供背压。解析器检查非法 JSON、Scanner 错误、Context、终止事件和下游写入错误；失败调用 `ReportStreamError`，不会调用 `ReportSuccess`。

原生同协议流是透明复制，只校验 `io.Copy` 的传输结果，不解析协议终止事件。

## 8. Fanout

Fanout 在同一时间向多个 Key 发请求：

- `wait_all=false` 返回第一个完整非流式 2xx，或第一个流式 2xx 响应头。
- `wait_all=true` 仅用于非流式，等待全部完成后选择最快 2xx。
- 流式赢家直到 Body 完整复制/转换后才记成功。
- 每个流请求有独立 Context，取消落选请求不会取消赢家 Body。
- 400 同样对客户端和 Key 映射为 429。

Fanout 不使用 `max_rotation_rounds` 和请求级 429 cooldown。需要严格串行轮转语义时应关闭 Fanout。

## 9. 配置示例

```yaml
channels:
  - id: mimo
    key_strategy: round-robin
    request_timeout_ms: 60000
    retry:
      retry_delay_429_ms: 1000
      max_rotation_rounds: 3
      max_total_wait_ms: 30000
      consec_error_threshold: 3
      pause_multiplier_sec: 30
      pause_max_sec: 600

failover:
  enabled: true
  max_channel_attempts: 3
  total_timeout_ms: 120000
  consecutive_fail_threshold: 2
```

`consecutive_fail_threshold=2` 表示连续两次网络/5xx 即可提前切换 Channel。如果目标是至少尝试完一轮全部 Key，应把它调到不小于渠道可用 Key 数量。

## 10. 兼容说明

旧字段 `max_retries` 和 `retry_delay_ms` 不参与当前渠道内轮转状态机，仍保留仅用于配置兼容。429 当前不解析 `Retry-After`，使用固定的 `retry_delay_429_ms`。
