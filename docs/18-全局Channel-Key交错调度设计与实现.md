# 全局 Channel/Key 交错调度设计与实现

> 实施日期：2026-07-26  
> 状态：已实现并补充契约测试  
> 关联问题：R-08、R-09，以及“单个 Channel 内连续请求多个 Key 后才切换 Channel”的调度偏斜

## 1. 目标

本次修改把请求重试从“先耗尽一个 Channel 的 Key，再切换 Channel”改为全局交错遍历：

```text
A/key1 -> B/key1 -> C/key1 -> A/key2 -> B/key2 -> C/key2
```

同时满足以下契约：

- 尽可能覆盖所有当前可用的 Channel/Key 组合。
- `retry.max_rotation_rounds` 表示完整 Key 轮次，而不是循环次数。
- 同一客户端模型优先尝试上一次成功的 Channel/Key。
- 首选失败后回到完整交错遍历，不让某个 Channel 独占请求时间。
- 401 永久跳过 Key；400/429 使用请求级 cooldown；5xx/网络错误切换到下一个 Channel。
- 只有 5xx 和连接错误影响 `ChannelHealth`。
- 流已经向客户端提交 200 后发生解析或写入错误时立即结束，不能切换 Channel 重放。

## 2. 修改前的根因

旧调用链有两层独立循环：

```text
failoverLoop
  -> 选择 Channel
  -> dispatch
    -> Forward 内部 RetryState
      -> 循环当前 Channel 的全部 Key 和全部轮次
  -> 当前 Channel 完全失败后才返回 failoverLoop
```

因此，即使有多个 Channel，请求仍会长时间停留在第一个 Channel。外层无法观察“单个 Key 尝试结束”，只能观察“整个 Channel 的重试周期结束”。

这还造成三个附带问题：

1. `max_channel_attempts` 只限制 Channel 数量，无法表达 Channel/Key 对的完整覆盖。
2. 四套原生/转换、流式/非流式循环各自判断状态码，容易漂移。
3. `consecutive_fail_threshold` 会在完整轮次前截断遍历，与“尽可能遍历所有 Key”冲突。

## 3. 当前执行结构

当前由 `failoverLoop` 统一负责请求级调度：

```text
请求
  -> 获取同模型上一次成功路由（可选）
  -> 确定候选 Channel
  -> 首选 Channel/Key 单次尝试（可选）
  -> global round 1
       pass 1: A/key1 -> B/key1 -> C/key1
       pass 2: A/key2 -> B/key2 -> C/key2
       ...
  -> global round 2
       清空每个 Channel 的本轮 attempted
       重新交错遍历
  -> 成功、超时、流已提交错误，或轮次耗尽
```

协议转发函数仍负责构造请求、发送 HTTP、转换响应和记录 Key 统计，但在全局调度模式下会收到一个 `singleAttempt` 的 `RetryState`。该状态只允许使用调度器明确指定的 Key；本次尝试结束后返回 `RetryNext`，不再在函数内部继续消费同一 Channel 的其他 Key。

## 4. 遍历顺序

### 4.1 无历史成功路由

两个 Channel、每个两个 Key、单轮配置：

```text
A/key1
B/key1
A/key2
B/key2
```

每个 pass 对每个 Channel 最多选择一个 Key。只有完成一遍 Channel 列表后，才开始每个 Channel 的下一个 Key。

### 4.2 有历史成功路由

假设同模型上次成功的是 `B/key2`：

```text
B/key2              # 独立首选尝试
A/key1
B/key1
A/key2
```

`B/key2` 会被标记为第一轮已尝试，因此首选失败后不会在第一轮立即重复。第二轮开始后，它可以按照普通规则重新候选。

### 4.3 首选失效

以下情况会清除缓存并进入普通遍历：

- Channel 已不在当前模型候选中。
- Channel 在请求开始时不健康。
- Key 已被 401 永久跳过或被管理员暂停。
- 首选尝试返回任意失败。

缓存使用进程内 `sync.Map`，进程重启后清空。只缓存配置中真实存在的模型或 alias；未知模型 fallback 不进入缓存，避免任意模型名造成无界增长。

## 5. 轮次契约

每个 Channel 使用自己的 `retry.max_rotation_rounds`：

- 值为 1：每个可用 Key 在本请求中最多尝试一次。
- 值为 2：第一轮完成后，每个仍可用的 Key 最多再尝试一次。
- 多 Channel 配置不同轮数时，全局循环运行到最大轮数；轮数较小的 Channel 在后续轮次跳过。

轮次边界只清空请求级 `attempted`，不会清除：

- 401 的永久跳过状态。
- 管理员暂停或 KeyState 跨请求暂停。
- 尚未到期的 400/429 请求级 cooldown。

配置值在正常 YAML 加载时默认是 3。代码对程序化构造的非正数值也防御性地按 1 轮处理。

## 6. 状态码和错误分类契约

所有四套转发路径共享 `classifyAttempt`：

| 分类 | 上游结果 | Key 处理 | 调度处理 | 最终全部耗尽 |
|---|---|---|---|---|
| Success | 2xx/3xx | 成功、清零连续错误 | 结束并记录成功路由 | 不适用 |
| Unauthorized | 401 | `On401`，永久跳过，计入 RequestCount | 下一个 Channel/Key | 503 |
| RateLimited | 429 | 按 429 统计 | cooldown 后可在后续轮次重入 | 503 |
| MappedBadRequest | 400 | Key 统计按 429 处理 | 与 429 相同，不写中间 429 | 503 |
| ClientError | 其他 4xx | 按原状态统计 | 返回 `FailoverError`，继续其他 Channel | 最后一个实际 4xx |
| ServerError | 5xx | 统计但不跨请求暂停 Key | 下一个 Channel；下一轮可重试该 Key | 503 |
| NetworkError | 连接错误/状态 0 | 记录网络错误 | 下一个 Channel | 502 |

超时统一返回 504。

400 的“映射为 429”只作用于 Key 统计和请求级 cooldown，不把中间 429 写给客户端。Fanout 是原子模式；若所有并发 Key 都失败且保留的代表状态是 400，最终客户端响应为 400，Key 统计仍按 429 处理。

## 7. cooldown

`RetryState.coolDown` 计算：

```text
cooldownUntil = now + retry.retry_delay_429_ms
```

单次尝试通过 `FailoverError.RetryCooldownUntil` 把时间传回全局调度器。调度器把 cooldown 保存在“本请求、当前 Channel”的状态中。

如果下一轮开始时所有未尝试 Key 都仍在 cooldown：

- 使用 Context 感知的 timer 等待最早到期时间。
- 客户端取消立即退出。
- `failover.total_timeout_ms` 或单 Channel 的 `retry.max_total_wait_ms` 到期时返回 504。

如果配置只有一轮，已尝试的 400/429 Key 不会仅因 cooldown 到期就在同一轮重复。

## 8. failover.enabled 与 Fanout

### 8.1 `failover.enabled: true`

- 遍历所有健康候选 Channel。
- 严格使用单 Key 交错调度。
- 即使 Channel 配置了 Fanout，也在本模式下旁路 Fanout，保证顺序可预测。

### 8.2 `failover.enabled: false`

- 只选择一个 Channel，不切换到其他 Channel。
- 非 Fanout 请求仍使用统一调度器遍历该 Channel 的所有 Key 和轮次。
- 非 Fanout 请求中，同模型成功路由可影响单 Channel 的优先选择及首选 Key。
- 如果选中的 Channel 开启 Fanout，则保留 Fanout 原子并发行为。

Fanout 的 `count`、`wait_all` 与串行轮次本质冲突，因此它是明确的执行模式例外，而不是交错遍历的一部分。

## 9. ChannelHealth

请求开始时只把当前健康的 Channel 放入遍历快照。

- 5xx、网络错误：`AffectsChannelHealth=true`，调用 `ReportChannelFailure`。
- 401、400、429、其他 4xx：不影响 ChannelHealth。
- 成功：调用 `ReportChannelSuccess`。
- 流解析/客户端写入失败：不伪装为成功，也不按上游 5xx/连接错误累计。

为了“尽可能遍历所有 Key”，某个 Channel 在本请求中因连续 5xx 达到不健康阈值后，当前请求仍会完成已建立的遍历快照；后续新请求会跳过该 Channel，直到恢复窗口允许探测。

## 10. 流式边界

在收到上游非 2xx Header 时，尚未向客户端提交成功响应，可以继续调度。

一旦写出客户端 HTTP 200：

- 流解析失败；
- 上游流中断；
- 下游写入失败；
- 跨协议转换失败；

都会返回 `Handled=true` 并结束请求。此时切换 Key 或 Channel 会把第二份响应拼到已经提交的流中，因此禁止重试。

## 11. 模型 fallback

系统继续保留未知模型回退默认模型的产品行为，但不再静默。日志包含：

```text
model_route_fallback=true
requested_model=<客户端模型>
resolved_model=<默认上游模型>
fallback_channel=<渠道 ID>
fallback_reason=unknown_model
```

fallback 请求不会写入成功路由缓存。

## 12. 配置迁移

以下无效或冲突字段已经删除，并在 YAML 加载时明确报错：

| 已删除字段 | 原问题 | 替代配置 |
|---|---|---|
| `channels[].max_retries` | 转发代码不读取 | `retry.max_rotation_rounds` |
| `channels[].retry_delay_ms` | 转发代码不读取 | `retry.retry_delay_429_ms` |
| `failover.max_channel_attempts` | 会截断全部 Channel/Key 覆盖 | `retry.max_rotation_rounds` + `failover.total_timeout_ms` |
| `failover.consecutive_fail_threshold` | 会提前截断完整轮次 | `retry.max_rotation_rounds` + ChannelHealth |

示例：

```yaml
failover:
  enabled: true
  total_timeout_ms: 120000
  load_balance: priority

channels:
  - id: primary
    retry:
      retry_delay_429_ms: 1000
      max_rotation_rounds: 3
      max_total_wait_ms: 30000
```

## 13. 延迟和日志

- 请求总延迟：从 Handler 接收请求到最终响应结束，写入 DB、Prometheus 和内存 Stats。
- 单 Key 尝试延迟：每次 HTTP 尝试独立记录到 KeyState，并写入“得到子渠道应答”日志。
- 调度日志增加 `channel_id`、掩码后的 `channel_key`、`round` 和全局 `attempt`。
- 最终失败信息包含实际尝试的 Channel/Key 对数量。

## 14. 测试覆盖

新增契约测试覆盖：

1. 两 Channel × 两 Key 的交错顺序。
2. 同模型上次成功 Channel/Key 优先。
3. 首选失败后继续标准完整遍历。
4. 多轮完整重复。
5. 400/429 cooldown 到期后在下一轮重入。
6. 401 永久跳过、RequestCount 和后续轮次不再选择。
7. 其他 4xx 切换下一个 Channel。
8. 5xx 切换 Channel 且轮次顺序稳定。
9. 状态分类器全部类别。
10. 未知模型 fallback 的结构化日志字段。
11. Fanout 400 不直接向客户端写 429。
12. 已提交流解析失败不调用后备 Channel。

## 15. 已知边界

- cooldown 尚未解析上游 `Retry-After`，也没有指数退避和抖动。
- 成功路由缓存只在当前进程有效，没有跨实例共享。
- 成功路由粘性会降低负载均衡的均匀度，这是“优先复用已验证路由”的明确取舍。
- Fanout 仍有独立的 Header 传递和并发统计边界，详见综合 Review 的 R-10。
- ChannelHealth 的阈值和恢复窗口仍是固定实现，尚未配置化。
