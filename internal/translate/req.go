package translate

import (
	"encoding/json"
	"fmt"
	"strings"
)

const mixedModeReasoningPlaceholder = "(this turn ran without thinking mode)"

func ReqToChat(req *ResponsesRequest, opts TranslateOpts) (*ChatRequest, error) {
	messages := []ChatMessage{}

	if req.Instructions != "" {
		messages = append(messages, ChatMessage{Role: "system", Content: req.Instructions})
	}

	switch v := req.Input.(type) {
	case string:
		messages = append(messages, ChatMessage{Role: "user", Content: v})
	case []interface{}:
		items, err := parseInputItems(v)
		if err != nil {
			return nil, fmt.Errorf("parse input items: %w", err)
		}
		msgs := inputItemsToMessages(items, opts)
		messages = append(messages, msgs...)
	}

	chat := &ChatRequest{
		Model:    req.Model,
		Messages: messages,
		Stream:   req.Stream,
	}
	if chat.Stream {
		chat.StreamOptions = &StreamOptions{IncludeUsage: true}
	}

	if len(req.Tools) > 0 {
		var mapped []ChatTool
		var droppedCount int
		for _, t := range req.Tools {
			converted := ToolToChat(t, opts)
			if len(converted) == 0 {
				droppedCount++
			} else {
				mapped = append(mapped, converted...)
			}
		}
		if len(mapped) > 0 {
			chat.Tools = DedupeChatTools(mapped)
		}

		connLabels := CollectFirstPartyConnectorLabels(req.Tools)
		if len(connLabels) > 0 {
			note := BuildConnectorAdvisoryNote(connLabels)
			insertAt := 0
			if req.Instructions != "" {
				insertAt = 1
			}
			newMsgs := make([]ChatMessage, 0, len(messages)+1)
			newMsgs = append(newMsgs, messages[:insertAt]...)
			newMsgs = append(newMsgs, ChatMessage{Role: "system", Content: note})
			newMsgs = append(newMsgs, messages[insertAt:]...)
			messages = newMsgs
			chat.Messages = messages
		}
	}

	tc := ToolChoiceToChat(req.ToolChoice)
	if tc != nil {
		chat.ToolChoice = tc
	}

	if opts.ForceParallelTools {
		v := true
		chat.ParallelToolCalls = &v
	} else if req.ParallelToolCalls != nil {
		chat.ParallelToolCalls = req.ParallelToolCalls
	}

	if req.Temperature != nil {
		chat.Temperature = req.Temperature
	}
	if req.TopP != nil {
		chat.TopP = req.TopP
	}
	if req.MaxOutputTokens != nil {
		chat.MaxCompletionTokens = req.MaxOutputTokens
	}

	if req.Reasoning != nil && req.Reasoning.Effort != nil {
		eff := *req.Reasoning.Effort
		if eff == "minimal" {
			chat.ReasoningEffort = "low"
		} else {
			chat.ReasoningEffort = eff
		}
	} else if opts.ForceHighEffort && !opts.DisableThinking {
		chat.ReasoningEffort = "high"
	}

	if !opts.DisableThinking {
		for i := range chat.Messages {
			m := &chat.Messages[i]
			if m.Role == "assistant" && m.ReasoningContent == nil {
				ph := mixedModeReasoningPlaceholder
				m.ReasoningContent = &ph
			}
		}
	}

	if opts.DisableThinking {
		chat.Thinking = &ThinkingConfig{Type: "disabled"}
	}

	return chat, nil
}

func ReqToResponses(req *ChatRequest, opts TranslateOpts) (*ResponsesRequest, error) {
	resp := &ResponsesRequest{
		Model:             req.Model,
		Stream:            req.Stream,
		Temperature:       req.Temperature,
		TopP:              req.TopP,
		ParallelToolCalls: req.ParallelToolCalls,
	}

	var inputItems []ResponsesInputItem
	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			if s, ok := msg.Content.(string); ok {
				resp.Instructions = s
			}
		case "user":
			item := ResponsesInputItem{
				Type: "message",
				Role: "user",
			}
			if s, ok := msg.Content.(string); ok {
				item.Content = []ResponsesContentPart{
					{Type: "input_text", Text: s},
				}
			} else {
				item.Content = msg.Content
			}
			inputItems = append(inputItems, item)
		case "assistant":
			if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
				ec := *msg.ReasoningContent
				inputItems = append(inputItems, ResponsesInputItem{
					Type:             "reasoning",
					Summary:          []ReasoningSummaryPart{{Type: "summary_text", Text: *msg.ReasoningContent}},
					EncryptedContent: &ec,
					Status:           "completed",
				})
			}
			if msg.Content != nil {
				item := ResponsesInputItem{
					Type: "message",
					Role: "assistant",
				}
				if s, ok := msg.Content.(string); ok {
					item.Content = []ResponsesContentPart{
						{Type: "output_text", Text: s},
					}
				}
				inputItems = append(inputItems, item)
			}
			for _, tc := range msg.ToolCalls {
				inputItems = append(inputItems, ResponsesInputItem{
					Type:      "function_call",
					CallID:    tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
					Status:    "completed",
				})
			}
		case "tool":
			inputItems = append(inputItems, ResponsesInputItem{
				Type:      "function_call_output",
				CallID:    msg.ToolCallID,
				Output:    msg.Content,
				Status:    "completed",
			})
		}
	}
	resp.Input = inputItems

	if len(req.Tools) > 0 {
		var tools []ResponsesTool
		for _, t := range req.Tools {
			tools = append(tools, ToolToResponses(t))
		}
		resp.Tools = tools
	}

	if req.ReasoningEffort != "" {
		eff := req.ReasoningEffort
		resp.Reasoning = &ReasoningConfig{Effort: &eff}
	}

	return resp, nil
}

func parseInputItems(raw []interface{}) ([]ResponsesInputItem, error) {
	var items []ResponsesInputItem
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	return items, nil
}

type assemblyState struct {
	pendingReasoning     *string
	pendingToolCalls     []ChatToolCall
	pendingAssistantText *string
}

func flushAssistant(messages *[]ChatMessage, state *assemblyState) {
	hasReasoning := state.pendingReasoning != nil
	hasTools := len(state.pendingToolCalls) > 0
	hasText := state.pendingAssistantText != nil
	if !hasReasoning && !hasTools && !hasText {
		return
	}

	msg := ChatMessage{Role: "assistant"}
	if hasText {
		msg.Content = state.pendingAssistantText
	} else if !hasTools {
		s := ""
		msg.Content = &s
	}
	if hasTools {
		msg.ToolCalls = state.pendingToolCalls
	}
	if hasReasoning {
		msg.ReasoningContent = state.pendingReasoning
	}
	*messages = append(*messages, msg)

	state.pendingReasoning = nil
	state.pendingToolCalls = nil
	state.pendingAssistantText = nil
}

func inputItemsToMessages(items []ResponsesInputItem, opts TranslateOpts) []ChatMessage {
	var out []ChatMessage
	state := &assemblyState{}

	for _, item := range items {
		switch item.Type {
		case "message":
			if item.Role == "assistant" {
				if state.pendingAssistantText != nil {
					flushAssistant(&out, state)
				}
				text := extractTextContent(item.Content)
				state.pendingAssistantText = &text
			} else {
				flushAssistant(&out, state)
				out = append(out, messageItemToChat(item, opts))
			}
		case "reasoning":
			text := ""
			if item.EncryptedContent != nil && *item.EncryptedContent != "" {
				text = *item.EncryptedContent
			} else if len(item.Summary) > 0 {
				for _, s := range item.Summary {
					if s.Type == "summary_text" {
						text += s.Text
					}
				}
			}
			if len(state.pendingToolCalls) > 0 || state.pendingAssistantText != nil {
				if state.pendingReasoning != nil {
					combined := *state.pendingReasoning + text
					state.pendingReasoning = &combined
				} else {
					state.pendingReasoning = &text
				}
			} else {
				flushAssistant(&out, state)
				state.pendingReasoning = &text
			}
		case "function_call":
			state.pendingToolCalls = append(state.pendingToolCalls, ChatToolCall{
				ID:   item.CallID,
				Type: "function",
				Function: FunctionCall{
					Name:      item.Name,
					Arguments: SanitizeFunctionCallArguments(item.Arguments),
				},
			})
		case "function_call_output":
			flushAssistant(&out, state)
			output := ""
			switch v := item.Output.(type) {
			case string:
				output = v
			default:
				data, _ := json.Marshal(v)
				output = string(data)
			}
			out = append(out, ChatMessage{
				Role:       "tool",
				ToolCallID: item.CallID,
				Content:    output,
			})
		}
	}
	flushAssistant(&out, &assemblyState{
		pendingReasoning:     state.pendingReasoning,
		pendingToolCalls:     state.pendingToolCalls,
		pendingAssistantText: state.pendingAssistantText,
	})

	removeOrphanToolMessages(&out)
	ensureToolCallsHaveOutputs(&out)

	return out
}

func extractTextContent(content interface{}) string {
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

func messageItemToChat(item ResponsesInputItem, opts TranslateOpts) ChatMessage {
	role := item.Role
	if role == "developer" {
		role = "system"
	}
	content := extractContentParts(item.Content, opts)
	msg := ChatMessage{Role: role, Content: content}
	if role == "assistant" {
		if s, ok := content.(string); ok {
			msg.Content = s
		} else {
			msg.Content = ""
		}
	}
	return msg
}

func extractContentParts(content interface{}, opts TranslateOpts) interface{} {
	if content == nil {
		return ""
	}
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var textParts []string
		for _, p := range v {
			pm, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			t, _ := pm["type"].(string)
			switch t {
			case "input_text", "output_text":
				if txt, ok := pm["text"].(string); ok && txt != "" {
					textParts = append(textParts, txt)
				}
			case "input_image":
				// skip images for non-vision models
			}
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "")
		}
		return ""
	}
	return ""
}

func removeOrphanToolMessages(messages *[]ChatMessage) {
	var validIDs map[string]bool
	i := 0
	for i < len(*messages) {
		m := (*messages)[i]
		switch m.Role {
		case "assistant":
			if len(m.ToolCalls) > 0 {
				validIDs = make(map[string]bool)
				for _, tc := range m.ToolCalls {
					validIDs[tc.ID] = true
				}
			} else {
				validIDs = nil
			}
			i++
		case "tool":
			if validIDs != nil && m.ToolCallID != "" && validIDs[m.ToolCallID] {
				i++
			} else {
				*messages = append((*messages)[:i], (*messages)[i+1:]...)
			}
		default:
			validIDs = nil
			i++
		}
	}
}

func ensureToolCallsHaveOutputs(messages *[]ChatMessage) {
	for i := 0; i < len(*messages); i++ {
		m := (*messages)[i]
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		seen := make(map[string]bool)
		j := i + 1
		for j < len(*messages) && (*messages)[j].Role == "tool" {
			if (*messages)[j].ToolCallID != "" {
				seen[(*messages)[j].ToolCallID] = true
			}
			j++
		}
		var missing []string
		for _, tc := range m.ToolCalls {
			if !seen[tc.ID] {
				missing = append(missing, tc.ID)
			}
		}
		if len(missing) == 0 {
			continue
		}
		placeholders := make([]ChatMessage, len(missing))
		for k, id := range missing {
			placeholders[k] = ChatMessage{
				Role:       "tool",
				ToolCallID: id,
				Content:    "[tool output missing — no function_call_output was provided for this call_id]",
			}
		}
		newMsgs := make([]ChatMessage, 0, len(*messages)+len(placeholders))
		newMsgs = append(newMsgs, (*messages)[:j]...)
		newMsgs = append(newMsgs, placeholders...)
		newMsgs = append(newMsgs, (*messages)[j:]...)
		*messages = newMsgs
		i = j + len(placeholders) - 1
	}
}
