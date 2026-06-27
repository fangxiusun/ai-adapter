# Metrics 监控指南

## 概述

ai-adapter 内置 Prometheus 格式的 metrics 端点，可用于监控请求量、延迟、Token 使用量、错误率等关键指标。

## 端点

```
GET /metrics
```

返回 Prometheus exposition format 格式的 metrics 数据，可直接配置 Prometheus server 抓取。

## 指标列表

### HTTP 请求指标

| 指标名称 | 类型 | 标签 | 说明 |
|----------|------|------|------|
| `ai_adapter_http_requests_total` | Counter | channel, model, api, status | 请求总数 |
| `ai_adapter_http_request_duration_seconds` | Histogram | channel, model, api | 请求延迟（秒） |
| `ai_adapter_http_active_requests` | Gauge | - | 当前活跃请求数 |
| `ai_adapter_http_errors_total` | Counter | channel, model, error_code | 错误总数 |

### Token 使用指标

| 指标名称 | 类型 | 标签 | 说明 |
|----------|------|------|------|
| `ai_adapter_tokens_prompt_total` | Counter | channel, model | Prompt token 总数 |
| `ai_adapter_tokens_completion_total` | Counter | channel, model | Completion token 总数 |
| `ai_adapter_tokens_total_total` | Counter | channel, model | Token 总数（prompt + completion） |

### Key 管理指标

| 指标名称 | 类型 | 标签 | 说明 |
|----------|------|------|------|
| `ai_adapter_keys_usage_total` | Counter | channel, key | Key 使用次数 |
| `ai_adapter_keys_errors_total` | Counter | channel, key, error_type | Key 错误次数（按类型） |
| `ai_adapter_keys_rate_limited_total` | Counter | channel, key | Key 被限流次数 |

### 上游服务指标

| 指标名称 | 类型 | 标签 | 说明 |
|----------|------|------|------|
| `ai_adapter_upstream_latency_seconds` | Histogram | channel, model | 上游响应延迟（秒） |

## 标签说明

- **channel**: 渠道 ID（如 `mimo`、`deepseek`）
- **model**: 模型名称（如 `mimo-v2.5-pro`）
- **api**: API 类型（`chat`、`responses`、`messages`、`generate_content`）
- **status**: HTTP 状态码（如 `200`、`401`、`500`）
- **key**: Key 名称（脱敏后的标识）
- **error_code**: 错误代码（如 `http_401`、`no_channel`）

## Histogram Bucket 配置

延迟指标使用以下 bucket（秒）：

```
0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120
```

## Prometheus 配置示例

```yaml
# prometheus.yml
scrape_configs:
  - job_name: 'ai-adapter'
    scrape_interval: 15s
    static_configs:
      - targets: ['localhost:8080']
    metrics_path: '/metrics'
```

## Grafana Dashboard

推荐的 PromQL 查询示例：

### 请求速率（QPS）

```promql
rate(ai_adapter_http_requests_total[5m])
```

### 平均延迟

```promql
rate(ai_adapter_http_request_duration_seconds_sum[5m]) / rate(ai_adapter_http_request_duration_seconds_count[5m])
```

### P99 延迟

```promql
histogram_quantile(0.99, rate(ai_adapter_http_request_duration_seconds_bucket[5m]))
```

### 错误率

```promql
rate(ai_adapter_http_errors_total[5m]) / rate(ai_adapter_http_requests_total[5m])
```

### Token 使用速率

```promql
rate(ai_adapter_tokens_total_total[5m])
```

### Key 限流次数

```promql
rate(ai_adapter_keys_rate_limited_total[5m])
```

## Go 运行时指标

除自定义指标外，端点还自动导出 Go 运行时指标：

- `go_gc_duration_seconds` — GC 暂停时间
- `go_goroutines` — Goroutine 数量
- `go_memstats_*` — 内存统计
- `process_cpu_seconds_total` — CPU 使用时间
- `process_resident_memory_bytes` — 常驻内存大小
- `process_open_fds` — 打开的文件描述符数量

## 告警规则示例

```yaml
# prometheus-rules.yml
groups:
  - name: ai-adapter
    rules:
      - alert: HighErrorRate
        expr: rate(ai_adapter_http_errors_total[5m]) / rate(ai_adapter_http_requests_total[5m]) > 0.05
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "错误率超过 5%"

      - alert: HighLatency
        expr: histogram_quantile(0.99, rate(ai_adapter_http_request_duration_seconds_bucket[5m])) > 30
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "P99 延迟超过 30 秒"

      - alert: KeyRateLimited
        expr: rate(ai_adapter_keys_rate_limited_total[5m]) > 0.1
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "Key 频繁被限流"
