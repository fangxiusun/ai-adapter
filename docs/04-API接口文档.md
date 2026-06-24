# resp2chat API 接口文档

## 1. 概述

resp2chat 提供两类 API：

- **代理 API**：面向 LLM 客户端的请求端点
- **Admin API**：面向管理界面的 REST 端点

基础地址：`http://localhost:8080`（默认）

## 2. 代理 API

### 2.1 POST /v1/responses

**用途**：Responses API 端点（Codex 使用）

**请求体**：OpenAI Responses API 格式

```json
{
  "model": "mimo-v2.5-pro",
  "input": [
    {
      "type": "message",
      "role": "user",
      "content": [{"type": "input_text", "text": "Hello"}]
    }
  ],
  "instructions": "You are a helpful assistant.",
  "tools": [...],
  "stream": true,
  "reasoning": {"effort": "high"}
}
```

**响应**：Responses API 格式（流式为 SSE）

**翻译逻辑**：
- `instructions` → `messages[0]` (system)
- `input[]` → `messages[]` (user/assistant/tool)
- `tools[]` → Chat 格式工具
- `reasoning.effort` → `reasoning_effort`

### 2.2 POST /v1/chat/completions

**用途**：Chat Completions API 端点

**请求体**：OpenAI Chat Completions 格式

```json
{
  "model": "mimo-v2.5-pro",
  "messages": [
    {"role": "user", "content": "Hello"}
  ],
  "stream": true,
  "max_completion_tokens": 4096
}
```

**响应**：Chat Completions 格式

### 2.3 渠道路由

请求通过 `model` 字段自动路由到匹配的渠道：

```
1. 遍历所有启用的渠道
2. 检查渠道的 models 列表是否包含请求的 model
3. 如果匹配 → 使用该渠道
4. 如果不匹配 → 使用默认渠道的 default_model
```

### 2.4 翻译方向

根据渠道的 `wire_api` 配置决定翻译方向：

| 客户端请求 | 渠道 wire_api | 翻译方向 |
|-----------|--------------|----------|
| Responses | `chat` | Responses → Chat |
| Responses | `responses` | 透传 |
| Chat | `chat` | 透传 |
| Chat | `responses` | Chat → Responses |

## 3. Admin API

### 3.1 健康检查

**GET /admin/api/health**

**响应**：
```json
{
  "ok": true,
  "version": "1.0.0"
}
```

### 3.2 统计信息

**GET /admin/api/stats**

**响应**：
```json
{
  "log_count": 1234,
  "channels": [
    {
      "id": "mimo",
      "name": "MiMo",
      "enabled": true,
      "key_count": 2,
      "key_stats": [
        {
          "name": "主 key",
          "request_count": 500,
          "error_count": 10,
          "avg_latency_ms": 1234,
          "last_success_time": "2024-01-01T12:00:00Z",
          "last_error_time": "2024-01-01T11:00:00Z",
          "paused": false
        }
      ]
    }
  ]
}
```

### 3.3 渠道管理

**GET /admin/api/channels** — 渠道列表

**GET /admin/api/channels/:id** — 渠道详情

**POST /admin/api/channels/:id/test** — 测试渠道

**响应**：
```json
{
  "ok": true,
  "key": "主 key",
  "status": "available"
}
```

### 3.4 Key 管理

**GET /admin/api/channels/:id/keys** — Key 列表

**POST /admin/api/channels/:id/keys** — Key 操作

**请求体**：
```json
{
  "action": "pause",  // "pause" | "resume"
  "key": "主 key"
}
```

### 3.5 日志查询

**GET /admin/api/logs**

**查询参数**：
- `channel`：渠道 ID
- `statusMin` / `statusMax`：状态码范围
- `from` / `to`：时间戳范围
- `limit`：返回数量（默认 100）
- `offset`：偏移量

**响应**：
```json
{
  "logs": [
    {
      "id": 1,
      "request_id": "req_1234567890",
      "timestamp": 1704067200000,
      "channel_id": "mimo",
      "model": "mimo-v2.5-pro",
      "status_code": 200,
      "latency_ms": 1234,
      "key_name": "sk-x***xxx"
    }
  ]
}
```

### 3.6 配置查看

**GET /admin/api/config**

**响应**：
```json
{
  "server": {"host": "0.0.0.0", "port": 8080},
  "logging": {"level": "info"},
  "channels": 2
}
```

## 4. SSE 事件格式

流式响应使用 Server-Sent Events：

### 4.1 Responses SSE

```
event: response.created
data: {"type":"response.created","response":{...},"sequence_number":0}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{...},"sequence_number":1}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello","sequence_number":2}

event: response.output_text.done
data: {"type":"response.output_text.done","text":"Hello world","sequence_number":3}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{...},"sequence_number":4}

event: response.completed
data: {"type":"response.completed","response":{...},"sequence_number":5}
```

### 4.2 Chat SSE

```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"delta":{"content":"Hello"}}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"delta":{"content":" world"}}]}

data: [DONE]
```

## 5. 错误格式

所有 API 错误返回统一格式：

```json
{
  "error": {
    "type": "error",
    "code": "error_code",
    "message": "Error message",
    "status": 400
  }
}
```

| HTTP 状态码 | 错误代码 | 说明 |
|------------|----------|------|
| 400 | `invalid_json` | 请求体 JSON 解析失败 |
| 400 | `missing_model` | 请求缺少 model 字段 |
| 400 | `translate_failed` | 协议翻译失败 |
| 404 | `no_channel` | 未找到匹配的渠道 |
| 500 | `create_request_failed` | 创建上游请求失败 |
| 502 | `upstream_error` | 上游返回错误（重试后仍失败） |
| 502 | `invalid_upstream_response` | 上游响应格式错误 |
| 502 | `all_retries_failed` | 所有重试都失败（含 401/429/5xx） |
| 503 | `no_available_keys` | 所有 key 都不可用（401 永久跳过 + 暂停） |
