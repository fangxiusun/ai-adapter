# ai-adapter 重试与 Key 管理策略文档

## 1. 概述

ai-adapter 实现了智能的错误处理和重试机制，核心目标是：**尽可能少地将错误抛给用户，在内部通过重试给用户返回正确结果**。

## 2. 配置项

所有配置在 `config.yaml` 的 `channels[].retry` 下：

```yaml
channels:
  - id: "mimo"
    retry:
      retry_delay_429_ms: 1000        # 429 重试延迟 (ms)
      max_rotation_rounds: 3          # 最大轮转轮数
      max_total_wait_ms: 30000        # 最大总等待时间 (ms)
      consec_error_threshold: 3       # 自动暂停的连续错误阈值
      pause_multiplier_sec: 30        # 暂停时间倍数 (秒)
      pause_max_sec: 600              # 暂停最大时间 (秒)
```

### 2.1 配置项说明

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| `retry_delay_429_ms` | 1000 | 收到 429 后等待的时间（ms），然后切换到下一个 key |
| `max_rotation_rounds` | 3 | 所有 key 都返回 429 时，从头重新轮转的最大次数 |
| `max_total_wait_ms` | 30000 | 最大总等待时间（ms），超时返回最后一个响应给客户端 |
| `consec_error_threshold` | 3 | 连续错误达到此次数时自动暂停 key |
| `pause_multiplier_sec` | 30 | 暂停时间计算倍数（秒） |
| `pause_max_sec` | 600 | 暂停时间上限（秒） |

## 3. 重试策略详解

### 3.1 401 错误处理

```
收到 401 → 永久跳过该 key → 切换到下一个 key → 不重试同一 key
```

- **行为**：该 key 被标记为 `PermanentlySkipped`，后续所有请求都不会使用它
- **日志**：明文打印 key 名称和值（便于排查）
- **恢复**：需手动在 Web UI 中 Resume 或重启代理

### 3.2 429 错误处理

```
收到 429 → 跳过当前 key → 等待 retry_delay_429_ms → 切换到下一个 key → 重试
```

**延迟**：固定为 `retry_delay_429_ms`（默认 1000ms）

**轮转逻辑**：
1. 收到 429 → 跳过当前 key → 等待 → 切换到下一个 key
2. 如果所有 key 都被跳过 → 开始新的轮转轮次（rotationRound++）
3. 如果轮转轮次超过 `max_rotation_rounds` → 返回 503 错误
4. 如果总等待时间超过 `max_total_wait_ms` → 返回超时错误

### 3.3 400/403/404 错误处理

```
收到 400/403/404 → 跳过当前 key → 切换到下一个 key → 立即重试
```

- **行为**：与 5xx 类似，跳过当前 key 并切换到下一个 key
- **统计**：分别记录到 `error_400`、`error_403`、`error_404` 计数器
- **暂停机制**：这些错误也会计入连续错误计数，触发自动暂停

### 3.4 5xx 错误处理

```
收到 5xx → 跳过当前 key → 切换到下一个 key → 不等待直接重试
```

### 3.5 流式解析错误

流式响应解析失败时，记录到 `error_stream` 计数器，并触发与其他错误相同的暂停机制。

### 3.6 自动暂停机制

当某个 key 的连续错误次数达到 `consec_error_threshold` 时：

```
暂停时间 = (连续错误数 - 阈值 + 1) × pause_multiplier_sec
```

**示例**（默认配置：threshold=3, multiplier=30s, max=600s）：

| 连续错误数 | 暂停时间 |
|-----------|----------|
| 3 | 30 秒 |
| 4 | 60 秒 |
| 5 | 90 秒 |
| 6 | 120 秒 |
| ... | ... |
| 23+ | 600 秒（上限） |

### 3.7 429 暂停机制

429 错误也会触发暂停，但使用相同的 `consec_error_threshold` 和 `pause_multiplier_sec`：

```
连续 429 错误 ≥ threshold → 暂停该 key
```

## 4. 轮转与超时

### 4.1 轮转逻辑

```
第 1 轮：尝试所有 key（排除已失败的）
  ↓ 所有 key 都失败
第 2 轮：重置排除列表，重新尝试所有 key
  ↓ 所有 key 都失败
第 3 轮：重置排除列表，重新尝试所有 key
  ↓ 所有 key 都失败
返回 503 错误
```

**配置**：`max_rotation_rounds`（默认 3）

### 4.2 超时机制

```
请求开始 → 记录 start 时间
  ↓ 每次重试检查
  if (now - start) ≥ max_total_wait_ms:
    返回超时错误
  ↓
继续重试
```

**配置**：`max_total_wait_ms`（默认 30000ms = 30秒）

## 5. 完整流程图

```
请求进来
  │
  ▼
获取下一个可用 key
  │
  ├── key == nil → 返回 503 (no_available_keys)
  │
  ▼
发送上游请求
  │
  ├── 成功 (2xx) → 返回结果 ✓
  │
  ├── 401 → 永久跳过 key → 获取下一个 key → 循环
  │
  ├── 429 → 跳过 key → 等待 retry_delay_429_ms → 获取下一个 key
  │         │
  │         ├── 所有 key 都跳过 → 轮转轮次++
  │         │   │
  │         │   ├── 轮次 ≤ max_rotation_rounds → 重置排除列表 → 循环
  │         │   └── 轮次 > max_rotation_rounds → 返回 503
  │         │
  │         └── 检查超时 → 超时 → 返回 502 (timeout)
  │
  ├── 5xx → 跳过 key → 获取下一个 key → 循环
  │
  └── 网络错误 → 跳过 key → 获取下一个 key → 循环
```

## 5. Key 选择策略

系统支持五种 Key 选择策略，通过 `key_strategy` 配置：

| 策略 | 说明 |
|------|------|
| `round-robin` | 轮询（默认） |
| `random` | 随机选择 |
| `least-errors` | 选择错误数最少的 Key |
| `least-latency` | 选择平均延迟最低的 Key |
| `least-rate-limited` | 选择被限流次数最少的 Key |

## 6. 配置示例

### 6.1 保守策略（默认）

```yaml
retry:
  retry_delay_429_ms: 1000        # 等待 1 秒
  max_rotation_rounds: 3          # 最多 3 轮
  max_total_wait_ms: 30000        # 最多等 30 秒
  consec_error_threshold: 3       # 连续 3 次错误暂停
  pause_multiplier_sec: 30        # 暂停 30 秒起
  pause_max_sec: 600              # 最多暂停 10 分钟
```

### 6.2 激进策略（快速失败）

```yaml
retry:
  retry_delay_429_ms: 500         # 等待 0.5 秒
  max_rotation_rounds: 1          # 只轮转 1 轮
  max_total_wait_ms: 5000         # 最多等 5 秒
  consec_error_threshold: 2       # 连续 2 次错误就暂停
  pause_multiplier_sec: 10        # 暂停 10 秒起
  pause_max_sec: 120              # 最多暂停 2 分钟
```

### 6.3 宽容策略（高可用）

```yaml
retry:
  retry_delay_429_ms: 2000        # 等待 2 秒
  max_rotation_rounds: 5          # 最多 5 轮
  max_total_wait_ms: 60000        # 最多等 60 秒
  consec_error_threshold: 5       # 连续 5 次错误才暂停
  pause_multiplier_sec: 60        # 暂停 60 秒起
  pause_max_sec: 1800             # 最多暂停 30 分钟
```

## 7. 日志说明

### 7.1 401 日志

```
WARN key permanently skipped (401) channel=mimo key_name=主 key key_value=sk-xxx reason=401 Unauthorized
```

### 7.2 429 日志

```
WARN key rate limited (429) channel=mimo key_name=主 key rate_limit_count=3
WARN key rate limited (429), retrying request_id=req_xxx key_name=主 key delay=1s
```

### 7.3 轮转日志

```
WARN all keys excluded, starting new rotation round round=2 max_rounds=3
```

### 7.4 超时日志

```
ERROR all retries failed request_id=req_xxx code=all_retries_failed message=all 3 rotation rounds exhausted
ERROR all retries failed request_id=req_xxx code=timeout message=max total wait time 30s exceeded
```

## 8. 错误响应

| 错误代码 | HTTP 状态码 | 说明 |
|----------|------------|------|
| `no_available_keys` | 503 | 所有 key 都不可用（401 永久跳过 + 暂停） |
| `all_retries_failed` | 502 | 所有轮转轮次都失败 |
| `timeout` | 502 | 超过最大总等待时间 |
