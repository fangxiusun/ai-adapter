package translate

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// ==================== Chat <-> Claude Request Conversion ====================

// ChatToClaudeRequest converts an OpenAI Chat request to a Claude request.
func ChatToClaudeRequest(req *ChatRequest) (*ClaudeRequest, error) {
	claude := &ClaudeRequest{
		Model:   req.Model,
		Stream:  req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}
	if req.MaxCompletionTokens != nil {
		claude.MaxTokens = *req.MaxCompletionTokens
	}
	if claude.MaxTokens == 0 {
		claude.MaxTokens = 4096
	}
	if len(req.Stop) > 0 {
		claude.StopSequences = req.Stop
	}

	// Extract system messages into the system field.
	var systemParts []string
	for _, msg := range req.Messages {
		if msg.Role == "system" || msg.Role == "developer" {
			systemParts = append(systemParts, extractTextFromContent(msg.Content))
		}
	}
	if len(systemParts) > 0 {
		claude.System = strings.Join(systemParts, "\n\n")
	}

	// Convert messages, handling tool_calls and tool results.
	var claudeMsgs []ClaudeMessage
	for _, msg := range req.Messages {
		switch msg.Role {
		case "system", "developer":
			continue // already handled
		case "user":
			claudeMsgs = append(claudeMsgs, ClaudeMessage{Role: "user", Content: msg.Content})
		case "assistant":
			blocks := chatAssistantToClaudeBlocks(msg)
			claudeMsgs = append(claudeMsgs, ClaudeMessage{Role: "assistant", Content: blocks})
		case "tool":
			claudeMsgs = appendToolResult(claudeMsgs, msg)
		}
	}

	// Remove empty messages.
	var filtered []ClaudeMessage
	for _, m := range claudeMsgs {
		if isEmptyClaudeMessage(m) {
			continue
		}
		filtered = append(filtered, m)
	}
	claude.Messages = filtered

	// Merge consecutive same-role messages (Claude requires alternating roles).
	claude.Messages = mergeClaudeMessages(claude.Messages)

	// Tools.
	for _, t := range req.Tools {
		if t.Type == "function" && t.Function.Name != "" {
			claude.Tools = append(claude.Tools, ClaudeTool{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				InputSchema: t.Function.Parameters,
			})
		}
	}

	// Tool choice.
	if req.ToolChoice != nil {
		claude.ToolChoice = chatToolChoiceToClaude(req.ToolChoice)
	}

	return claude, nil
}

// ClaudeToChatRequest converts a Claude request to an OpenAI Chat request.
func ClaudeToChatRequest(req *ClaudeRequest) (*ChatRequest, error) {
	chat := &ChatRequest{
		Model:  req.Model,
		Stream: req.Stream,
	}
	chat.Temperature = req.Temperature
	chat.TopP = req.TopP
	if req.MaxTokens > 0 {
		v := req.MaxTokens
		chat.MaxCompletionTokens = &v
	}
	if len(req.StopSequences) > 0 {
		chat.Stop = req.StopSequences
	}

	// System field -> system message.
	if req.System != nil {
		text := extractClaudeSystemText(req.System)
		if text != "" {
			chat.Messages = append(chat.Messages, ChatMessage{Role: "system", Content: text})
		}
	}

	// Convert messages.
	for _, msg := range req.Messages {
		switch msg.Role {
		case "user":
			chatMsgs := claudeUserToChatMessages(msg)
			chat.Messages = append(chat.Messages, chatMsgs...)
		case "assistant":
			chatMsg := claudeAssistantToChatMessage(msg)
			chat.Messages = append(chat.Messages, chatMsg)
		}
	}

	// Tools.
	for _, t := range req.Tools {
		chat.Tools = append(chat.Tools, ChatTool{
			Type: "function",
			Function: ChatToolDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	// Tool choice.
	if req.ToolChoice != nil {
		chat.ToolChoice = claudeToolChoiceToChat(req.ToolChoice)
	}

	return chat, nil
}

// ==================== Chat <-> Claude Response Conversion ====================

// ChatToClaudeResponse converts an OpenAI Chat response to a Claude response.
func ChatToClaudeResponse(resp *ChatResponse) *ClaudeResponse {
	claude := &ClaudeResponse{
		ID:    resp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: resp.Model,
		Usage: ClaudeUsage{},
	}
	if resp.Usage != nil {
		claude.Usage.InputTokens = resp.Usage.PromptTokens
		claude.Usage.OutputTokens = resp.Usage.CompletionTokens
	}
	if len(resp.Choices) == 0 {
		claude.StopReason = "end_turn"
		return claude
	}
	choice := resp.Choices[0]
	claude.StopReason = chatFinishToClaudeStop(choice.FinishReason)

	// Reasoning content -> thinking block (if present).
	if choice.Message.ReasoningContent != nil && *choice.Message.ReasoningContent != "" {
		claude.Content = append(claude.Content, ClaudeContentBlock{
			Type: "thinking",
			Text: *choice.Message.ReasoningContent,
		})
	}

	// Text content.
	if choice.Message.Content != nil && *choice.Message.Content != "" {
		claude.Content = append(claude.Content, ClaudeContentBlock{
			Type: "text",
			Text: *choice.Message.Content,
		})
	}

	// Tool calls -> tool_use blocks.
	for _, tc := range choice.Message.ToolCalls {
		input := make(map[string]interface{})
		if tc.Function.Arguments != "" {
			json.Unmarshal([]byte(tc.Function.Arguments), &input)
		}
		claude.Content = append(claude.Content, ClaudeContentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}

	if len(claude.Content) == 0 {
		claude.Content = []ClaudeContentBlock{{Type: "text", Text: ""}}
	}

	return claude
}

// ClaudeToChatResponse converts a Claude response to an OpenAI Chat response.
func ClaudeToChatResponse(resp *ClaudeResponse) *ChatResponse {
	chat := &ChatResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Usage: &ChatUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}

	msg := ChatChoiceMsg{Role: "assistant"}
	finishReason := claudeStopToChatFinish(resp.StopReason)

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			if msg.Content == nil {
				msg.Content = &block.Text
			} else {
				*msg.Content += block.Text
			}
		case "tool_use":
			inputBytes, _ := json.Marshal(block.Input)
			msg.ToolCalls = append(msg.ToolCalls, ChatToolCall{
				ID:   block.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      block.Name,
					Arguments: string(inputBytes),
				},
			})
		}
	}

	if msg.Content == nil {
		s := ""
		msg.Content = &s
	}

	chat.Choices = []ChatChoice{{
		Index:        0,
		Message:      msg,
		FinishReason: finishReason,
	}}
	return chat
}

// ==================== Streaming Conversion ====================

// PipeChatStreamToClaude converts OpenAI Chat SSE stream to Claude SSE stream.
func PipeChatStreamToClaude(ctx context.Context, upstream io.Reader, sink io.Writer, req *ChatRequest, flusher func()) (*ClaudeResponse, error) {
	w := newSSEWriter(sink, flusher)
	state := &claudeStreamState{
		responseID: generateID(),
		model:      req.Model,
	}

	// Emit message_start.
	startResp := &ClaudeResponse{
		ID:      state.responseID,
		Type:    "message",
		Role:    "assistant",
		Model:   state.model,
		Content: []ClaudeContentBlock{},
		Usage:   ClaudeUsage{},
	}
	w.writeEvent("message_start", map[string]interface{}{"message": startResp})

	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk ChatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		processChatChunkToClaude(w, state, &chunk)
	}

	// Finalize any open block.
	if state.blockOpen {
		w.writeEvent("content_block_stop", map[string]interface{}{"index": state.blockIndex})
		state.blockOpen = false
	}

	// Emit message_delta and message_stop.
	stopReason := "end_turn"
	if state.finishReason != "" {
		stopReason = chatFinishToClaudeStop(state.finishReason)
	}
	w.writeEvent("message_delta", map[string]interface{}{
		"delta": map[string]interface{}{"stop_reason": stopReason},
		"usage": map[string]interface{}{"output_tokens": state.outputTokens},
	})
	w.writeEvent("message_stop", nil)

	return &ClaudeResponse{
		ID:         state.responseID,
		Type:       "message",
		Role:       "assistant",
		Model:      state.model,
		Content:    state.finalContent,
		StopReason: stopReason,
		Usage:      ClaudeUsage{InputTokens: state.inputTokens, OutputTokens: state.outputTokens},
	}, nil
}

type claudeStreamState struct {
	responseID     string
	model          string
	blockIndex     int
	blockOpen      bool
	blockType      string // "text" or "tool_use"
	toolCallID     string
	toolName       string
	finishReason   string
	inputTokens    int
	outputTokens   int
	finalContent   []ClaudeContentBlock
}

func processChatChunkToClaude(w *sseWriter, st *claudeStreamState, chunk *ChatStreamChunk) {
	if chunk.Usage != nil {
		st.inputTokens = chunk.Usage.PromptTokens
		st.outputTokens = chunk.Usage.CompletionTokens
	}
	if len(chunk.Choices) == 0 {
		return
	}
	delta := chunk.Choices[0].Delta

	// Text content.
	if delta.Content != nil && *delta.Content != "" {
		if !st.blockOpen || st.blockType != "text" {
			closeBlock(w, st)
			w.writeEvent("content_block_start", map[string]interface{}{
				"index": st.blockIndex,
				"content_block": map[string]interface{}{"type": "text", "text": ""},
			})
			st.blockOpen = true
			st.blockType = "text"
		}
		w.writeEvent("content_block_delta", map[string]interface{}{
			"index": st.blockIndex,
			"delta": map[string]interface{}{"type": "text_delta", "text": *delta.Content},
		})
	}

	// Tool calls.
	for _, tc := range delta.ToolCalls {
		if tc.ID != "" || (tc.Function != nil && tc.Function.Name != "") {
			// New tool call starting.
			closeBlock(w, st)
			callID := tc.ID
			if callID == "" {
				callID = "toolu_" + generateID()
			}
			name := ""
			if tc.Function != nil {
				name = tc.Function.Name
			}
			st.toolCallID = callID
			st.toolName = name
			w.writeEvent("content_block_start", map[string]interface{}{
				"index": st.blockIndex,
				"content_block": map[string]interface{}{
					"type":  "tool_use",
					"id":    callID,
					"name":  name,
					"input": map[string]interface{}{},
				},
			})
			st.blockOpen = true
			st.blockType = "tool_use"
		}
		if tc.Function != nil && tc.Function.Arguments != "" {
			w.writeEvent("content_block_delta", map[string]interface{}{
				"index": st.blockIndex,
				"delta": map[string]interface{}{
					"type":         "input_json_delta",
					"partial_json": tc.Function.Arguments,
				},
			})
		}
	}

	if chunk.Choices[0].FinishReason != "" {
		st.finishReason = chunk.Choices[0].FinishReason
	}
}

func closeBlock(w *sseWriter, st *claudeStreamState) {
	if st.blockOpen {
		w.writeEvent("content_block_stop", map[string]interface{}{"index": st.blockIndex})
		if st.blockType == "text" {
			st.finalContent = append(st.finalContent, ClaudeContentBlock{Type: "text"})
		} else if st.blockType == "tool_use" {
			st.finalContent = append(st.finalContent, ClaudeContentBlock{
				Type: "tool_use",
				ID:   st.toolCallID,
				Name: st.toolName,
			})
		}
		st.blockIndex++
		st.blockOpen = false
		st.blockType = ""
	}
}

// PipeClaudeStreamToChat converts Claude SSE stream to OpenAI Chat SSE stream.
func PipeClaudeStreamToChat(ctx context.Context, upstream io.Reader, sink io.Writer, req *ChatRequest, flusher func()) (*ChatResponse, error) {
	w := newSSEWriter(sink, flusher)
	state := &chatFromClaudeStreamState{
		toolCalls: make(map[int]*toolCallAccumulator),
	}

	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var event ClaudeStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		processClaudeEventToChat(w, state, &event, req.Model)
	}

	// Emit [DONE].
	fmt.Fprintf(sink, "data: [DONE]\n\n")
	if flusher != nil {
		flusher()
	}

	return state.buildResponse(req.Model), nil
}

type chatFromClaudeStreamState struct {
	responseID    string
	model         string
	content       string
	reasoning     string
	toolCalls     map[int]*toolCallAccumulator
	finishReason  string
	outputTokens  int
	inputTokens   int
}

type toolCallAccumulator struct {
	id       string
	name     string
	argsJSON string
}

func processClaudeEventToChat(w *sseWriter, st *chatFromClaudeStreamState, event *ClaudeStreamEvent, defaultModel string) {
	switch event.Type {
	case "message_start":
		if event.Message != nil {
			st.responseID = event.Message.ID
			st.model = event.Message.Model
		}
	case "content_block_start":
		if event.ContentBlock != nil {
			switch event.ContentBlock.Type {
			case "tool_use":
				st.toolCalls[event.Index] = &toolCallAccumulator{
					id:   event.ContentBlock.ID,
					name: event.ContentBlock.Name,
				}
			}
		}
	case "content_block_delta":
		if event.Delta != nil {
			switch event.Delta.Type {
			case "text_delta":
				st.content += event.Delta.Text
				emitChatChunk(w, st.responseID, st.model, &event.Delta.Text, nil, "", nil)
			case "input_json_delta":
				if acc, ok := st.toolCalls[event.Index]; ok {
					acc.argsJSON += event.Delta.PartialJSON
					idx := event.Index
					emitChatToolChunk(w, st.responseID, st.model, idx, acc.id, "", acc.name, event.Delta.PartialJSON)
				}
			}
		}
	case "message_delta":
		if event.Delta != nil && event.Delta.Type == "" {
			// message_delta has stop_reason at top level
		}
		if event.Usage != nil {
			st.outputTokens = event.Usage.OutputTokens
		}
	case "message_stop":
		st.finishReason = "stop"
	case "error":
		st.finishReason = "stop"
	}
}

func emitChatChunk(w *sseWriter, id, model string, content *string, reasoningContent *string, toolCallID string, toolCalls []ToolCallDelta) {
	chunk := ChatStreamChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []ChatStreamChoice{{
			Index: 0,
			Delta: ChatStreamDelta{
				Content:          content,
				ReasoningContent: reasoningContent,
				ToolCalls:        toolCalls,
			},
		}},
	}
	data, _ := json.Marshal(chunk)
	fmt.Fprintf(w.sink, "data: %s\n\n", string(data))
	w.flush()
}

func emitChatToolChunk(w *sseWriter, id, model string, index int, callID, typeName, name, args string) {
	delta := ToolCallDelta{
		Index: index,
	}
	if callID != "" {
		delta.ID = callID
	}
	if typeName != "" {
		delta.Type = typeName
	}
	if name != "" || args != "" {
		fn := &FunctionDelta{Name: name, Arguments: args}
		delta.Function = fn
	}
	emitChatChunk(w, id, model, nil, nil, "", []ToolCallDelta{delta})
}

func (st *chatFromClaudeStreamState) buildResponse(model string) *ChatResponse {
	msg := ChatChoiceMsg{Role: "assistant"}
	if st.content != "" {
		msg.Content = &st.content
	} else {
		s := ""
		msg.Content = &s
	}
	if st.reasoning != "" {
		msg.ReasoningContent = &st.reasoning
	}
	for _, acc := range st.toolCalls {
		msg.ToolCalls = append(msg.ToolCalls, ChatToolCall{
			ID:   acc.id,
			Type: "function",
			Function: FunctionCall{
				Name:      acc.name,
				Arguments: acc.argsJSON,
			},
		})
	}
	finish := st.finishReason
	if finish == "" {
		finish = "stop"
	}
	return &ChatResponse{
		ID:      st.responseID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []ChatChoice{{Index: 0, Message: msg, FinishReason: finish}},
		Usage:   &ChatUsage{PromptTokens: st.inputTokens, CompletionTokens: st.outputTokens, TotalTokens: st.inputTokens + st.outputTokens},
	}
}

// ==================== SSE Writer Helper ====================

type sseWriter struct {
	sink    io.Writer
	flusher func()
}

func newSSEWriter(sink io.Writer, flusher func()) *sseWriter {
	return &sseWriter{sink: sink, flusher: flusher}
}

func (w *sseWriter) writeEvent(event string, data interface{}) {
	if data != nil {
		payload, _ := json.Marshal(data)
		fmt.Fprintf(w.sink, "event: %s\ndata: %s\n\n", event, string(payload))
	} else {
		fmt.Fprintf(w.sink, "event: %s\ndata: {}\n\n", event)
	}
	w.flush()
}

func (w *sseWriter) flush() {
	if w.flusher != nil {
		w.flusher()
	}
}

// ==================== Helper Functions ====================

func extractTextFromContent(content interface{}) string {
	if content == nil {
		return ""
	}
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, p := range v {
			if m, ok := p.(map[string]interface{}); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}

func chatAssistantToClaudeBlocks(msg ChatMessage) []ClaudeContentBlock {
	var blocks []ClaudeContentBlock

	if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
		blocks = append(blocks, ClaudeContentBlock{Type: "thinking", Text: *msg.ReasoningContent})
	}

	if msg.Content != nil {
		text := extractTextFromContent(msg.Content)
		if text != "" {
			blocks = append(blocks, ClaudeContentBlock{Type: "text", Text: text})
		}
	}

	for _, tc := range msg.ToolCalls {
		input := make(map[string]interface{})
		if tc.Function.Arguments != "" {
			json.Unmarshal([]byte(tc.Function.Arguments), &input)
		}
		blocks = append(blocks, ClaudeContentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}

	if len(blocks) == 0 {
		blocks = append(blocks, ClaudeContentBlock{Type: "text", Text: ""})
	}
	return blocks
}

func appendToolResult(msgs []ClaudeMessage, msg ChatMessage) []ClaudeMessage {
	isErr := false
	resultBlock := ClaudeContentBlock{
		Type:      "tool_result",
		ToolUseID: msg.ToolCallID,
		Content:   extractTextFromContent(msg.Content),
		IsError:   &isErr,
	}
	// Try to append to previous user message.
	if len(msgs) > 0 && msgs[len(msgs)-1].Role == "user" {
		last := &msgs[len(msgs)-1]
		var blocks []ClaudeContentBlock
		switch v := last.Content.(type) {
		case string:
			if v != "" {
				blocks = append(blocks, ClaudeContentBlock{Type: "text", Text: v})
			}
		case []ClaudeContentBlock:
			blocks = v
		}
		blocks = append(blocks, resultBlock)
		last.Content = blocks
		return msgs
	}
	return append(msgs, ClaudeMessage{Role: "user", Content: []ClaudeContentBlock{resultBlock}})
}

func isEmptyClaudeMessage(m ClaudeMessage) bool {
	switch v := m.Content.(type) {
	case string:
		return v == ""
	case []ClaudeContentBlock:
		return len(v) == 0
	case nil:
		return true
	}
	return false
}

func mergeClaudeMessages(msgs []ClaudeMessage) []ClaudeMessage {
	if len(msgs) == 0 {
		return msgs
	}
	merged := []ClaudeMessage{msgs[0]}
	for _, msg := range msgs[1:] {
		last := &merged[len(merged)-1]
		if last.Role == msg.Role {
			// Merge content blocks.
			lastBlocks := claudeContentToBlocks(last.Content)
			newBlocks := claudeContentToBlocks(msg.Content)
			last.Content = append(lastBlocks, newBlocks...)
		} else {
			merged = append(merged, msg)
		}
	}
	return merged
}

func claudeContentToBlocks(content interface{}) []ClaudeContentBlock {
	switch v := content.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []ClaudeContentBlock{{Type: "text", Text: v}}
	case []ClaudeContentBlock:
		return v
	}
	return nil
}

func extractClaudeSystemText(system interface{}) string {
	switch v := system.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func claudeUserToChatMessages(msg ClaudeMessage) []ChatMessage {
	var result []ChatMessage
	blocks := claudeContentToBlocks(msg.Content)
	if len(blocks) == 0 {
		return result
	}

	var userParts []string
	for _, block := range blocks {
		switch block.Type {
		case "text":
			userParts = append(userParts, block.Text)
		case "tool_result":
			output := ""
			switch v := block.Content.(type) {
			case string:
				output = v
			default:
				data, _ := json.Marshal(v)
				output = string(data)
			}
			result = append(result, ChatMessage{
				Role:       "tool",
				ToolCallID: block.ToolUseID,
				Content:    output,
			})
		}
	}
	if len(userParts) > 0 {
		result = append(result, ChatMessage{Role: "user", Content: strings.Join(userParts, "\n")})
	}
	return result
}

func claudeAssistantToChatMessage(msg ClaudeMessage) ChatMessage {
	chat := ChatMessage{Role: "assistant"}
	blocks := claudeContentToBlocks(msg.Content)

	var textParts []string
	for _, block := range blocks {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			inputBytes, _ := json.Marshal(block.Input)
			chat.ToolCalls = append(chat.ToolCalls, ChatToolCall{
				ID:   block.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      block.Name,
					Arguments: string(inputBytes),
				},
			})
		}
	}

	text := strings.Join(textParts, "")
	if text != "" {
		chat.Content = text
	} else {
		chat.Content = ""
	}
	return chat
}

func chatToolChoiceToClaude(tc interface{}) interface{} {
	switch v := tc.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]string{"type": "auto"}
		case "required", "any":
			return map[string]string{"type": "any"}
		case "none":
			return map[string]string{"type": "auto"}
		}
	case map[string]interface{}:
		if v["type"] == "function" {
			name := ""
			if fn, ok := v["function"].(map[string]interface{}); ok {
				name, _ = fn["name"].(string)
			}
			if name != "" {
				return map[string]string{"type": "tool", "name": name}
			}
		}
	}
	return map[string]string{"type": "auto"}
}

func claudeToolChoiceToChat(tc interface{}) interface{} {
	switch v := tc.(type) {
	case map[string]interface{}:
		t, _ := v["type"].(string)
		switch t {
		case "auto":
			return "auto"
		case "any":
			return "required"
		case "tool":
			name, _ := v["name"].(string)
			if name != "" {
				return map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{"name": name},
				}
			}
			return "auto"
		}
	}
	return "auto"
}

func chatFinishToClaudeStop(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "end_turn"
	}
	return "end_turn"
}

func claudeStopToChatFinish(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	}
	return "stop"
}