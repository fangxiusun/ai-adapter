# RetryConfig 参数说明

> 定义位置：`internal/config/config.go`
> 执行状态机：`internal/proxy/retry.go`
> 更新日期：2026-07-26

`channels[].retry` 控制 Key 健康与请求级完整轮次。全局执行器按 Channel 交错选择 Key。Fanout 是并发竞速模式，不执行串行轮次。

## 参数总览

| YAML 配置 | 默认值 | 作用 |
|---|---:|---|
| `retry_delay_429_ms` | 500 | 429 Key 在当前请求中的冷却时间 |
| `max_rotation_rounds` | 3 | 当前 Channel 最多参加的完整 Key 轮次 |
| `max_total_wait_ms` | 30000 | 单 Channel 模式的 Context 硬截止时间 |
| `consec_error_threshold` | 3 | 单 Key 跨请求连续错误暂停阈值 |
| `pause_multiplier_sec` | 30 | Key 暂停时长的线性递增步长（秒） |
| `pause_max_sec` | 600 | 单次 Key 暂停时长上限（秒） |

## 请求级轮转状态

每个请求为每个候选 Channel 创建独立遍历状态：

- `attempted` 保存当前轮已尝试的 Key，每轮每个候选 Key 最多调用一次。
- `cooldownUntil` 保存本请求中 429 Key 的冷却截止时间。
- 完成所有 Channel 的当前轮后清空 `attempted` 并进入下一轮。
- Key 选择通过 `KeyPool.NextExcluding` 完成，所有选择策略都先过滤排除集合。

`max_rotation_rounds=3` 表示最多执行三轮完整 Key 选择，不再表示三次 HTTP 尝试。实际可能提前结束，原因包括成功、无可用 Key、客户端取消、总超时或已提交的流错误。

## 429 冷却

收到 429 后：

1. Key 记录一次 429 和请求耗时。
2. Key 在本请求中冷却 `retry_delay_429_ms`。
3. 状态机立即选择其他候选 Key。
4. 没有其他候选时，使用 Context 感知的 timer 等待最早冷却到期。
5. 冷却到期后，Key 可在后续轮次重新进入候选。

这里没有阻塞式 `time.Sleep`。客户端取消、`max_total_wait_ms` 或外层 Failover deadline 都能中断等待。当前仍使用固定延迟，不解析 `Retry-After`。

429 不触发 Key 跨请求暂停；请求级 cooldown 到期后可在后续轮次重新候选。

## 状态码处理

| 结果 | Key 行为 | 当前渠道行为 |
|---|---|---|
| 401 | 永久跳过；增加 `RequestCount` 和 401 计数 | 选择下一个 Channel/Key |
| 429 | 临时冷却；增加 429 计数 | 选择其他 Key，必要时等待冷却 |
| 400 | Key 按 429 统计并 cooldown | 不写中间 429，继续下一个 Channel/Key |
| 其他 4xx | 按实际状态统计 | 返回 `FailoverError`，继续其他 Channel |
| 5xx | 当前轮已尝试；增加 5xx 计数 | 立即交错到下一个 Channel，下一轮可再次候选 |
| 连接错误 | 当前轮已尝试；增加网络错误计数 | 与 5xx 相同 |
| 成功 | 增加请求计数，清除连续错误和暂停 | 结束轮转 |

401 不会删除 Key 的历史错误统计，并会持久化永久跳过状态。

## Key 暂停

其他 4xx、网络错误和流错误会增加 `ConsecErrors`。400/429 和 5xx 使用专门统计，不触发这一跨请求暂停。达到阈值时：

```text
pause = (ConsecErrors - consec_error_threshold + 1) * pause_multiplier_sec
pause = min(pause, pause_max_sec)
```

默认配置下，第 3、4、5 次连续错误分别暂停 30、60、90 秒。成功会把 `ConsecErrors` 清零并解除暂停。

## 总超时

`max_total_wait_ms` 通过派生 Context deadline 覆盖 Key 选择、429 等待、HTTP 请求、非流式读取和流式消费。它与以下限制同时存在：

- `request_timeout_ms`：单个 Channel HTTP client 的总请求超时。
- `failover.total_timeout_ms`：跨多个 Channel 的总超时。

最先到期的限制终止当前操作。流式响应已经向客户端提交 200 后，超时只能终止流，不能切换 Key 重放。

## 与 Failover 的关系

`failover.enabled=true` 时，执行器遍历所有健康候选 Channel；关闭时只选择一个 Channel，但仍对该 Channel 执行完整 Key 轮次。

ChannelHealth 只累计明确的连接错误和上游 5xx。401、429、400、其他 4xx、客户端取消和协议转换错误不会暂停整个渠道。

## 延迟口径

- Key 平均延迟：每个 Key 的单次 HTTP attempt 耗时，失败 attempt 也计入。
- 系统总延迟：从 Handler 接受请求到应答处理结束，写入请求日志和汇总指标。

两者不能互换；`least-latency` 只使用 Key 单次 attempt 的平均延迟。

## 配置示例

```yaml
channels:
  - id: my-channel
    retry:
      retry_delay_429_ms: 1000
      max_rotation_rounds: 3
      max_total_wait_ms: 30000
      consec_error_threshold: 3
      pause_multiplier_sec: 30
      pause_max_sec: 600
```

## 已删除字段

`max_retries`、`retry_delay_ms`、`failover.max_channel_attempts` 和 `failover.consecutive_fail_threshold` 已删除，配置加载会给出替代字段提示。
