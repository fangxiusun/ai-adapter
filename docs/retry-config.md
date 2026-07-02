# RetryConfig 参数说明文档

> 定义位置: internal/config/config.go:87-95

RetryConfig 控制 **单个 Channel 内** 的请求重试行为，包括 429 限流时的键轮换、错误累计暂停策略，以及整体超时保护。

---

## 参数总览

| 参数 | 类型 | 默认值 | YAML Key | 作用 |
|------|------|--------|----------|------|
| RetryDelay429Ms | int | 500 | etry_delay_429_ms | 429 限流后的等待时间（毫秒） |
| MaxRotationRounds | int | 3 | max_rotation_rounds | 最大键轮换轮数 |
| MaxTotalWaitMs | int | 30000 | max_total_wait_ms | 单次请求最大总等待时间（毫秒） |
| ConsecErrorThreshold | int | 3 | consec_error_threshold | 触发键暂停的连续错误阈值 |
| PauseMultiplierSec | int | 30 | pause_multiplier_sec | 暂停时间的递增倍数（秒） |
| PauseMaxSec | int | 600 | pause_max_sec | 单个键的最大暂停时间（秒） |

---

## 详细说明

### 1. etry_delay_429_ms — 429 重试延迟

**作用**: 当请求收到上游 429 Too Many Requests 响应时，排除当前 key 后等待该时间再尝试下一个 key。

**代码位置**:
- 配置默认值: config.go:224-225
- 转换为 	ime.Duration: etry.go:41
- 实际使用（非流式）: orward.go:139
- 实际使用（流式）: stream.go:155

**核心逻辑** (orward.go:134-139):
`go
if resp.StatusCode == 429 {
    resp.Body.Close()
    ch.ReportError(key.Value, 429)
    rs.excluded[key.Value] = true
    h.logger.Warn("key_excluded", ...)
    time.Sleep(rs.retryDelay)  // ← 在此等待
    continue
}
`

**设计意图**: 限流时快速换 key，但留一小段冷却时间避免持续触发限流。建议值 500ms-2000ms。

---

### 2. max_rotation_rounds — 最大轮换轮数

**作用**: 限制单次请求中 key 轮换的总轮数。每轮会尝试所有可用 key，超过此数值后直接返回 503 错误。

**代码位置**:
- 配置默认值: config.go:227-228
- 初始化: etry.go:40
- 检查: etry.go:79-83

**核心逻辑** (etry.go:79-83):
`go
func (h *ProxyHandler) checkRotationAndTimeout(ch *channel.Channel, rs *RetryState, reqID string) *FailoverError {
    rs.rounds++
    if rs.maxRounds > 0 && rs.rounds > rs.maxRounds {
        return &FailoverError{StatusCode: 503, Message: "max rotation rounds exceeded"}
    }
    ...
}
`

**设计意图**: 防止在所有 key 都不可用时无限重试。每排除一个 key（429/401/5xx/连接错误）后继续尝试下一个，一轮用完所有 key 后算一轮。默认 3 轮意味着最多尝试 3 × key数量 次。

---

### 3. max_total_wait_ms — 最大总等待时间

**作用**: 从请求开始计时，超过该时间后立即终止重试，返回 504 超时错误。这是整个重试循环的硬性时间上限。

**代码位置**:
- 配置默认值: config.go:230-231
- 初始化: etry.go:42
- 检查: etry.go:53-55, etry.go:80-81

**核心逻辑** (etry.go:53-55):
`go
func (rs *RetryState) isTimedOut() bool {
    return time.Since(rs.start) >= rs.maxTotalWait
}
`

**设计意图**: 无论轮换了多少轮，只要总耗时超过此值就立即放弃。默认 30 秒，与一般 HTTP 请求超时对齐。此参数与 checkRotationAndTimeout 配合，两个条件（轮数、时间）哪个先触发就用哪个。

---

### 4. consec_error_threshold — 连续错误暂停阈值

**作用**: 当某个 key 连续出错达到此阈值时，触发该 key 的**自动暂停**机制。暂停期间该 key 不会被分配新请求。

**代码位置**:
- 配置默认值: config.go:233-234
- 初始化到 KeyState: keystate.go:36-38
- 触发暂停判断: keystate.go:130, keystate.go:170
- 传入路径: channel.go:77 → keypool.go:34-44

**核心逻辑** (keystate.go:126-135):
`go
if ks.ConsecErrors >= ks.consecThreshold {
    pauseSec := (ks.ConsecErrors - ks.consecThreshold + 1) * ks.pauseMultiplierSec
    pauseDuration := time.Duration(pauseSec) * time.Second
    maxDuration := time.Duration(ks.pauseMaxSec) * time.Second
    if pauseDuration > maxDuration {
        pauseDuration = maxDuration
    }
    ks.Paused = true
    ks.PauseUntil = now.Add(pauseDuration)
}
`

**触发场景**: 429、400、403、404、4xx、5xx、网络错误、流解析错误都会累加 ConsecErrors。只有**成功请求**才会重置该计数器。

**设计意图**: 快速隔离"坏 key"，避免对已知有问题的 key 反复尝试浪费时间。

---

### 5. pause_multiplier_sec — 暂停递增倍数

**作用**: 计算暂停时长的系数。暂停时长公式为：

`
pause_duration = (ConsecErrors - threshold + 1) × pause_multiplier_sec
`

即超过阈值后每多错一次，暂停时间线性增长。

**代码位置**: keystate.go:130, keystate.go:170

**暂停时长示例**（默认 threshold=3, multiplier=30s）:

| 连续错误数 | 超出阈值 | 暂停时长 |
|-----------|---------|---------|
| 3 | 0 | 30 秒 (1×30) |
| 4 | 1 | 60 秒 (2×30) |
| 5 | 2 | 90 秒 (3×30) |
| 6 | 3 | 120 秒 (4×30) |

**设计意图**: 采用线性递增的退避策略，错误越多暂停越久，但不会无限增长（受 pause_max_sec 限制）。

---

### 6. pause_max_sec — 最大暂停时长

**作用**: 限制单个 key 的暂停时长上限，无论连续错误多少次，暂停时间不会超过此值。

**代码位置**: keystate.go:132, keystate.go:172

**核心逻辑**:
`go
maxDuration := time.Duration(ks.pauseMaxSec) * time.Second
if pauseDuration > maxDuration {
    pauseDuration = maxDuration
}
`

**设计意图**: 防止因长时间持续错误导致 key 被暂停过久（例如数小时），确保 key 有机会在恢复后重新参与调度。默认 10 分钟。

---

## 完整工作流程

`
请求进入 Channel
    │
    ▼
┌─────────────────────────────┐
│  检查轮换轮数和总超时         │ ← MaxRotationRounds / MaxTotalWaitMs
│  (checkRotationAndTimeout)   │
└──────────┬──────────────────┘
           │ 未超限
           ▼
┌─────────────────────────────┐
│  从 KeyPool 获取下一个可用 key │ ← 跳过已暂停的 key
│  (getNextKey)                │    (ConsecErrorThreshold 触发暂停)
└──────────┬──────────────────┘
           │ 有可用 key
           ▼
┌─────────────────────────────┐
│  发送请求到上游 API           │
└──────────┬──────────────────┘
           │
     ┌─────┴─────┬──────────┬──────────┐
     ▼           ▼          ▼          ▼
   200 OK      429        5xx       401/连接错误
   记录成功   排除该key    排除该key    排除该key
   结束流程   Sleep等待     累计错误     累计错误
              (RetryDelay429Ms)
              继续循环     继续循环     继续循环
                                │
                                ▼
                    ConsecErrors >= threshold?
                        │是          │否
                        ▼           ▼
                    暂停该key    继续尝试
                    (PauseMultiplierSec × N)
                    (不超过 PauseMaxSec)
`

---

## YAML 配置示例

`yaml
channels:
  - id: "my-channel"
    retry:
      retry_delay_429_ms: 1000     # 429 后等 1 秒再换 key
      max_rotation_rounds: 3       # 最多轮换 3 轮
      max_total_wait_ms: 30000     # 整体不超过 30 秒
      consec_error_threshold: 3    # 连续 3 次错误触发暂停
      pause_multiplier_sec: 30     # 暂停时间线性递增（30s 步长）
      pause_max_sec: 600           # 最多暂停 10 分钟
`

---

## 参数调优建议

| 场景 | 建议 |
|------|------|
| 上游限流严格 | 增大 etry_delay_429_ms（1000-3000ms） |
| key 数量多、质量参差不齐 | 增大 max_rotation_rounds（5-10） |
| 对延迟敏感 | 减小 max_total_wait_ms（10000-15000ms） |
| key 恢复快 | 减小 pause_multiplier_sec（10-15s）和 pause_max_sec（120-300s） |
| 上游不稳定 | 增大 consec_error_threshold（5-10），避免单次抖动就暂停 |
