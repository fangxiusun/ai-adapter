# Fanout 并发请求配置指南

## 概述

Fanout 是一种并发请求策略：将同一个请求同时发送到多个 Key，取第一个成功（或最快）的响应返回。

与 Key 轮转（串行重试）互斥 — 启用 fanout 后，不再走轮转逻辑。

## 工作原理

### 非流式请求

```
Client Request
    │
    ├─ Fanout: 并发发送到 Key-1, Key-2, Key-3
    │   ├─ Key-1: 200 OK (150ms)  ← 选中（wait_all=false 时第一个成功即返回）
    │   ├─ Key-2: 200 OK (200ms)  ← 取消
    │   └─ Key-3: 200 OK (300ms)  ← 取消
    │
    └─ 返回 Key-1 的响应
```

### 流式请求

```
Client Request (SSE)
    │
    ├─ Fanout: 并发发送到 Key-1, Key-2, Key-3
    │   ├─ Key-1: 200 OK → 开始推 SSE ──→ 转发给 Client ✓
    │   ├─ Key-2: 200 OK → 开始推 SSE ──→ cancel ✗
    │   └─ Key-3: 等待中...            ──→ cancel ✗
    │
    └─ 持续转发 Key-1 的 SSE 流
```

### wait_all 模式

```
Client Request
    │
    ├─ Fanout: 并发发送到 Key-1, Key-2, Key-3
    │   ├─ Key-1: 200 OK (300ms)
    │   ├─ Key-2: 200 OK (150ms)  ← 选中（最快）
    │   └─ Key-3: 500 Error       ← 报告错误
    │
    └─ 返回 Key-2 的响应（延迟最低）
```

## 配置说明

```yaml
channels:
  - id: "mimo"
    name: "MiMo"
    fanout:
      enabled: true       # 启用 fanout（默认 false）
      count: 3            # 并发请求数（默认 2）
      wait_all: false     # 是否等待所有响应（默认 false）
```

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enabled` | bool | `false` | 是否启用 fanout |
| `count` | int | `2` | 并发发送到几个 Key |
| `wait_all` | bool | `false` | `false`=第一个成功即返回；`true`=等待全部，选最快 |

### count 说明

- `count` 不能超过渠道可用 Key 数量
- 如果可用 Key 数量不足 `count`，使用所有可用 Key
- `count` 越大，延迟越低，但资源消耗越高

### wait_all 说明

| 模式 | 行为 | 适用场景 |
|------|------|---------|
| `false` | 第一个 200 响应立即返回，取消其余 | 追求最低延迟 |
| `true` | 等待所有响应完成，选延迟最低的 200 | 追求最稳定的结果，可接受额外等待 |

## Fanout vs Key 轮转

两种策略互斥，根据场景选择：

| 维度 | Key 轮转（fanout=false） | Fanout（fanout=true） |
|------|------------------------|-----------------------|
| 方式 | 串行：key-1 → 失败 → key-2 | 并行：key-1 + key-2 同时发 |
| 延迟 | 高（每次失败重试增加延迟） | 低（取最快的响应） |
| 资源 | 低（每次只用 1 个 Key） | 高（同时占用多个 Key） |
| 适用 | Key 充足、对延迟不敏感 | 延迟敏感、Key 充裕 |
| 配置 | `fanout.enabled: false` + `retry` 配置 | `fanout.enabled: true` |

### 选择建议

- **延迟敏感**（如实时对话）→ 启用 fanout
- **成本敏感**（Key 有限）→ 使用轮转
- **高可用**（Key 充裕）→ 启用 fanout + `wait_all: false`

## 与故障转移的关系

Fanout 是**渠道内**的 Key 级并发策略，故障转移是**跨渠道**的切换策略。两者独立运作：

```
请求 → 故障转移循环（跨渠道）
         │
         └─ 渠道内处理：
              ├─ fanout enabled → 并发多 Key
              └─ fanout disabled → 串行轮转 Key
```

- Fanout 全部失败 → 触发故障转移到下一渠道（如果启用）
- 故障转移的判断逻辑不变（5xx/连接失败 → 转移）

## 完整配置示例

```yaml
failover:
  enabled: true
  max_channel_attempts: 3
  total_timeout_ms: 120000
  consecutive_fail_threshold: 2

channels:
  # 主力渠道：启用 fanout，追求低延迟
  - id: "mimo-fast"
    name: "MiMo 低延迟"
    priority: 10
    chat_url: "https://fast.example.com"
    fanout:
      enabled: true
      count: 3
      wait_all: false
    models:
      - id: "mimo-v2.5-pro"
    keys:
      - value: "sk-key-1"
      - value: "sk-key-2"
      - value: "sk-key-3"

  # 备用渠道：不启用 fanout，使用轮转
  - id: "mimo-fallback"
    name: "MiMo 备用"
    priority: 20
    chat_url: "https://fallback.example.com"
    fanout:
      enabled: false
    retry:
      max_rotation_rounds: 3
      retry_delay_429_ms: 1000
    models:
      - id: "mimo-v2.5-pro"
    keys:
      - value: "sk-key-4"
      - value: "sk-key-5"
```

## 日志和监控

Fanout 事件会在日志中记录：

```
level=DEBUG msg=fanout channel=mimo keys=3 strategy=round-robin
level=WARN  msg=fanout_stream_failed request_id=xxx channel=mimo error="all fanout keys failed (3 attempts)"
```

Prometheus 指标中，fanout 请求的指标与普通请求一致（`ai_adapter_http_requests_total` 等），不单独区分。
