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

// ==================== Chat <-> Gemini Request Conversion ====================

// ChatToGeminiRequest converts an OpenAI Chat request to a Gemini request.
func ChatToGeminiRequest(req *ChatRequest) (*GeminiRequest, error) {
	gemini := &GeminiRequest{}

	// Generation config.
	gc := &GeminiGenConfig{}
	if req.Temperature != nil {
		gc.Temperature = req.Temperature
	}
	if req.TopP != nil {
		gc.TopP = req.TopP
	}
	if req.MaxCompletionTokens != nil {
		gc.MaxOutputTokens = req.MaxCompletionTokens
	}
	if len(req.Stop) > 0 {
		gc.StopSequences = req.Stop
	}
	gemini.GenerationConfig = gc

	// System messages -> system_instruction.
	var systemParts []string
	for _, msg := range req.Messages {
		if msg.Role == "system" || msg.Role == "developer" {
			systemParts = append(systemParts, extractTextFromContent(msg.Content))
		}
	}
	if len(systemParts) > 0 {
		text := strings.Join(systemParts, "\n\n")
		gemini.SystemInstruction = &GeminiContent{
			Parts: []GeminiPart{{Text: text}},
		}
	}

	// Convert messages.
	var contents []GeminiContent
	for _, msg := range req.Messages {
		switch msg.Role {
		case "system", "developer":
			continue
		case "user":
			contents = append(contents, GeminiContent{
				Role:  "user",
				Parts: []GeminiPart{{Text: extractTextFromContent(msg.Content)}},
			})
		case "assistant":
			content := chatAssistantToGeminiContent(msg)
			contents = append(contents, content)
		case "tool":
			contents = append(contents, chatToolToGeminiContent(msg))
		}
	}

	// Merge consecutive same-role messages.
	gemini.Contents = mergeGeminiContents(contents)

	// Tools.
	if len(req.Tools) > 0 {
		var decls []GeminiFunctionDecl
		for _, t := range req.Tools {
			if t.Type == "function" && t.Function.Name != "" {
				decls = append(decls, GeminiFunctionDecl{
					Name:        t.Function.Name,
					Description: t.Function.Description,
					Parameters:  t.Function.Parameters,
				})
			}
		}
		if len(decls) > 0 {
			gemini.Tools = []GeminiTool{{FunctionDeclarations: decls}}
		}
	}

	return gemini, nil
}

// GeminiToChatRequest converts a Gemini request to an OpenAI Chat request.
func GeminiToChatRequest(req *GeminiRequest) (*ChatRequest, error) {
	chat := &ChatRequest{}

	// System instruction.
	if req.SystemInstruction != nil {
		text := geminiContentToText(req.SystemInstruction)
		if text != "" {
			chat.Messages = append(chat.Messages, ChatMessage{Role: "system", Content: text})
		}
	}

	// Generation config.
	if req.GenerationConfig != nil {
		gc := req.GenerationConfig
		chat.Temperature = gc.Temperature
		chat.TopP = gc.TopP
		chat.MaxCompletionTokens = gc.MaxOutputTokens
		chat.Stop = gc.StopSequences
	}

	// Convert contents.
	for _, content := range req.Contents {
		msgs := geminiContentToChatMessages(content)
		chat.Messages = append(chat.Messages, msgs...)
	}

	// Tools.
	for _, tool := range req.Tools {
		for _, decl := range tool.FunctionDeclarations {
			chat.Tools = append(chat.Tools, ChatTool{
				Type: "function",
				Function: ChatToolDef{
					Name:        decl.Name,
					Description: decl.Description,
					Parameters:  decl.Parameters,
				},
			})
		}
	}

	return chat, nil
}

// ==================== Chat <-> Gemini Response Conversion ====================

// ChatToGeminiResponse converts an OpenAI Chat response to a Gemini response.
func ChatToGeminiResponse(resp *ChatResponse) *GeminiResponse {
	gemini := &GeminiResponse{}
	if resp.Usage != nil {
		gemini.UsageMetadata = &GeminiUsage{
			PromptTokenCount:     resp.Usage.PromptTokens,
			CandidatesTokenCount: resp.Usage.CompletionTokens,
			TotalTokenCount:      resp.Usage.TotalTokens,
		}
	}
	if len(resp.Choices) == 0 {
		return gemini
	}
	choice := resp.Choices[0]
	content := GeminiContent{Role: "model"}

	if choice.Message.ReasoningContent != nil && *choice.Message.ReasoningContent != "" {
		content.Parts = append(content.Parts, GeminiPart{Text: *choice.Message.ReasoningContent, Thought: true})
	}
	if choice.Message.Content != nil && *choice.Message.Content != "" {
		content.Parts = append(content.Parts, GeminiPart{Text: *choice.Message.Content})
	}
	for _, tc := range choice.Message.ToolCalls {
		args := make(map[string]interface{})
		if tc.Function.Arguments != "" {
			json.Unmarshal([]byte(tc.Function.Arguments), &args)
		}
		content.Parts = append(content.Parts, GeminiPart{
			FunctionCall: &GeminiFunctionCall{
				Name: tc.Function.Name,
				Args: args,
			},
		})
	}
	if len(content.Parts) == 0 {
		content.Parts = []GeminiPart{{Text: ""}}
	}

	gemini.Candidates = []GeminiCandidate{{
		Content:      content,
		FinishReason: chatFinishToGeminiStop(choice.FinishReason),
	}}
	return gemini
}

// GeminiToChatResponse converts a Gemini response to an OpenAI Chat response.
func GeminiToChatResponse(resp *GeminiResponse) *ChatResponse {
	chat := &ChatResponse{
		ID:      generateResponseID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
	}
	if resp.UsageMetadata != nil {
		chat.Usage = &ChatUsage{
			PromptTokens:     resp.UsageMetadata.PromptTokenCount,
			CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      resp.UsageMetadata.TotalTokenCount,
		}
	}
	if len(resp.Candidates) == 0 {
		chat.Choices = []ChatChoice{{
			Index:        0,
			Message:      ChatChoiceMsg{Role: "assistant", Content: strPtr("")},
			FinishReason: "stop",
		}}
		return chat
	}
	cand := resp.Candidates[0]
	msg := ChatChoiceMsg{Role: "assistant"}

	for _, part := range cand.Content.Parts {
		if part.FunctionCall != nil {
			argsBytes, _ := json.Marshal(part.FunctionCall.Args)
			msg.ToolCalls = append(msg.ToolCalls, ChatToolCall{
				ID:   "call_" + generateID(),
				Type: "function",
				Function: FunctionCall{
					Name:      part.FunctionCall.Name,
					Arguments: string(argsBytes),
				},
			})
		} else if part.Text != "" && !part.Thought {
			if msg.Content == nil {
				msg.Content = &part.Text
			} else {
				*msg.Content += part.Text
			}
		}
	}
	if msg.Content == nil {
		s := ""
		msg.Content = &s
	}

	chat.Choices = []ChatChoice{{
		Index:        0,
		Message:      msg,
		FinishReason: geminiStopToChatFinish(cand.FinishReason),
	}}
	chat.Model = ""
	return chat
}

// ==================== Streaming Conversion ====================

// PipeChatStreamToGemini converts OpenAI Chat SSE stream to Gemini JSON stream.
func PipeChatStreamToGemini(ctx context.Context, upstream io.Reader, sink io.Writer, req *ChatRequest, flusher func()) (*GeminiResponse, error) {
	state := &geminiStreamState{
		model: req.Model,
	}

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
		processChatChunkToGemini(sink, state, &chunk, flusher)
	}

	return state.buildResponse(), nil
}

type geminiStreamState struct {
	model          string
	textBuffer     string
	toolCalls      []geminiToolCallState
	finishReason   string
	inputTokens    int
	outputTokens   int
}

type geminiToolCallState struct {
	name     string
	argsJSON string
}

func processChatChunkToGemini(sink io.Writer, st *geminiStreamState, chunk *ChatStreamChunk, flusher func()) {
	if chunk.Usage != nil {
		st.inputTokens = chunk.Usage.PromptTokens
		st.outputTokens = chunk.Usage.CompletionTokens
	}
	if len(chunk.Choices) == 0 {
		return
	}
	delta := chunk.Choices[0].Delta

	if delta.Content != nil && *delta.Content != "" {
		st.textBuffer += *delta.Content
		geminiResp := &GeminiResponse{
			Candidates: []GeminiCandidate{{
				Content: GeminiContent{
					Role:  "model",
					Parts: []GeminiPart{{Text: *delta.Content}},
				},
			}},
		}
		data, _ := json.Marshal(geminiResp)
		fmt.Fprintf(sink, "%s\n", string(data))
		if flusher != nil {
			flusher()
		}
	}

	for _, tc := range delta.ToolCalls {
		if tc.Function != nil && tc.Function.Name != "" {
			st.toolCalls = append(st.toolCalls, geminiToolCallState{
				name:     tc.Function.Name,
				argsJSON: tc.Function.Arguments,
			})
		} else if tc.Function != nil && tc.Function.Arguments != "" {
			if len(st.toolCalls) > 0 {
				st.toolCalls[len(st.toolCalls)-1].argsJSON += tc.Function.Arguments
			}
		}
	}

	if chunk.Choices[0].FinishReason != "" {
		st.finishReason = chunk.Choices[0].FinishReason
	}
}

func (st *geminiStreamState) buildResponse() *GeminiResponse {
	content := GeminiContent{Role: "model"}
	if st.textBuffer != "" {
		content.Parts = append(content.Parts, GeminiPart{Text: st.textBuffer})
	}
	for _, tc := range st.toolCalls {
		args := make(map[string]interface{})
		if tc.argsJSON != "" {
			json.Unmarshal([]byte(tc.argsJSON), &args)
		}
		content.Parts = append(content.Parts, GeminiPart{
			FunctionCall: &GeminiFunctionCall{Name: tc.name, Args: args},
		})
	}
	if len(content.Parts) == 0 {
		content.Parts = []GeminiPart{{Text: ""}}
	}

	return &GeminiResponse{
		Candidates: []GeminiCandidate{{
			Content:      content,
			FinishReason: chatFinishToGeminiStop(st.finishReason),
		}},
		UsageMetadata: &GeminiUsage{
			PromptTokenCount:     st.inputTokens,
			CandidatesTokenCount: st.outputTokens,
			TotalTokenCount:      st.inputTokens + st.outputTokens,
		},
	}
}

// PipeGeminiStreamToChat converts Gemini JSON stream to OpenAI Chat SSE stream.
func PipeGeminiStreamToChat(ctx context.Context, upstream io.Reader, sink io.Writer, req *ChatRequest, flusher func()) (*ChatResponse, error) {
	w := newSSEWriter(sink, flusher)
	state := &chatFromGeminiStreamState{}

	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var chunk GeminiStreamChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue
		}
		processGeminiChunkToChat(w, state, &chunk, req.Model)
	}

	fmt.Fprintf(sink, "data: [DONE]\n\n")
	if flusher != nil {
		flusher()
	}

	return state.buildResponse(req.Model), nil
}

type chatFromGeminiStreamState struct {
	responseID   string
	content      string
	reasoning    string
	toolCalls    []chatFromGeminiToolCall
	finishReason string
	inputTokens  int
	outputTokens int
}

type chatFromGeminiToolCall struct {
	id       string
	name     string
	argsJSON string
}

func processGeminiChunkToChat(w *sseWriter, st *chatFromGeminiStreamState, chunk *GeminiStreamChunk, model string) {
	if st.responseID == "" {
		st.responseID = "chatcmpl-" + generateID()
	}
	if chunk.UsageMetadata != nil {
		st.inputTokens = chunk.UsageMetadata.PromptTokenCount
		st.outputTokens = chunk.UsageMetadata.CandidatesTokenCount
	}
	if len(chunk.Candidates) == 0 {
		return
	}
	cand := chunk.Candidates[0]
	if cand.FinishReason != "" {
		st.finishReason = cand.FinishReason
	}

	for _, part := range cand.Content.Parts {
		if part.FunctionCall != nil {
			callID := "call_" + generateID()
			st.toolCalls = append(st.toolCalls, chatFromGeminiToolCall{
				id:   callID,
				name: part.FunctionCall.Name,
			})
			argsBytes, _ := json.Marshal(part.FunctionCall.Args)
			chunk := ChatStreamChunk{
				ID:      st.responseID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   model,
				Choices: []ChatStreamChoice{{
					Index: 0,
					Delta: ChatStreamDelta{
						ToolCalls: []ToolCallDelta{{
							Index: len(st.toolCalls) - 1,
							ID:    callID,
							Type:  "function",
							Function: &FunctionDelta{
								Name:      part.FunctionCall.Name,
								Arguments: string(argsBytes),
							},
						}},
					},
				}},
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w.sink, "data: %s\n\n", string(data))
			w.flush()
		} else if part.Text != "" {
			if part.Thought {
				st.reasoning += part.Text
				c := part.Text
				chunk := ChatStreamChunk{
					ID:      st.responseID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   model,
					Choices: []ChatStreamChoice{{
						Index: 0,
						Delta: ChatStreamDelta{ReasoningContent: &c},
					}},
				}
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w.sink, "data: %s\n\n", string(data))
				w.flush()
			} else {
				st.content += part.Text
				c := part.Text
				chunk := ChatStreamChunk{
					ID:      st.responseID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   model,
					Choices: []ChatStreamChoice{{
						Index: 0,
						Delta: ChatStreamDelta{Content: &c},
					}},
				}
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w.sink, "data: %s\n\n", string(data))
				w.flush()
			}
		}
	}
}

func (st *chatFromGeminiStreamState) buildResponse(model string) *ChatResponse {
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
	for _, tc := range st.toolCalls {
		msg.ToolCalls = append(msg.ToolCalls, ChatToolCall{
			ID:   tc.id,
			Type: "function",
			Function: FunctionCall{
				Name:      tc.name,
				Arguments: tc.argsJSON,
			},
		})
	}
	finish := geminiStopToChatFinish(st.finishReason)
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

// ==================== Helper Functions ====================

func chatAssistantToGeminiContent(msg ChatMessage) GeminiContent {
	content := GeminiContent{Role: "model"}
	if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
		content.Parts = append(content.Parts, GeminiPart{Text: *msg.ReasoningContent, Thought: true})
	}
	if msg.Content != nil {
		text := extractTextFromContent(msg.Content)
		if text != "" {
			content.Parts = append(content.Parts, GeminiPart{Text: text})
		}
	}
	for _, tc := range msg.ToolCalls {
		args := make(map[string]interface{})
		if tc.Function.Arguments != "" {
			json.Unmarshal([]byte(tc.Function.Arguments), &args)
		}
		content.Parts = append(content.Parts, GeminiPart{
			FunctionCall: &GeminiFunctionCall{
				Name: tc.Function.Name,
				Args: args,
			},
		})
	}
	if len(content.Parts) == 0 {
		content.Parts = []GeminiPart{{Text: ""}}
	}
	return content
}

func chatToolToGeminiContent(msg ChatMessage) GeminiContent {
	output := extractTextFromContent(msg.Content)
	respMap := map[string]interface{}{"output": output}
	return GeminiContent{
		Role: "function",
		Parts: []GeminiPart{{
			FunctionResponse: &GeminiFunctionResponse{
				Name:     msg.ToolCallID,
				Response: respMap,
			},
		}},
	}
}

func geminiContentToText(content *GeminiContent) string {
	var parts []string
	for _, p := range content.Parts {
		if p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func geminiContentToChatMessages(content GeminiContent) []ChatMessage {
	var msgs []ChatMessage
	role := "user"
	if content.Role == "model" {
		role = "assistant"
	}

	if role == "assistant" {
		msg := ChatMessage{Role: "assistant"}
		var textParts []string
		for _, part := range content.Parts {
			if part.FunctionCall != nil {
				argsBytes, _ := json.Marshal(part.FunctionCall.Args)
				msg.ToolCalls = append(msg.ToolCalls, ChatToolCall{
					ID:   "call_" + generateID(),
					Type: "function",
					Function: FunctionCall{
						Name:      part.FunctionCall.Name,
						Arguments: string(argsBytes),
					},
				})
			} else if part.Text != "" {
				textParts = append(textParts, part.Text)
			}
		}
		text := strings.Join(textParts, "")
		msg.Content = text
		msgs = append(msgs, msg)
	} else {
		// User message - check for function responses.
		var userParts []string
		for _, part := range content.Parts {
			if part.FunctionResponse != nil {
				output := ""
				if o, ok := part.FunctionResponse.Response["output"]; ok {
					output = fmt.Sprintf("%v", o)
				}
				msgs = append(msgs, ChatMessage{
					Role:       "tool",
					ToolCallID: part.FunctionResponse.Name,
					Content:    output,
				})
			} else if part.Text != "" {
				userParts = append(userParts, part.Text)
			}
		}
		if len(userParts) > 0 {
			msgs = append([]ChatMessage{{Role: "user", Content: strings.Join(userParts, "\n")}}, msgs...)
		}
	}
	return msgs
}

func mergeGeminiContents(contents []GeminiContent) []GeminiContent {
	if len(contents) == 0 {
		return contents
	}
	merged := []GeminiContent{contents[0]}
	for _, c := range contents[1:] {
		last := &merged[len(merged)-1]
		if last.Role == c.Role {
			last.Parts = append(last.Parts, c.Parts...)
		} else {
			merged = append(merged, c)
		}
	}
	return merged
}

func chatFinishToGeminiStop(reason string) string {
	switch reason {
	case "stop":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	case "tool_calls":
		return "STOP"
	case "content_filter":
		return "SAFETY"
	}
	return "STOP"
}

func geminiStopToChatFinish(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	case "RECITATION":
		return "content_filter"
	}
	return "stop"
}

func strPtr(s string) *string { return &s }