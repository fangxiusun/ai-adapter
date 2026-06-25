# Native Claude Format 与 Gemini Text Chat API 说明

本文档介绍两种常见 LLM 请求格式的 API 定义要点：**Anthropic Claude 的原生格式（Messages API）** 与 **Google Gemini 的 Text Chat 格式（generateContent）**，并分别说明 **非流式** 与 **流式** 请求的差异。以下内容基于公开通用规范整理，供接入与协议映射参考；若生产接入，请以厂商最新官方文档为准。

> 适用场景：你在做协议网关/代理时，需要理解“Claude 原生”与“Gemini 文本对话”两种请求体/响应体结构、Header、以及 stream 行为差异。

---

## 1. Native Claude Format（Anthropic Messages API）

### 1.1 基本信息
- 典型 Endpoint：`POST /v1/messages`
- 鉴权与头信息（常见）：
  - `x-api-key: <ANTHROPIC_API_KEY>`
  - `anthropic-version: <版本号，如 2023-06-01>`
  - `content-type: application/json`
- 请求语义：围绕 `messages`（对话历史）、`max_tokens`、`model`、可选 `system`、`stream` 等字段构建。

### 1.2 非流式请求（stream=false）

**请求（Request）示例**
```json
{
  "model": "claude-xxx",
  "max_tokens": 1024,
  "system": "You are a helpful assistant.",
  "messages": [
    {"role": "user", "content": "用一句话介绍 Claude API"}
  ],
  "stream": false
}
```

**响应（Response）要点**
- 返回完整 JSON，包含输出文本和用量信息。
- 结构中通常包含：
  - `id`: 请求/响应标识
  - `type`: 固定值（如 `message`）
  - `role`: 通常为 `assistant`
  - `content`: 内容块数组/文本块（常见为带 `type` 的 block）
  - `stop_reason`: 结束原因（如 `end_turn`, `max_tokens` 等）
  - `usage`: token 用量（如 `input_tokens`, `output_tokens`）

**响应示例（示意）**
```json
{
  "id": "msg_xxx",
  "type": "message",
  "role": "assistant",
  "content": [{"type": "text", "text": "Claude API 是 Anthropic 的对话接口..."}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 23, "output_tokens": 51}
}
```

### 1.3 流式请求（stream=true）

- 请求：与非流式一致，仅 `stream: true`。
- 响应：以 **SSE（Server-Sent Events）** 形式逐步返回事件。
- 常见事件类型（示意）：
  - `message_start`: 消息开始，包含基础元信息
  - `content_block_start` / `content_block_delta` / `content_block_stop`: 内容块的开始、增量、结束
  - `message_delta`: 消息级别更新（如 stop_reason）
  - `message_end`: 消息结束
- 实现要点：
  - 客户端按行读取 `data: {...}`，按事件类型拼接文本。
  - 错误事件可能穿插在流中，需要异常处理。

---

## 2. Gemini Text Chat（Google generateContent）

### 2.1 基本信息
- 典型 Endpoint：
  - 非流式：`POST /v1beta/models/{model}:generateContent`
  - 流式：`POST /v1beta/models/{model}:streamGenerateContent`
- 鉴权与头信息（常见）：
  - 使用 API Key 查询参数 `?key=...`，或 OAuth/Bearer（取决于接入方式）
  - `content-type: application/json`
- 请求语义：以 `contents` 数组承载对话历史，配合 `generationConfig` 控制输出。

### 2.2 非流式请求（generateContent）

**请求（Request）示例**
```json
{
  "contents": [
    {"role": "user", "parts": [{"text": "用一句话介绍 Gemini API"}]}
  ],
  "generationConfig": {
    "temperature": 0.7,
    "maxOutputTokens": 1024
  }
}
```

**响应（Response）要点**
- 返回 `candidates` 列表，常见结构：
  - `content.role`: 通常为 `model`
  - `content.parts`: 内容块（文本等）
  - `finishReason`: 结束原因（如 `STOP`, `MAX_TOKENS` 等）
  - `usageMetadata`: 用量信息（如 `promptTokenCount`, `candidatesTokenCount`）

**响应示例（示意）**
```json
{
  "candidates": [
    {
      "content": {"role": "model", "parts": [{"text": "Gemini API 是 Google 的生成接口..."}]},
      "finishReason": "STOP"
    }
  ],
  "usageMetadata": {"promptTokenCount": 12, "candidatesTokenCount": 45}
}
```

### 2.3 流式请求（streamGenerateContent）

- 请求：与非流式一致，使用 `streamGenerateContent` 接口。
- 响应：常见为 **JSON 流**（NDJSON/分块 JSON），每块包含一个 `candidate` 增量。
- 解析要点：
  - 逐块读取并解析 JSON，按 `parts` 拼接文本。
  - 流结束通常有明确终止块或连接关闭。
  - 需处理网络中断与部分返回的重试策略。

---

## 3. 关键差异对照（便于网关映射）

| 维度 | Claude (Messages API) | Gemini (generateContent) |
|---|---|---|
| 典型路径 | `/v1/messages` | `...:generateContent` / `...:streamGenerateContent` |
| 角色与内容结构 | `messages[].role` + `content` block | `contents[].role` + `parts[].text` |
| 流式协议 | SSE 事件流（事件类型分层） | JSON 分块流/NDJSON |
| 结束标识 | `stop_reason` / 事件 `message_end` | `finishReason` / 流终止 |
| 用量信息 | `usage` 字段 | `usageMetadata` 字段 |

---

## 4. 接入建议（面向代理层）

- **统一抽象层**：为 Claude 与 Gemini 分别建立请求/响应解析器，统一映射到内部模型（如：输入消息、输出文本、结束原因、用量）。
- **流式转发**：Claude 使用 SSE，Gemini 可能是 NDJSON/JSON 流；转发时保持逐块透传，避免在代理层缓存整条消息。
- **错误处理**：Claude 可在流中返回错误事件；Gemini 可返回错误对象或中断流。代理层需统一错误码映射。
- **兼容性**：保持最小必填字段集，兼容厂商扩展字段（如推理、工具调用、系统提示等）。

---

## 5. 快速示例（curl）

### Claude 非流式（示意）
```bash
curl -X POST https://api.anthropic.com/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{
    "model":"claude-xxx",
    "max_tokens":256,
    "messages":[{"role":"user","content":"hi"}],
    "stream":false
  }'
```

### Gemini 非流式（示意）
```bash
curl -X POST "https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-flash:generateContent?key=$GOOGLE_API_KEY" \
  -H "content-type: application/json" \
  -d '{
    "contents":[{"role":"user","parts":[{"text":"hi"}]}]
  }'
```

---

*说明：本文档用于格式级参考与协议映射设计，不同厂商/模型版本可能存在字段差异，请结合官方接口文档校验关键字段。*