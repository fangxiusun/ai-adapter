package translate

import (
	"time"
)

func RespToResponses(resp *ChatResponse, req *ResponsesRequest, opts TranslateOpts) *ResponsesObject {
	output := []OutputItem{}

	if len(resp.Choices) == 0 {
		return &ResponsesObject{
			ID:        generateResponseID(),
			Object:    "response",
			CreatedAt: time.Now().Unix(),
			Status:    "completed",
			Model:     resp.Model,
			Output:    output,
			Usage:     mapUsage(resp.Usage),
		}
	}

	choice := resp.Choices[0]
	msg := choice.Message

	if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
		ec := *msg.ReasoningContent
		summary := []ReasoningSummaryPart{}
		if opts.ExtractInlineThink {
			summary = append(summary, ReasoningSummaryPart{Type: "summary_text", Text: *msg.ReasoningContent})
		}
		output = append(output, OutputItem{
			Type:             "reasoning",
			ID:               generateReasoningID(),
			Summary:          summary,
			EncryptedContent: &ec,
			Status:           "completed",
		})
	}

	if msg.Content != nil && *msg.Content != "" {
		output = append(output, OutputItem{
			Type:   "message",
			ID:     generateMessageID(),
			Role:   "assistant",
			Status: "completed",
			Content: []OutputContentPart{
				{Type: "output_text", Text: *msg.Content},
			},
		})
	}

	for _, tc := range msg.ToolCalls {
		args := SalvageToolCallArguments(tc.Function.Arguments)
		item := OutputItem{
			Type:      "function_call",
			ID:        generateFunctionCallID(),
			CallID:    tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
			Status:    "completed",
		}
		output = append(output, item)
	}

	status := "completed"
	var incomplete *IncompleteDetails
	if choice.FinishReason == "length" {
		status = "incomplete"
		incomplete = &IncompleteDetails{Reason: "max_output_tokens"}
	}

	var reasoningResult *ReasoningResult
	if req.Reasoning != nil {
		reasoningResult = &ReasoningResult{
			Effort:  req.Reasoning.Effort,
			Summary: req.Reasoning.Summary,
		}
	} else {
		reasoningResult = &ReasoningResult{}
	}

	return &ResponsesObject{
		ID:                generateResponseID(),
		Object:            "response",
		CreatedAt:         resp.Created,
		Status:            status,
		Model:             resp.Model,
		Output:            output,
		Usage:             mapUsage(resp.Usage),
		ParallelToolCalls: getBool(req.ParallelToolCalls, true),
		ToolChoice:        req.ToolChoice,
		Reasoning:         reasoningResult,
		Text:              getTextFormat(req.Text),
		IncompleteDetails: incomplete,
		Error:             nil,
		Metadata:          req.Metadata,
	}
}

func RespToChat(resp *ResponsesObject, req *ChatRequest, opts TranslateOpts) *ChatResponse {
	msg := ChatChoiceMsg{Role: "assistant"}

	if len(resp.Output) > 0 {
		for _, item := range resp.Output {
			switch item.Type {
			case "reasoning":
				if item.EncryptedContent != nil {
					msg.ReasoningContent = item.EncryptedContent
				}
			case "message":
				if len(item.Content) > 0 {
					for _, c := range item.Content {
						if c.Type == "output_text" && c.Text != "" {
							s := c.Text
							msg.Content = &s
							break
						}
					}
				}
			case "function_call":
				msg.ToolCalls = append(msg.ToolCalls, ChatToolCall{
					ID:   item.CallID,
					Type: "function",
					Function: FunctionCall{
						Name:      item.Name,
						Arguments: item.Arguments,
					},
				})
			}
		}
	}

	if msg.Content == nil && len(msg.ToolCalls) == 0 {
		s := ""
		msg.Content = &s
	}

	finishReason := "stop"
	if resp.IncompleteDetails != nil {
		finishReason = "length"
	}

	choice := ChatChoice{
		Index:        0,
		Message:      msg,
		FinishReason: finishReason,
	}

	return &ChatResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: resp.CreatedAt,
		Model:   resp.Model,
		Choices: []ChatChoice{choice},
		Usage:   unmapUsage(resp.Usage),
	}
}

func mapUsage(u *ChatUsage) *ResponsesUsage {
	if u == nil {
		return nil
	}
	out := &ResponsesUsage{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		TotalTokens:  u.TotalTokens,
	}
	if u.PromptTokensDetails != nil {
		out.InputTokensDetails = &TokenDetails{CachedTokens: u.PromptTokensDetails.CachedTokens}
	}
	if u.CompletionTokensDetails != nil {
		out.OutputTokensDetails = &TokenDetails{ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens}
	}
	return out
}

func unmapUsage(u *ResponsesUsage) *ChatUsage {
	if u == nil {
		return nil
	}
	out := &ChatUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
	}
	if u.InputTokensDetails != nil {
		out.PromptTokensDetails = &TokenDetails{CachedTokens: u.InputTokensDetails.CachedTokens}
	}
	if u.OutputTokensDetails != nil {
		out.CompletionTokensDetails = &TokenDetails{ReasoningTokens: u.OutputTokensDetails.ReasoningTokens}
	}
	return out
}

func getBool(p *bool, def bool) bool {
	if p != nil {
		return *p
	}
	return def
}

func getTextFormat(t *TextFormat) *TextFormat {
	if t != nil {
		return t
	}
	return &TextFormat{Type: "text"}
}

func generateResponseID() string  { return "resp_" + generateID() }
func generateMessageID() string   { return "msg_" + generateID() }
func generateFunctionCallID() string { return "fc_" + generateID() }
func generateReasoningID() string { return "rs_" + generateID() }

func generateID() string {
	b := make([]byte, 18)
	time.Now().AppendFormat(b, "0102030405060708000")
	return base62Encode(b)
}

func base62Encode(data []byte) string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, 24)
	for i := range result {
		result[i] = charset[int(data[i%len(data)])%len(charset)]
	}
	return string(result)
}
