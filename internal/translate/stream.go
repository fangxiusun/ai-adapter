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

type StreamTranslator struct {
	state   *streamState
	sink    io.Writer
	req     *ResponsesRequest
	opts    TranslateOpts
	flusher func()
	err     error
}

type streamState struct {
	responseID   string
	createdAt    int64
	model        string
	outputIndex  int
	seqNum       int
	activeKind   string
	activeItemID string
	activeBuffer string
	toolCalls    map[int]*toolCallState
	finalOutput  []OutputItem
	finishReason string
	usage        *ResponsesUsage
}

type toolCallState struct {
	itemID      string
	outputIndex int
	callID      string
	name        string
	argsBuffer  string
}

func NewStreamTranslator(sink io.Writer, req *ResponsesRequest, opts TranslateOpts, flusher func()) *StreamTranslator {
	return &StreamTranslator{
		sink:    sink,
		req:     req,
		opts:    opts,
		flusher: flusher,
		state: &streamState{
			responseID: generateResponseID(),
			createdAt:  time.Now().Unix(),
			model:      req.Model,
			toolCalls:  make(map[int]*toolCallState),
		},
	}
}

func (st *StreamTranslator) Start() {
	resp := st.buildSnapshot("in_progress")
	st.emit("response.created", map[string]interface{}{"response": resp})
	st.emit("response.in_progress", map[string]interface{}{"response": resp})
}

func (st *StreamTranslator) ProcessChunk(chunk *ChatStreamChunk) {
	if chunk.Usage != nil {
		st.state.usage = mapUsage(chunk.Usage)
	}

	if len(chunk.Choices) == 0 {
		return
	}
	delta := chunk.Choices[0].Delta

	if delta.ReasoningContent != nil && *delta.ReasoningContent != "" {
		if st.state.activeKind != "reasoning" {
			st.openReasoning()
		}
		st.state.activeBuffer += *delta.ReasoningContent
		st.emit("response.reasoning_summary_text.delta", map[string]interface{}{
			"item_id":       st.state.activeItemID,
			"output_index":  st.state.outputIndex - 1,
			"summary_index": 0,
			"delta":         *delta.ReasoningContent,
		})
	}

	if delta.Content != nil && *delta.Content != "" {
		if st.state.activeKind != "message" {
			st.openMessage()
		}
		st.state.activeBuffer += *delta.Content
		st.emit("response.output_text.delta", map[string]interface{}{
			"item_id":       st.state.activeItemID,
			"output_index":  st.state.outputIndex - 1,
			"content_index": 0,
			"delta":         *delta.Content,
		})
	}

	for _, tcDelta := range delta.ToolCalls {
		tc, exists := st.state.toolCalls[tcDelta.Index]
		if !exists {
			tc = st.openToolCall(tcDelta.Index, tcDelta.ID, "")
			exists = true
		}
		if tcDelta.Function != nil {
			if tcDelta.Function.Name != "" && tc.name == "" {
				tc.name = tcDelta.Function.Name
			}
			if tcDelta.Function.Arguments != "" {
				tc.argsBuffer += tcDelta.Function.Arguments
				st.emit("response.function_call_arguments.delta", map[string]interface{}{
					"item_id":      tc.itemID,
					"output_index": tc.outputIndex,
					"delta":        tcDelta.Function.Arguments,
				})
			}
		}
	}

	if chunk.Choices[0].FinishReason != "" {
		st.state.finishReason = chunk.Choices[0].FinishReason
	}
}

func (st *StreamTranslator) Finish() *StreamResult {
	st.finalizeActive()
	st.finalizeToolCalls()

	completed := st.buildSnapshot("completed")
	st.emit("response.completed", map[string]interface{}{"response": completed})

	return &StreamResult{
		Usage:         st.state.usage,
		Response:      completed,
		ToolCallCount: len(st.state.toolCalls),
	}
}

func (st *StreamTranslator) FinishWithError(err error) *StreamResult {
	st.finalizeActive()
	st.finalizeToolCalls()

	failed := st.buildSnapshot("failed")
	failed.Error = &ErrorInfo{Type: "upstream_error", Message: err.Error()}
	st.emit("response.failed", map[string]interface{}{"response": failed})

	return &StreamResult{
		Usage:         st.state.usage,
		Response:      failed,
		ToolCallCount: len(st.state.toolCalls),
	}
}

func (st *StreamTranslator) openReasoning() {
	st.finalizeActive()
	st.state.activeKind = "reasoning"
	st.state.activeItemID = generateReasoningID()
	st.state.activeBuffer = ""
	idx := st.state.outputIndex
	st.state.outputIndex++

	st.emit("response.output_item.added", map[string]interface{}{
		"output_index": idx,
		"item": map[string]interface{}{
			"id":                st.state.activeItemID,
			"type":              "reasoning",
			"summary":           []interface{}{},
			"encrypted_content": nil,
			"status":            "in_progress",
		},
	})
	st.emit("response.reasoning_summary_part.added", map[string]interface{}{
		"item_id":       st.state.activeItemID,
		"output_index":  idx,
		"summary_index": 0,
		"part":          map[string]interface{}{"type": "summary_text", "text": ""},
	})
}

func (st *StreamTranslator) openMessage() {
	st.finalizeActive()
	st.state.activeKind = "message"
	st.state.activeItemID = generateMessageID()
	st.state.activeBuffer = ""
	idx := st.state.outputIndex
	st.state.outputIndex++

	st.emit("response.output_item.added", map[string]interface{}{
		"output_index": idx,
		"item": map[string]interface{}{
			"id":      st.state.activeItemID,
			"type":    "message",
			"role":    "assistant",
			"status":  "in_progress",
			"content": []interface{}{},
		},
	})
	st.emit("response.content_part.added", map[string]interface{}{
		"item_id":       st.state.activeItemID,
		"output_index":  idx,
		"content_index": 0,
		"part":          map[string]interface{}{"type": "output_text", "text": "", "annotations": []interface{}{}},
	})
}

func (st *StreamTranslator) openToolCall(index int, id, name string) *toolCallState {
	st.finalizeActive()
	itemID := generateFunctionCallID()
	outputIndex := st.state.outputIndex
	st.state.outputIndex++
	callID := id
	if callID == "" {
		callID = "call_" + itemID[3:]
	}
	tc := &toolCallState{
		itemID:      itemID,
		outputIndex: outputIndex,
		callID:      callID,
		name:        name,
	}
	st.state.toolCalls[index] = tc

	st.emit("response.output_item.added", map[string]interface{}{
		"output_index": outputIndex,
		"item": map[string]interface{}{
			"id":        itemID,
			"type":      "function_call",
			"call_id":   callID,
			"name":      name,
			"arguments": "",
			"status":    "in_progress",
		},
	})
	return tc
}

func (st *StreamTranslator) finalizeActive() {
	if st.state.activeKind == "" {
		return
	}
	itemID := st.state.activeItemID
	buffer := st.state.activeBuffer
	outputIndex := st.state.outputIndex - 1

	if st.state.activeKind == "reasoning" {
		st.emit("response.reasoning_summary_text.done", map[string]interface{}{
			"item_id":       itemID,
			"output_index":  outputIndex,
			"summary_index": 0,
			"text":          buffer,
		})
		st.emit("response.reasoning_summary_part.done", map[string]interface{}{
			"item_id":       itemID,
			"output_index":  outputIndex,
			"summary_index": 0,
			"part":          map[string]interface{}{"type": "summary_text", "text": buffer},
		})
		finalItem := OutputItem{
			ID:               itemID,
			Type:             "reasoning",
			Summary:          []ReasoningSummaryPart{{Type: "summary_text", Text: buffer}},
			EncryptedContent: &buffer,
			Status:           "completed",
		}
		st.state.finalOutput = append(st.state.finalOutput, finalItem)
		st.emit("response.output_item.done", map[string]interface{}{
			"output_index": outputIndex,
			"item":         finalItem,
		})
	} else if st.state.activeKind == "message" {
		st.emit("response.output_text.done", map[string]interface{}{
			"item_id":       itemID,
			"output_index":  outputIndex,
			"content_index": 0,
			"text":          buffer,
		})
		st.emit("response.content_part.done", map[string]interface{}{
			"item_id":       itemID,
			"output_index":  outputIndex,
			"content_index": 0,
			"part":          map[string]interface{}{"type": "output_text", "text": buffer, "annotations": []interface{}{}},
		})
		finalItem := OutputItem{
			ID:     itemID,
			Type:   "message",
			Role:   "assistant",
			Status: "completed",
			Content: []OutputContentPart{
				{Type: "output_text", Text: buffer},
			},
		}
		st.state.finalOutput = append(st.state.finalOutput, finalItem)
		st.emit("response.output_item.done", map[string]interface{}{
			"output_index": outputIndex,
			"item":         finalItem,
		})
	}

	st.state.activeKind = ""
	st.state.activeItemID = ""
	st.state.activeBuffer = ""
}

func (st *StreamTranslator) finalizeToolCalls() {
	type indexed struct {
		index int
		tc    *toolCallState
	}
	var ordered []indexed
	for idx, tc := range st.state.toolCalls {
		ordered = append(ordered, indexed{idx, tc})
	}
	for i := 0; i < len(ordered); i++ {
		for j := i + 1; j < len(ordered); j++ {
			if ordered[j].index < ordered[i].index {
				ordered[i], ordered[j] = ordered[j], ordered[i]
			}
		}
	}

	for _, o := range ordered {
		tc := o.tc
		safeArgs := SalvageToolCallArguments(tc.argsBuffer)
		st.emit("response.function_call_arguments.done", map[string]interface{}{
			"item_id":      tc.itemID,
			"output_index": tc.outputIndex,
			"arguments":    safeArgs,
		})
		finalItem := OutputItem{
			ID:        tc.itemID,
			Type:      "function_call",
			CallID:    tc.callID,
			Name:      tc.name,
			Arguments: safeArgs,
			Status:    "completed",
		}
		st.state.finalOutput = append(st.state.finalOutput, finalItem)
		st.emit("response.output_item.done", map[string]interface{}{
			"output_index": tc.outputIndex,
			"item":         finalItem,
		})
	}
}

func (st *StreamTranslator) buildSnapshot(status string) *ResponsesObject {
	incompleteDetails := (*IncompleteDetails)(nil)
	if st.state.finishReason == "length" {
		incompleteDetails = &IncompleteDetails{Reason: "max_output_tokens"}
	}
	var reasoningResult *ReasoningResult
	if st.req.Reasoning != nil {
		reasoningResult = &ReasoningResult{
			Effort:  st.req.Reasoning.Effort,
			Summary: st.req.Reasoning.Summary,
		}
	} else {
		reasoningResult = &ReasoningResult{}
	}

	return &ResponsesObject{
		ID:                st.state.responseID,
		Object:            "response",
		CreatedAt:         st.state.createdAt,
		Status:            status,
		Model:             st.state.model,
		Output:            st.state.finalOutput,
		Usage:             st.state.usage,
		ParallelToolCalls: getBool(st.req.ParallelToolCalls, true),
		ToolChoice:        st.req.ToolChoice,
		Reasoning:         reasoningResult,
		Text:              getTextFormat(st.req.Text),
		IncompleteDetails: incompleteDetails,
		Error:             nil,
		Metadata:          st.req.Metadata,
	}
}

func (st *StreamTranslator) emit(event string, data map[string]interface{}) {
	if st.err != nil {
		return
	}
	data["type"] = event
	data["sequence_number"] = st.state.seqNum
	st.state.seqNum++
	payload, err := json.Marshal(data)
	if err != nil {
		st.err = err
		return
	}
	_, st.err = fmt.Fprintf(st.sink, "event: %s\ndata: %s\n\n", event, payload)
	if st.err == nil && st.flusher != nil {
		st.flusher()
	}
}

func (st *StreamTranslator) Err() error {
	return st.err
}

func PipeChatStreamToResponses(ctx context.Context, upstream io.Reader, sink io.Writer, req *ResponsesRequest, opts TranslateOpts) (*StreamResult, error) {
	if req == nil {
		req = &ResponsesRequest{}
	}
	translator := NewStreamTranslator(sink, req, opts, nil)
	translator.Start()
	if err := translator.Err(); err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	terminal := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return translator.FinishWithError(err), err
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			terminal = true
			break
		}

		var chunk ChatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			parseErr := fmt.Errorf("parse chat SSE chunk: %w", err)
			return translator.FinishWithError(parseErr), parseErr
		}
		translator.ProcessChunk(&chunk)
		if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != "" {
			terminal = true
		}
		if err := translator.Err(); err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return translator.FinishWithError(err), err
	}
	if !terminal {
		err := fmt.Errorf("chat SSE ended before a terminal event")
		return translator.FinishWithError(err), err
	}
	result := translator.Finish()
	return result, translator.Err()
}

func PipeResponsesStreamToChat(ctx context.Context, upstream io.Reader, sink io.Writer, req *ChatRequest, opts TranslateOpts) (*ChatResponse, error) {
	if req == nil {
		req = &ChatRequest{}
	}
	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	state := newResponsesStreamToChatState(req.Model)
	w := newSSEWriter(sink, nil)
	terminal := false
	emittedContent := false
	emittedReasoning := false
	emittedToolCalls := make(map[string]bool)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") || !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			terminal = true
			break
		}

		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			return nil, fmt.Errorf("parse Responses SSE event: %w", err)
		}
		eventType, _ := raw["type"].(string)
		if eventType == "response.failed" || eventType == "error" {
			return nil, fmt.Errorf("Responses stream returned %s", eventType)
		}
		state.process(raw)
		if state.id == "" {
			state.id = "chatcmpl-" + generateID()
		}
		switch eventType {
		case "response.output_text.delta":
			delta, _ := raw["delta"].(string)
			emitChatChunk(w, state.id, state.model, &delta, nil, "", nil)
			emittedContent = emittedContent || delta != ""
		case "response.reasoning_summary_text.delta":
			delta, _ := raw["delta"].(string)
			emitChatChunk(w, state.id, state.model, nil, &delta, "", nil)
			emittedReasoning = emittedReasoning || delta != ""
		case "response.output_item.added":
			if item, ok := raw["item"].(map[string]interface{}); ok {
				if itemType, _ := item["type"].(string); itemType == "function_call" {
					index := intFromRaw(raw["output_index"])
					callID, _ := item["call_id"].(string)
					name, _ := item["name"].(string)
					emitChatToolChunk(w, state.id, state.model, index, callID, "function", name, "")
					emittedToolCalls[callID] = true
				}
			}
		case "response.function_call_arguments.delta":
			item := state.itemForEvent(raw)
			if item != nil {
				delta, _ := raw["delta"].(string)
				emitChatToolChunk(w, state.id, state.model, intFromRaw(raw["output_index"]), item.callID, "", item.name, delta)
				emittedToolCalls[item.callID] = true
			}
		case "response.completed":
			terminal = true
			completed := state.build()
			message := completed.Choices[0].Message
			if !emittedReasoning && message.ReasoningContent != nil && *message.ReasoningContent != "" {
				emitChatChunk(w, completed.ID, completed.Model, nil, message.ReasoningContent, "", nil)
			}
			if !emittedContent && message.Content != nil && *message.Content != "" {
				emitChatChunk(w, completed.ID, completed.Model, message.Content, nil, "", nil)
			}
			for index, toolCall := range message.ToolCalls {
				if emittedToolCalls[toolCall.ID] {
					continue
				}
				emitChatToolChunk(w, completed.ID, completed.Model, index, toolCall.ID, "function", toolCall.Function.Name, toolCall.Function.Arguments)
			}
		}
		if err := w.Err(); err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !terminal {
		return nil, fmt.Errorf("Responses stream ended before response.completed")
	}
	resp := state.build()
	finishChunk := ChatStreamChunk{
		ID: resp.ID, Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: resp.Model,
		Choices: []ChatStreamChoice{{Index: 0, Delta: ChatStreamDelta{}, FinishReason: resp.Choices[0].FinishReason}},
		Usage:   resp.Usage,
	}
	w.writeData(finishChunk)
	w.writeDone()
	if err := w.Err(); err != nil {
		return nil, err
	}
	return resp, nil
}

func intFromRaw(value interface{}) int {
	switch n := value.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

type responsesStreamToChatState struct {
	id               string
	created          int64
	model            string
	content          string
	reasoningContent string
	usage            *ChatUsage
	finishReason     string
	items            map[string]*responsesStreamItem
	orderedIDs       []string
}

type responsesStreamItem struct {
	id        string
	itemType  string
	callID    string
	name      string
	arguments string
	content   string
	reasoning string
}

func newResponsesStreamToChatState(model string) *responsesStreamToChatState {
	return &responsesStreamToChatState{
		model:      model,
		items:      make(map[string]*responsesStreamItem),
		orderedIDs: make([]string, 0),
	}
}

func (st *responsesStreamToChatState) process(raw map[string]interface{}) {
	eventType, _ := raw["type"].(string)
	switch eventType {
	case "response.output_item.added":
		st.upsertItem(raw["item"])
	case "response.output_item.done":
		st.upsertItem(raw["item"])
	case "response.output_text.delta":
		item := st.itemForEvent(raw)
		delta, _ := raw["delta"].(string)
		if item != nil {
			item.content += delta
		}
		st.content += delta
	case "response.output_text.done":
		item := st.itemForEvent(raw)
		text, _ := raw["text"].(string)
		if item != nil && text != "" {
			item.content = text
			st.content = st.collectMessageContent()
		}
	case "response.reasoning_summary_text.delta":
		item := st.itemForEvent(raw)
		delta, _ := raw["delta"].(string)
		if item != nil {
			item.reasoning += delta
		}
		st.reasoningContent += delta
	case "response.reasoning_summary_text.done":
		item := st.itemForEvent(raw)
		text, _ := raw["text"].(string)
		if item != nil && text != "" {
			item.reasoning = text
			st.reasoningContent = st.collectReasoningContent()
		}
	case "response.function_call_arguments.delta":
		item := st.itemForEvent(raw)
		delta, _ := raw["delta"].(string)
		if item != nil {
			item.itemType = "function_call"
			item.arguments += delta
		}
	case "response.function_call_arguments.done":
		item := st.itemForEvent(raw)
		args, _ := raw["arguments"].(string)
		if item != nil {
			item.itemType = "function_call"
			item.arguments = SalvageToolCallArguments(args)
		}
	case "response.function_call":
		item := st.itemForEvent(raw)
		if item != nil {
			item.itemType = "function_call"
			item.callID, _ = raw["call_id"].(string)
			item.name, _ = raw["name"].(string)
		}
	case "response.completed":
		st.applyCompleted(raw["response"])
	}
}

func (st *responsesStreamToChatState) itemForEvent(raw map[string]interface{}) *responsesStreamItem {
	itemID, _ := raw["item_id"].(string)
	if itemID == "" {
		return nil
	}
	item, ok := st.items[itemID]
	if !ok {
		item = &responsesStreamItem{id: itemID}
		st.items[itemID] = item
		st.orderedIDs = append(st.orderedIDs, itemID)
	}
	return item
}

func (st *responsesStreamToChatState) upsertItem(raw interface{}) *responsesStreamItem {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	itemID, _ := m["id"].(string)
	if itemID == "" {
		return nil
	}
	item, ok := st.items[itemID]
	if !ok {
		item = &responsesStreamItem{id: itemID}
		st.items[itemID] = item
		st.orderedIDs = append(st.orderedIDs, itemID)
	}
	item.applyMap(m)
	st.refreshFromItems()
	return item
}

func (item *responsesStreamItem) applyMap(m map[string]interface{}) {
	if t, ok := m["type"].(string); ok && t != "" {
		item.itemType = t
	}
	if callID, ok := m["call_id"].(string); ok && callID != "" {
		item.callID = callID
	}
	if name, ok := m["name"].(string); ok && name != "" {
		item.name = name
	}
	if args, ok := m["arguments"].(string); ok && args != "" {
		item.arguments = SalvageToolCallArguments(args)
	}
	if content, ok := m["content"].([]interface{}); ok {
		item.content = responsesContentPartsText(content)
	}
	if encrypted, ok := m["encrypted_content"].(string); ok && encrypted != "" {
		item.reasoning = encrypted
	} else if summary, ok := m["summary"].([]interface{}); ok {
		item.reasoning = responsesSummaryText(summary)
	}
}

func (st *responsesStreamToChatState) applyCompleted(raw interface{}) {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return
	}
	if id, ok := m["id"].(string); ok && id != "" {
		st.id = id
	}
	if created, ok := numberToInt64(m["created_at"]); ok {
		st.created = created
	}
	if model, ok := m["model"].(string); ok && model != "" {
		st.model = model
	}
	if usage, ok := responsesUsageFromRaw(m["usage"]); ok {
		st.usage = usage
	}
	if incomplete, ok := m["incomplete_details"].(map[string]interface{}); ok {
		if reason, _ := incomplete["reason"].(string); reason == "max_output_tokens" {
			st.finishReason = "length"
		}
	}
	if output, ok := m["output"].([]interface{}); ok && len(output) > 0 {
		for _, rawItem := range output {
			st.upsertItem(rawItem)
		}
		st.refreshFromItems()
	}
}

func (st *responsesStreamToChatState) refreshFromItems() {
	if content := st.collectMessageContent(); content != "" {
		st.content = content
	}
	if reasoning := st.collectReasoningContent(); reasoning != "" {
		st.reasoningContent = reasoning
	}
}

func (st *responsesStreamToChatState) collectMessageContent() string {
	var parts []string
	for _, id := range st.orderedIDs {
		item := st.items[id]
		if item != nil && item.itemType == "message" && item.content != "" {
			parts = append(parts, item.content)
		}
	}
	return strings.Join(parts, "")
}

func (st *responsesStreamToChatState) collectReasoningContent() string {
	var parts []string
	for _, id := range st.orderedIDs {
		item := st.items[id]
		if item != nil && item.itemType == "reasoning" && item.reasoning != "" {
			parts = append(parts, item.reasoning)
		}
	}
	return strings.Join(parts, "")
}

func (st *responsesStreamToChatState) build() *ChatResponse {
	msg := ChatChoiceMsg{Role: "assistant"}
	if st.reasoningContent != "" {
		msg.ReasoningContent = &st.reasoningContent
	}
	if st.content != "" {
		msg.Content = &st.content
	}

	for _, id := range st.orderedIDs {
		item := st.items[id]
		if item == nil || item.itemType != "function_call" {
			continue
		}
		callID := item.callID
		if callID == "" {
			callID = item.id
		}
		msg.ToolCalls = append(msg.ToolCalls, ChatToolCall{
			ID:   callID,
			Type: "function",
			Function: FunctionCall{
				Name:      item.name,
				Arguments: SalvageToolCallArguments(item.arguments),
			},
		})
	}

	if msg.Content == nil && len(msg.ToolCalls) == 0 {
		s := ""
		msg.Content = &s
	}

	finish := st.finishReason
	if finish == "" {
		finish = "stop"
	}
	id := st.id
	if id == "" {
		id = generateResponseID()
	}
	created := st.created
	if created == 0 {
		created = time.Now().Unix()
	}

	return &ChatResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: created,
		Model:   st.model,
		Choices: []ChatChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: finish,
		}},
		Usage: st.usage,
	}
}

func responsesContentPartsText(raw []interface{}) string {
	var parts []string
	for _, p := range raw {
		m, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t != "output_text" {
			continue
		}
		if text, ok := m["text"].(string); ok {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "")
}

func responsesSummaryText(raw []interface{}) string {
	var parts []string
	for _, p := range raw {
		m, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if text, ok := m["text"].(string); ok {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "")
}

func responsesUsageFromRaw(raw interface{}) (*ChatUsage, bool) {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil, false
	}
	input, _ := numberToInt(m["input_tokens"])
	output, _ := numberToInt(m["output_tokens"])
	total, _ := numberToInt(m["total_tokens"])
	return &ChatUsage{PromptTokens: input, CompletionTokens: output, TotalTokens: total}, true
}

func numberToInt(raw interface{}) (int, bool) {
	switch v := raw.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	default:
		return 0, false
	}
}

func numberToInt64(raw interface{}) (int64, bool) {
	switch v := raw.(type) {
	case float64:
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	default:
		return 0, false
	}
}

// truncateString truncates a string to maxLength and adds "..." if truncated.
func truncateString(s string, maxLength int) string {
	if len(s) <= maxLength {
		return s
	}
	return s[:maxLength] + "..."
}
