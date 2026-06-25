# ai-adapter API 接口文档

## 1. 概述

ai-adapter 提供两类 API：

- **代理 API**：面向 LLM 客户端，支持四类协议端点
- **Admin API**：面向管理界面的 REST 端点

基础地址：`http://localhost:8080`（默认）

## 2. 代理 API

### 2.1 POST /v1/chat/completions

OpenAI Chat Completions 协议。

```json
{
  "model": "mimo-v2.5-pro",
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Hello"}
  ],
  "stream": false,
  "temperature": 0.7,
  "max_completion_tokens": 4096
}
```

### 2.2 POST /v1/responses

OpenAI Responses 协议。

```json
{
  "model": "mimo-v2.5-pro",
  "input": [
    {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Hello"}]}
  ],
  "instructions": "You are a helpful assistant.",
  "stream": true,
  "reasoning": {"effort": "high"}
}
```

### 2.3 POST /v1/messages

Anthropic Claude Messages 协议。

```json
{
  "model": "claude-sonnet-4-20250514",
  "max_tokens": 4096,
  "system": "You are a helpful assistant.",
  "messages": [
    {"role": "user", "content": "Hello"}
  ],
  "stream": false
}
```

支持 Claude 特有字段：`system`（系统提示）、`tools`（工具定义，使用 `input_schema`）、`tool_choice`、`stop_sequences`。

### 2.4 POST /v1beta/models/{model}:generateContent

Google Gemini generateContent 协议（非流式）。

```json
{
  "contents": [
    {"role": "user", "parts": [{"text": "Hello"}]}
  ],
  "generationConfig": {
    "temperature": 0.7,
    "maxOutputTokens": 4096
  }
}
```

### 2.5 POST /v1beta/models/{model}:streamGenerateContent

Google Gemini 流式协议。请求体与非流式相同，URL 路径中使用 `streamGenerateContent`。

### 2.6 渠道路由

请求通过 `model` 字段（或 Gemini URL 路径中的模型名）自动路由到匹配的渠道：

1. 遍历所有启用的渠道
2. 检查渠道的 `models` 列表（含 `aliases`）是否匹配
3. 匹配到则使用该渠道
4. 均未匹配则回退到默认渠道的 `default_model`

### 2.7 接口能力与转换

系统根据目标接口和渠道的接口能力 URL 自动决策：

- **原生转发**：渠道配置了目标接口 URL → 直接转发（复杂度 0）
- **协议转换**：从已配置的源接口中选择复杂度最低的路径
- **无可用路径**：返回 503 `no_conversion_path`

支持全部 12 个方向的非流式和流式转换。

## 3. Admin API

### 3.1 健康检查

**GET /admin/api/health**

```json
{"ok": true, "version": "1.0.0"}
```

### 3.2 统计信息

**GET /admin/api/stats**

```json
{
  "log_count": 1234,
  "channels": [
    {
      "id": "mimo",
      "name": "MiMo",
      "enabled": true,
      "key_count": 2,
      "key_stats": [...]
    }
  ]
}
```

### 3.3 渠道管理

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/admin/api/channels` | 渠道列表（含接口能力 URL、Key 统计） |
| GET | `/admin/api/channels/:id` | 渠道详情 |
| POST | `/admin/api/channels/:id/test` | 测试渠道 Key 可用性 |

### 3.4 Key 管理

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/admin/api/channels/:id/keys` | Key 列表及状态 |
| POST | `/admin/api/channels/:id/keys` | 操作：`{"action":"pause","key":"name"}` 或 `{"action":"resume","key":"name"}` |

### 3.5 日志查询

**GET /admin/api/logs**

| 参数 | 类型 | 说明 |
|------|------|------|
| `channel` | string | 渠道 ID 过滤 |
| `statusMin` / `statusMax` | int | 状态码范围 |
| `from` / `to` | int | 时间戳范围（毫秒） |
| `limit` | int | 返回数量（默认 100） |
| `offset` | int | 偏移量 |

### 3.6 配置查看

**GET /admin/api/config**

返回服务器、日志、渠道数量等摘要信息。

## 4. SSE 事件格式

### 4.1 Responses SSE

```
event: response.created
data: {"type":"response.created","response":{...},"sequence_number":0}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello","sequence_number":1}

event: response.completed
data: {"type":"response.completed","response":{...},"sequence_number":2}
```

### 4.2 Chat SSE

```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"delta":{"content":"Hello"}}]}

data: [DONE]
```

### 4.3 Claude SSE

```
event: message_start
data: {"type":"message_start","message":{...}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}

event: message_stop
data: {"type":"message_stop"}
```

### 4.4 Gemini 流式

每行一个完整 JSON 对象（非 SSE 格式）：

```json
{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":10,"totalTokenCount":15}}
```

## 5. 错误格式

所有 API 错误返回统一 JSON 格式：

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

| HTTP 码 | 错误代码 | 说明 |
|---------|----------|------|
| 400 | `invalid_json` | 请求体 JSON 解析失败 |
| 400 | `missing_model` | 请求缺少 model |
| 400 | `convert_failed` | 协议转换失败 |
| 404 | `no_channel` | 未找到匹配渠道 |
| 502 | `upstream_error` | 上游错误（重试后仍失败） |
| 503 | `no_available_keys` | 所有 Key 不可用 |
| 503 | `no_conversion_path` | 无可用转换路径 |
| 504 | `timeout` | 重试总超时 |