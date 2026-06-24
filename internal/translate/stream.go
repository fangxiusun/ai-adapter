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
	state          *streamState
	sink           io.Writer
	req            *ResponsesRequest
	opts           TranslateOpts
	flusher        func()
}

type streamState struct {
	responseID     string
	createdAt      int64
	model          string
	outputIndex    int
	seqNum         int
	activeKind     string
	activeItemID   string
	activeBuffer   string
	toolCalls      map[int]*toolCallState
	finalOutput    []OutputItem
	finishReason   string
	usage          *ResponsesUsage
}

type toolCallState struct {
	itemID       string
	outputIndex  int
	callID       string
	name         string
	argsBuffer   string
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
			"item_id":      st.state.activeItemID,
			"output_index": st.state.outputIndex - 1,
			"summary_index": 0,
			"delta":        *delta.ReasoningContent,
		})
	}

	if delta.Content != nil && *delta.Content != "" {
		if st.state.activeKind != "message" {
			st.openMessage()
		}
		st.state.activeBuffer += *delta.Content
		st.emit("response.output_text.delta", map[string]interface{}{
			"item_id":      st.state.activeItemID,
			"output_index": st.state.outputIndex - 1,
			"content_index": 0,
			"delta":        *delta.Content,
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
			"id":          st.state.activeItemID,
			"type":        "reasoning",
			"summary":     []interface{}{},
			"encrypted_content": nil,
			"status":      "in_progress",
		},
	})
	st.emit("response.reasoning_summary_part.added", map[string]interface{}{
		"item_id":      st.state.activeItemID,
		"output_index": idx,
		"summary_index": 0,
		"part":         map[string]interface{}{"type": "summary_text", "text": ""},
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
			"id":       st.state.activeItemID,
			"type":     "message",
			"role":     "assistant",
			"status":   "in_progress",
			"content":  []interface{}{},
		},
	})
	st.emit("response.content_part.added", map[string]interface{}{
		"item_id":      st.state.activeItemID,
		"output_index": idx,
		"content_index": 0,
		"part":         map[string]interface{}{"type": "output_text", "text": "", "annotations": []interface{}{}},
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
			"id":          itemID,
			"type":        "function_call",
			"call_id":     callID,
			"name":        name,
			"arguments":   "",
			"status":      "in_progress",
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
			"item_id":      itemID,
			"output_index": outputIndex,
			"summary_index": 0,
			"text":         buffer,
		})
		st.emit("response.reasoning_summary_part.done", map[string]interface{}{
			"item_id":      itemID,
			"output_index": outputIndex,
			"summary_index": 0,
			"part":         map[string]interface{}{"type": "summary_text", "text": buffer},
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
			"item_id":      itemID,
			"output_index": outputIndex,
			"content_index": 0,
			"text":         buffer,
		})
		st.emit("response.content_part.done", map[string]interface{}{
			"item_id":      itemID,
			"output_index": outputIndex,
			"content_index": 0,
			"part":         map[string]interface{}{"type": "output_text", "text": buffer, "annotations": []interface{}{}},
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
	data["type"] = event
	data["sequence_number"] = st.state.seqNum
	st.state.seqNum++

	payload, _ := json.Marshal(data)
	fmt.Fprintf(st.sink, "event: %s\ndata: %s\n\n", event, string(payload))
	if st.flusher != nil {
		st.flusher()
	}
}

func PipeChatStreamToResponses(ctx context.Context, upstream io.Reader, sink io.Writer, req *ResponsesRequest, opts TranslateOpts) (*StreamResult, error) {
 translator := NewStreamTranslator(sink, req, opts, nil)
 translator.Start()

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
 translator.ProcessChunk(&chunk)
 }

 result := translator.Finish()
 return result, nil
}

func PipeResponsesStreamToChat(ctx context.Context, upstream io.Reader, sink io.Writer, req *ChatRequest, opts TranslateOpts) (*ChatResponse, error) {
	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var content string
	var reasoningContent string
	var toolCalls []ChatToolCall
	var usage *ChatUsage
	var finishReason string

	seenEvents := make(map[string]bool)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			continue
		}

		eventType, _ := raw["type"].(string)
		seenEvents[eventType] = true

		switch eventType {
		case "response.output_text.delta":
			if delta, ok := raw["delta"].(string); ok {
				content += delta
			}
		case "response.reasoning_summary_text.delta":
			if delta, ok := raw["delta"].(string); ok {
				reasoningContent += delta
			}
		case "response.function_call_arguments.delta":
			itemID, _ := raw["item_id"].(string)
			delta, _ := raw["delta"].(string)
			found := false
			for i := range toolCalls {
				if toolCalls[i].ID == itemID {
					toolCalls[i].Function.Arguments += delta
					found = true
					break
				}
			}
			if !found {
				toolCalls = append(toolCalls, ChatToolCall{
					ID:   itemID,
					Type: "function",
					Function: FunctionCall{
						Arguments: delta,
					},
				})
			}
		case "response.function_call_arguments.done":
			itemID, _ := raw["item_id"].(string)
			args, _ := raw["arguments"].(string)
			for i := range toolCalls {
				if toolCalls[i].ID == itemID {
					toolCalls[i].Function.Arguments = SalvageToolCallArguments(args)
					break
				}
			}
		case "response.function_call":
			itemID, _ := raw["item_id"].(string)
			callID, _ := raw["call_id"].(string)
			name, _ := raw["name"].(string)
			toolCalls = append(toolCalls, ChatToolCall{
				ID:   callID,
				Type: "function",
				Function: FunctionCall{
					Name: name,
				},
			})
			_ = itemID
		}
	}

	msg := ChatChoiceMsg{Role: "assistant"}
	if reasoningContent != "" {
		msg.ReasoningContent = &reasoningContent
	}
	if content != "" {
		msg.Content = &content
	} else if len(toolCalls) == 0 {
		s := ""
		msg.Content = &s
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}

	finish := "stop"
	if finishReason == "length" {
		finish = "length"
	}

	return &ChatResponse{
		ID:      generateResponseID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []ChatChoice{
			{
				Index:        0,
				Message:      msg,
				FinishReason: finish,
			},
		},
		Usage: usage,
	}, nil
}
