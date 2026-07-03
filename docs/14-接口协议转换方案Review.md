# 接口协议转换方案 Review

审查日期：2026-07-03

## 1. 结论摘要

当前系统支持 5 个外部入口：

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/messages`
- `POST /v1beta/models/{model}:generateContent`
- `POST /v1beta/models/{model}:streamGenerateContent`

实现上并不是为每一对协议单独写转换器，而是采用 **Chat Completions 作为中心格式** 的方案：

```text
客户端协议 -> ChatRequest -> 上游原生协议
上游响应 -> ChatResponse -> 客户端协议
```

渠道能力由 `chat_url`、`responses_url`、`messages_url`、`generate_content_url` 决定。调度时先根据客户端目标协议选择渠道已有的最佳上游协议，优先级是原生转发，其次按静态复杂度选择转换路径。

总体方案是可扩展的，但存在几个关键问题：

- **[必须修复] converted + fanout 非流式路径会绕过响应转换，导致客户端收到上游协议格式。**
- **[必须修复] Chat -> Gemini 的工具结果转换使用 `tool_call_id` 作为 `functionResponse.name`，不符合 Gemini 语义。**
- **[建议修改] 非 Chat 上游的流式转换会先完整累积，再一次性重放给客户端，严格意义上不是真流式。**
- **[建议修改] Responses 流式输入解析覆盖不足，工具调用、usage、finish reason 容易丢失。**
- **[建议修改] Claude/Gemini 的 reasoning、图片、多模态、部分工具配置存在明显信息损耗。**
- **[建议修改] 缺少协议转换核心单测，当前测试主要覆盖 handler 错误、usage 捕获和 fanout 基础行为。**

## 2. 入口与接口类型

入口注册在 `cmd/server/main.go`：

| 客户端路径 | Handler | 目标接口类型 |
| --- | --- | --- |
| `/v1/chat/completions` | `HandleChat` | `chat` |
| `/v1/responses` | `HandleResponses` | `responses` |
| `/v1/messages` | `HandleMessages` | `messages` |
| `/v1beta/models/...` | `HandleGenerateContent` | `generate_content` |

代码证据：

- `cmd/server/main.go:117-120` 注册 4 类 handler，其中 Gemini 非流式和流式共用 `/v1beta/models/`。
- `internal/proxy/handler.go:164`、`handler.go:201`、`handler.go:238` 分别解析 Chat、Responses、Claude 请求体。
- `internal/proxy/handler.go:275` 从 Gemini URL 中提取模型名，并用路径中是否包含 `streamGenerateContent` 判断流式。

注意：`generateContent` 和 `streamGenerateContent` 在内部都映射为 `InterfaceGenerateContent`，由 `stream bool` 决定上游路径。

## 3. 渠道能力选择

渠道通过 4 个 URL 字段声明原生能力：

| 字段 | 协议 |
| --- | --- |
| `chat_url` | OpenAI Chat Completions |
| `responses_url` | OpenAI Responses |
| `messages_url` | Anthropic Claude Messages |
| `generate_content_url` | Google Gemini generateContent / streamGenerateContent |

选择逻辑在 `internal/config/capability.go`：

- `BestSourceForTarget` 优先选择与客户端目标协议相同的原生接口。
- 如果没有原生接口，就按 `ConversionComplexity` 从渠道已有接口里选最低成本来源。
- 同分时按 `chat > responses > messages > generate_content` 打破平局。

当前复杂度表：

| Source \ Target | chat | responses | messages | generate_content |
| --- | ---: | ---: | ---: | ---: |
| chat | 0 | 10 | 20 | 30 |
| responses | 10 | 0 | 20 | 30 |
| messages | 20 | 20 | 0 | 30 |
| generate_content | 30 | 30 | 30 | 0 |

这意味着所有协议组合都会被视为可转换，但实际转换质量取决于中心格式能否承载源协议特性。

## 4. 请求转换链路

统一调度入口在 `internal/proxy/handler.go:314`：

1. 根据目标协议和渠道能力调用 `BestSourceForTarget`。
2. 原生路径：`source == target` 时只替换请求体里的 `model`，然后 `nativeForward`。
3. 转换路径：先调用 `buildChatRequest` 把客户端请求转为 `ChatRequest`。
4. 非流式走 `convertedNonStreamForward`，流式走 `convertedStreamForward`。

请求转换函数如下：

| 客户端协议 | 转 Chat | Chat 转上游协议 |
| --- | --- | --- |
| Chat | 直接使用 `ChatRequest` | 直接使用 `ChatRequest` |
| Responses | `ReqToChat` | `ReqToResponses` |
| Claude Messages | `ClaudeToChatRequest` | `ChatToClaudeRequest` |
| Gemini | `GeminiToChatRequest` | `ChatToGeminiRequest` |

代码证据：

- `internal/proxy/handler.go:408` 的 `buildChatRequest` 负责目标协议转 Chat。
- `internal/proxy/convert.go:12` 的 `convertChatToSource` 负责 Chat 转上游协议。
- `internal/translate/req.go:11`、`req.go:120` 实现 Responses <-> Chat 请求转换。
- `internal/translate/claude_convert.go:16`、`claude_convert.go:93` 实现 Claude <-> Chat 请求转换。
- `internal/translate/gemini_convert.go:16`、`gemini_convert.go:92` 实现 Gemini <-> Chat 请求转换。

## 5. 响应转换链路

非流式转换路径的正常链路是：

```text
source response body
  -> convertSourceToChat(source)
  -> convertChatToTarget(target)
  -> client response
```

代码证据：

- `internal/proxy/forward.go:345` 调用 `convertSourceToChat`。
- `internal/proxy/forward.go:350` 调用 `convertChatToTarget`。
- `internal/proxy/forward.go:358-359` 将目标协议响应写回客户端。

响应转换函数如下：

| 上游协议 | 转 Chat | Chat 转客户端协议 |
| --- | --- | --- |
| Chat | JSON 反序列化为 `ChatResponse` | 直接返回 |
| Responses | `RespToChat` | `RespToResponses` |
| Claude Messages | `ClaudeToChatResponse` | `ChatToClaudeResponse` |
| Gemini | `GeminiToChatResponse` | `ChatToGeminiResponse` |

## 6. 流式转换链路

流式分两类：

### 6.1 Chat 上游源

当上游源协议是 Chat 时，系统可以边读边转：

- Chat -> Chat：直接 `io.Copy`。
- Chat -> Responses：`PipeChatStreamToResponses`。
- Chat -> Claude：`PipeChatStreamToClaude`。
- Chat -> Gemini：`PipeChatStreamToGemini`。

代码证据：

- `internal/proxy/stream.go:96` 的 `streamFromChatSource`。
- `internal/proxy/stream.go:204-219` 按目标协议选择流式转换器。

### 6.2 非 Chat 上游源

当上游源协议不是 Chat 时，系统先把上游流完整累积成 `ChatResponse`，再按目标协议重放流式响应：

```text
source stream -> accumulateStreamToChat -> ChatResponse -> emitStreamResponse(target)
```

代码证据：

- `internal/proxy/stream.go:230` 的 `streamChainConversion`。
- `internal/proxy/stream.go:327` 先调用 `accumulateStreamToChat`。
- `internal/proxy/stream.go:351` 再调用 `emitStreamResponse`。

这类路径对客户端仍然返回流式 Content-Type，但首包需要等上游流完全结束后才发出。

## 7. 主要问题

### 7.1 [必须修复] converted + fanout 非流式路径绕过响应转换

问题位置：

- `internal/proxy/forward.go:226` 是转换后的非流式转发入口。
- `internal/proxy/forward.go:240` 如果 fanout 启用，直接调用 `fanoutForward`。
- `internal/proxy/forward.go:24` 的 `fanoutForward` 只把请求发给上游接口，并在成功后把 `result.Response` 原样写回客户端。

影响：

如果客户端请求 `/v1/responses`，渠道只有 `chat_url` 且开启 fanout，系统会把 Responses 请求转换成 Chat 请求发给上游，但上游返回的 Chat 响应会原样返回给 Responses 客户端。客户端期望 `response` 对象，实际收到 `chat.completion`。

类似地，`/v1/messages` 或 Gemini 客户端也可能收到 Chat、Responses 或 Claude 原生响应，取决于被选中的上游 source。

建议修复：

- 最小修复：`convertedNonStreamForward` 中当 `source != target` 时禁用 `fanoutForward` 快路径，走已有的普通转换路径。
- 更完整修复：新增 converted fanout 路径，fanout 拿到上游响应后继续执行 `convertSourceToChat` 和 `convertChatToTarget`，再写回客户端。
- 补充回归测试：覆盖 `target=responses/source=chat/fanout=true`，断言客户端响应包含 `object: "response"` 而不是 `object: "chat.completion"`。

### 7.2 [必须修复] Chat -> Gemini 工具结果使用了错误的 name

问题位置：

- `internal/translate/gemini_convert.go:555` 的 `chatToolToGeminiContent`。
- 该函数把 Chat `tool` message 转成 Gemini `functionResponse`，但设置的是 `Name: msg.ToolCallID`。

问题原因：

Gemini 的 `functionResponse.name` 表示函数名，而 Chat 的 `tool_call_id` 是工具调用 ID。二者语义不同。当前中心格式在 `tool` message 上只保留 `tool_call_id`，没有保留函数名，因此从 Chat 转 Gemini 工具结果时无法正确填充 `functionResponse.name`。

影响：

Gemini 上游在多轮工具调用中可能无法把工具结果关联到正确函数，尤其是需要继续发送 function response 的场景。

建议修复：

- 在 Chat 中心格式构建阶段维护 `tool_call_id -> function name` 映射。
- 或在转换 Gemini 请求时扫描前文 assistant `tool_calls`，用同一 ID 找回函数名。
- 补充测试：构造 assistant `tool_calls` + 后续 `tool` message，断言 Gemini `functionResponse.name` 是函数名，而不是 `call_xxx`。

### 7.3 [建议修改] Responses 流式转 Chat 的事件解析不完整

问题位置：

- `internal/translate/stream.go:421` 的 `PipeResponsesStreamToChat`。
- 函数声明了 `usage`、`finishReason` 和 `seenEvents`，但当前没有从 `response.completed` 或 `response.output_item.done` 中恢复 usage 和完成原因。
- 函数处理 `response.function_call_arguments.delta/done`，但没有处理 `response.output_item.added` 或 `response.output_item.done` 中的函数名和 `call_id`。

影响：

- Responses 流式上游转 Chat 时，usage 通常为空。
- 函数调用可能只有 arguments，没有 name。
- `finish_reason` 基本固定为 `stop`，无法可靠表达 `length` 等结束原因。
- 如果上游只在 `response.completed` 里给完整 output，当前解析会漏内容。

建议修复：

- 解析 `response.output_item.added` / `response.output_item.done`，建立 `item_id -> call_id/name`。
- 解析 `response.completed.response.output` 作为兜底完整快照。
- 从 `response.completed.response.usage` 映射到 Chat usage。
- 增加 Responses SSE fixture 测试，覆盖文本、reasoning、function_call 和 usage。

### 7.4 [建议修改] 非 Chat 源流式路径不是实时转换

问题位置：

- `internal/proxy/stream.go:230` 的 `streamChainConversion`。
- `internal/proxy/stream.go:327` 先完整累积上游流。
- `internal/proxy/stream.go:351` 上游结束后才重放客户端流。

影响：

如果客户端请求流式，但被选中的上游 source 是 Responses、Claude 或 Gemini，客户端不会实时收到增量 token。长输出会表现为长时间无响应，最后一次性收到几个 SSE 事件。

建议修复：

- 为 `Responses -> target`、`Claude -> target`、`Gemini -> target` 增加真正的边读边转路径。
- 至少在文档中明确：只有 source 是 Chat 时支持实时流转换；其他 source 是「流式响应格式重放」。

### 7.5 [建议修改] Claude thinking / reasoning 转换不对称

问题位置：

- Chat -> Claude 会把 `ReasoningContent` 转成 Claude `thinking` block：`internal/translate/claude_convert.go:170`。
- Claude -> Chat 响应转换只处理 `text` 和 `tool_use`，没有处理 `thinking`：`internal/translate/claude_convert.go:207`。
- Claude 请求中的 assistant `thinking` block 在 `claudeAssistantToChatMessage` 中也没有恢复。

影响：

Claude 原生上游返回 thinking 时，转成 Chat / Responses 会丢失 reasoning 内容。反向转换支持，正向回收不支持，导致协议往返不对称。

建议修复：

- 在 `ClaudeToChatResponse` 和 `claudeAssistantToChatMessage` 中把 `thinking` block 映射到 `ReasoningContent`。
- 补充 Claude thinking fixture。

### 7.6 [建议修改] Gemini 多模态和工具配置存在信息损耗

问题位置：

- `GeminiRequest` 类型定义包含 `ToolConfig`、`SafetySettings`、`InlineData`：`internal/translate/gemini_types.go:10-23`。
- `GeminiToChatRequest` 没有保留 `ToolConfig` 和 `SafetySettings`。
- Chat -> Gemini 只从文本和工具调用构造 parts，无法恢复原 Gemini `inlineData`。

影响：

Gemini 原生请求如果经过非 Gemini 上游，会丢失 safety/tool config 和图片等多模态输入。Chat / Responses 图片输入也会在转 Chat 中心格式时被丢弃或只保留文本。

相关证据：

- `internal/translate/req.go:452` 对 `input_image` 的处理是跳过。
- `internal/translate/claude_convert.go:626` 和 `gemini_convert.go` 中的文本提取逻辑主要只拼接 text。

建议修复：

- 明确中心格式是否支持多模态。如果支持，应在 `ChatMessage.Content` 中保留结构化 parts，而不是只提取文本。
- 如果暂不支持，应在文档和错误响应中明确提示，而不是静默丢弃。
- 接入 `ModelConfig.SupportsImages` 做能力校验。

### 7.7 [建议修改] Gemini 转 Chat 响应会丢失 model

问题位置：

- `internal/translate/gemini_convert.go:183` 的 `GeminiToChatResponse`。
- 函数最后设置 `chat.Model = ""`。

影响：

Gemini 上游经转换后返回给 Chat / Responses / Claude 客户端时，模型字段为空。客户端日志、计费归因和部分 SDK 校验可能受影响。

建议修复：

- 让 `GeminiToChatResponse` 接收默认模型名，或在调用后由 `convertSourceToChat` 填充 `chatResp.Model = chatReq.Model`。

### 7.8 [建议修改] 存在两套 Chat -> Responses 请求转换实现

问题位置：

- `internal/translate/chat_convert.go:4` 有 `ChatToResponses`。
- `internal/translate/req.go:120` 有 `ReqToResponses`。
- 生产转发路径使用 `ReqToResponses`，debug handler 使用 `ChatToResponses`。

影响：

两套实现字段覆盖不同，长期会产生调试结果和真实转发结果不一致的问题。

建议修复：

- 保留一套权威实现，debug handler 也复用生产路径。
- 删除或标注废弃旧函数。

## 8. 转换能力矩阵

下表按「能否走通」和「主要损耗」总结：

| 方向 | 当前状态 | 主要损耗 / 风险 |
| --- | --- | --- |
| Chat -> Responses | 可走通 | 多模态、部分 Responses 原生工具语义损耗；debug 路径有旧实现 |
| Responses -> Chat | 可走通 | `input_image` 跳过；MCP/server-side tools 降级或丢弃；工具输出会自动补占位 |
| Chat -> Claude | 可走通 | 图片结构可能不完整；tool choice 的 `none` 被映射为 `auto` |
| Claude -> Chat | 可走通 | thinking block 丢失；多内容块只保留文本和 tool_use |
| Chat -> Gemini | 可走通 | 工具结果 name 错误；图片/inlineData 无法恢复 |
| Gemini -> Chat | 可走通 | `tool_config`、`safety_settings`、inlineData 丢失；响应 model 为空 |
| Responses -> Claude/Gemini | 经 Chat 中转 | 叠加 Responses -> Chat 与 Chat -> 目标损耗 |
| Claude/Gemini -> Responses | 经 Chat 中转 | 叠加源协议 -> Chat 与 Chat -> Responses 损耗 |
| 任意转换 + fanout 非流式 | 有严重问题 | 响应转换被绕过，客户端收到上游原生格式 |
| 非 Chat 源流式转换 | 格式上可返回流 | 首包等上游结束后才发出，不是真实时流 |

## 9. 测试覆盖现状

当前可见测试主要覆盖：

- handler 错误处理和 `/v1/models`：`internal/proxy/handler_test.go`。
- stream usage 捕获：`internal/proxy/sse_test.go`。
- fanout 基础行为：`internal/channel/fanout_test.go`。

没有看到针对以下核心转换的直接单元测试：

- `ReqToChat` / `ReqToResponses` 往返。
- `ClaudeToChatRequest` / `ChatToClaudeRequest` 往返。
- `GeminiToChatRequest` / `ChatToGeminiRequest` 往返。
- 非流式 `source -> Chat -> target` 端到端。
- converted + fanout 响应格式。
- Responses / Claude / Gemini SSE fixture 转换。

建议补充以协议 fixture 为核心的表驱动测试。优先级：

1. converted + fanout 响应格式回归测试。
2. Gemini 工具调用与工具结果转换测试。
3. Responses SSE 到 Chat SSE 的文本、工具、usage 测试。
4. Claude thinking 到 Chat reasoning 测试。
5. 多模态输入显式报错或保留的测试。

## 10. 建议修复顺序

1. 修复 converted + fanout 非流式响应转换绕过问题。
2. 修复 Chat -> Gemini 工具结果 name 映射问题。
3. 为协议转换增加表驱动单测和 SSE fixture。
4. 修复 Responses 流式解析，至少保证 text、function_call、usage 和 finish reason。
5. 明确或实现非 Chat 源的实时流式转换。
6. 统一 Chat -> Responses 请求转换实现。
7. 梳理多模态、reasoning、server-side tools 的支持边界，并在不支持时显式报错。

