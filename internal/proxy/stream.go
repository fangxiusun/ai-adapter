package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/debuglog"
	"github.com/fangxiusun/ai-adapter/internal/translate"
)

// ==================== Stream Forwarding ====================

func (h *ProxyHandler) streamFromChatSource(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, target config.InterfaceType, chatReq *translate.ChatRequest, model string, targetReq interface{}, deepLog *debuglog.RequestLog) {
	sourceBody, err := json.Marshal(chatReq)
	if err != nil {
		h.sendError(w, 500, "marshal_failed", err.Error())
		return
	}
	path := upstreamPathForInterface(config.InterfaceChat, model, true)
	logPath := "/v1/chat/completions"
	rs := newRetryState(ch)
	for {
		if h.checkRotationAndTimeout(ch, rs, w, reqID, logPath) {
			return
		}
		key := h.getNextKey(ch, rs)
		if key == nil {
			h.sendError(w, 503, "no_available_keys", "all keys are unavailable")
			return
		}
		url := ch.Config.NativeBaseURL(config.InterfaceChat) + path
		httpReq, err := http.NewRequestWithContext(r.Context(), "POST", url, bytes.NewReader(sourceBody))
		if err != nil {
			h.sendError(w, 500, "create_request_failed", err.Error())
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+key.Value)
		if processed := h.processRequestHeaders(ch, model, r.Header); processed != nil {
			applyProcessedHeaders(httpReq.Header, processed, "Content-Type", "Authorization")
		}
		deepLog.LogUpstreamRequestHeader("POST", url, httpReq.Header)
		deepLog.LogUpstreamRequestBody(sourceBody)
		resp, err := ch.HTTPClient().Do(httpReq)
		if err != nil {
			ch.ReportError(key.Value, 0)
			rs.excluded[key.Value] = true
			continue
		}
		if resp.StatusCode == 401 {
			resp.Body.Close()
			ch.ReportError(key.Value, 401)
			rs.excluded[key.Value] = true
			continue
		}
		if resp.StatusCode == 429 {
			resp.Body.Close()
			ch.ReportError(key.Value, 429)
			rs.excluded[key.Value] = true
			time.Sleep(rs.retryDelay)
			continue
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			ch.ReportError(key.Value, resp.StatusCode)
			rs.excluded[key.Value] = true
			continue
		}
		if resp.StatusCode >= 400 {
			errBodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			ch.ReportError(key.Value, resp.StatusCode)
			rs.excluded[key.Value] = true
			h.logger.Warn("upstream_error",
				"request_id", reqID,
				"channel", ch.Config.ID,
				"model", model,
				"status", resp.StatusCode,
				"url", url,
				"request_body", string(sourceBody),
				"upstream_body", string(errBodyBytes),
			)
			h.sendError(w, resp.StatusCode, "upstream_error",
				fmt.Sprintf("upstream returned %d: %s", resp.StatusCode, string(errBodyBytes)))
			return
		}

		deepLog.LogUpstreamResponseHeader(resp.StatusCode, resp.Header)
		deepLog.LogUpstreamStreamResponse(resp.StatusCode, nil)
		flusher := func() {
		if processed := h.processResponseHeaders(ch, model, resp.Header); processed != nil {
			applyProcessedHeaders(w.Header(), processed, "Content-Type", "Cache-Control", "Connection")
		}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		deepLog.LogClientResponseHeader(200, w.Header())
		w.WriteHeader(200)
		switch target {
		case config.InterfaceChat:
			io.Copy(w, resp.Body)
		case config.InterfaceResponses:
			respReq, _ := targetReq.(*translate.ResponsesRequest)
			translate.PipeChatStreamToResponses(r.Context(), resp.Body, w, respReq, translate.TranslateOpts{ExtractInlineThink: true})
		case config.InterfaceMessages:
			translate.PipeChatStreamToClaude(r.Context(), resp.Body, w, chatReq, flusher)
		case config.InterfaceGenerateContent:
			translate.PipeChatStreamToGemini(r.Context(), resp.Body, w, chatReq, flusher)
		}
		resp.Body.Close()
		ch.RecordLatency(key.Value, rs.elapsed().Milliseconds())
		ch.ReportSuccess(key.Value)
		h.recordLog(reqID, ch.Config.ID, model, model, 200, rs.elapsed().Milliseconds(), key.Value, "", "")
		h.logger.LogRequest(reqID, "POST", logPath, 200, rs.elapsed().Milliseconds(), key.Value, ch.Config.ID, model)
		return
	}
}

func (h *ProxyHandler) streamChainConversion(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, source config.InterfaceType, target config.InterfaceType, chatReq *translate.ChatRequest, model string, targetReq interface{}, deepLog *debuglog.RequestLog) {
	sourceReq, err := convertChatToSource(source, chatReq)
	if err != nil {
		h.sendError(w, 400, "convert_to_source_failed", err.Error())
		return
	}
	sourceBody, err := json.Marshal(sourceReq)
	if err != nil {
		h.sendError(w, 500, "marshal_source_failed", err.Error())
		return
	}
	path := upstreamPathForInterface(source, model, true)
	logPath := path
	if idx := strings.Index(logPath, "?"); idx >= 0 {
		logPath = logPath[:idx]
	}
	rs := newRetryState(ch)
	for {
		if h.checkRotationAndTimeout(ch, rs, w, reqID, logPath) {
			return
		}
		key := h.getNextKey(ch, rs)
		if key == nil {
			h.sendError(w, 503, "no_available_keys", "all keys are unavailable")
			return
		}
		url := ch.Config.NativeBaseURL(source) + path
		httpReq, err := http.NewRequestWithContext(r.Context(), "POST", url, bytes.NewReader(sourceBody))
		if err != nil {
			h.sendError(w, 500, "create_request_failed", err.Error())
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+key.Value)
		if processed := h.processRequestHeaders(ch, model, r.Header); processed != nil {
			applyProcessedHeaders(httpReq.Header, processed, "Content-Type", "Authorization")
		}
		deepLog.LogUpstreamRequestHeader("POST", url, httpReq.Header)
		deepLog.LogUpstreamRequestBody(sourceBody)
		resp, err := ch.HTTPClient().Do(httpReq)
		if err != nil {
			ch.ReportError(key.Value, 0)
			rs.excluded[key.Value] = true
			continue
		}
		if resp.StatusCode == 401 {
			resp.Body.Close()
			ch.ReportError(key.Value, 401)
			rs.excluded[key.Value] = true
			continue
		}
		if resp.StatusCode == 429 {
			resp.Body.Close()
			ch.ReportError(key.Value, 429)
			rs.excluded[key.Value] = true
			time.Sleep(rs.retryDelay)
			continue
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			ch.ReportError(key.Value, resp.StatusCode)
			rs.excluded[key.Value] = true
			continue
		}
		if resp.StatusCode >= 400 {
			errBodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			ch.ReportError(key.Value, resp.StatusCode)
			rs.excluded[key.Value] = true
			h.logger.Warn("upstream_error",
				"request_id", reqID,
				"channel", ch.Config.ID,
				"model", model,
				"status", resp.StatusCode,
				"url", url,
				"request_body", string(sourceBody),
				"upstream_body", string(errBodyBytes),
			)
			h.sendError(w, resp.StatusCode, "upstream_error",
				fmt.Sprintf("upstream returned %d: %s", resp.StatusCode, string(errBodyBytes)))
			return
		}

		chatResp, err := h.accumulateStreamToChat(r.Context(), source, resp.Body, chatReq)
		resp.Body.Close()
		if err != nil {
			ch.ReportStreamError(key.Value)
			rs.excluded[key.Value] = true
			continue
		}
		ch.RecordLatency(key.Value, rs.elapsed().Milliseconds())
		ch.ReportSuccess(key.Value)
		deepLog.LogUpstreamResponseHeader(resp.StatusCode, resp.Header)
		deepLog.LogUpstreamStreamResponse(resp.StatusCode, nil)
		flusher := func() {
		if processed := h.processResponseHeaders(ch, model, resp.Header); processed != nil {
			applyProcessedHeaders(w.Header(), processed, "Content-Type", "Cache-Control", "Connection")
		}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		deepLog.LogClientResponseHeader(200, w.Header())
		w.WriteHeader(200)
		h.emitStreamResponse(w, target, chatResp, chatReq, targetReq, flusher)
		h.recordLog(reqID, ch.Config.ID, model, model, 200, rs.elapsed().Milliseconds(), key.Value, "", "")
		h.logger.LogRequest(reqID, "POST", logPath, 200, rs.elapsed().Milliseconds(), key.Value, ch.Config.ID, model)
		return
	}
}

// ==================== Stream Accumulation ====================

func (h *ProxyHandler) accumulateStreamToChat(ctx context.Context, source config.InterfaceType, upstream io.Reader, chatReq *translate.ChatRequest) (*translate.ChatResponse, error) {
	switch source {
	case config.InterfaceChat:
		return h.accumulateChatStream(upstream, chatReq)
	case config.InterfaceResponses:
		return translate.PipeResponsesStreamToChat(ctx, upstream, io.Discard, chatReq, translate.TranslateOpts{})
	case config.InterfaceMessages:
		return translate.PipeClaudeStreamToChat(ctx, upstream, io.Discard, chatReq, nil)
	case config.InterfaceGenerateContent:
		return translate.PipeGeminiStreamToChat(ctx, upstream, io.Discard, chatReq, nil)
	default:
		return nil, fmt.Errorf("unsupported source: %s", source)
	}
}

func (h *ProxyHandler) accumulateChatStream(upstream io.Reader, chatReq *translate.ChatRequest) (*translate.ChatResponse, error) {
	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var content, reasoningContent string
	var toolCalls []translate.ChatToolCall
	var usage *translate.ChatUsage
	var model, id string
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk translate.ChatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.ID != "" {
			id = chunk.ID
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			if delta.Content != nil && *delta.Content != "" {
				content += *delta.Content
			}
			if delta.ReasoningContent != nil && *delta.ReasoningContent != "" {
				reasoningContent += *delta.ReasoningContent
			}
			for _, tc := range delta.ToolCalls {
				if tc.Function != nil && tc.Function.Name != "" {
					toolCalls = append(toolCalls, translate.ChatToolCall{
						ID: tc.ID, Type: "function",
						Function: translate.FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
					})
				} else if tc.Function != nil && tc.Function.Arguments != "" && len(toolCalls) > 0 {
					toolCalls[len(toolCalls)-1].Function.Arguments += tc.Function.Arguments
				}
			}
		}
	}
	msg := translate.ChatChoiceMsg{Role: "assistant"}
	msg.Content = &content
	if content == "" {
		s := ""
		msg.Content = &s
	}
	if reasoningContent != "" {
		msg.ReasoningContent = &reasoningContent
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}
	if id == "" {
		id = "chatcmpl-" + generateRequestID()
	}
	if model == "" {
		model = chatReq.Model
	}
	return &translate.ChatResponse{
		ID: id, Object: "chat.completion", Created: time.Now().Unix(), Model: model,
		Choices: []translate.ChatChoice{{Index: 0, Message: msg, FinishReason: "stop"}},
		Usage:   usage,
	}, nil
}

// ==================== Stream Emission ====================

func (h *ProxyHandler) emitStreamResponse(w io.Writer, target config.InterfaceType, chatResp *translate.ChatResponse, chatReq *translate.ChatRequest, targetReq interface{}, flusher func()) {
	switch target {
	case config.InterfaceChat:
		h.emitChatStreamResponse(w, chatResp, flusher)
	case config.InterfaceResponses:
		h.emitResponsesStreamResponse(w, chatResp, chatReq, targetReq, flusher)
	case config.InterfaceMessages:
		h.emitClaudeStreamResponse(w, chatResp, flusher)
	case config.InterfaceGenerateContent:
		h.emitGeminiStreamResponse(w, chatResp, flusher)
	}
}

func (h *ProxyHandler) emitChatStreamResponse(w io.Writer, resp *translate.ChatResponse, flusher func()) {
	sw := newSSEWriter(w, flusher)
	for _, choice := range resp.Choices {
		chunk := translate.ChatStreamChunk{
			ID:      resp.ID,
			Object:  "chat.completion.chunk",
			Created: resp.Created,
			Model:   resp.Model,
			Choices: []translate.ChatStreamChoice{{
				Index: choice.Index,
				Delta: translate.ChatStreamDelta{
					Role:    "assistant",
					Content: choice.Message.Content,
				},
				FinishReason: choice.FinishReason,
			}},
		}
		sw.writeEvent("message", chunk)
	}
	sw.writeEvent("done", "[DONE]")
}

func (h *ProxyHandler) emitResponsesStreamResponse(w io.Writer, chatResp *translate.ChatResponse, chatReq *translate.ChatRequest, targetReq interface{}, flusher func()) {
	respReq, _ := targetReq.(*translate.ResponsesRequest)
	if respReq == nil {
		respReq = &translate.ResponsesRequest{}
	}
	sw := newSSEWriter(w, flusher)
	fullResp := translate.RespToResponses(chatResp, respReq, translate.TranslateOpts{ExtractInlineThink: true})
	sw.writeEvent("response.created", map[string]interface{}{
		"type":     "response.created",
		"response": fullResp,
	})
	sw.writeEvent("response.completed", map[string]interface{}{
		"type":     "response.completed",
		"response": fullResp,
	})
}

func (h *ProxyHandler) emitClaudeStreamResponse(w io.Writer, chatResp *translate.ChatResponse, flusher func()) {
	claudeResp := translate.ChatToClaudeResponse(chatResp)
	sw := newSSEWriter(w, flusher)
	sw.writeEvent("message_start", map[string]interface{}{"message": claudeResp})
	for i, block := range claudeResp.Content {
		sw.writeEvent("content_block_start", map[string]interface{}{"index": i, "content_block": block})
		switch block.Type {
		case "text":
			sw.writeEvent("content_block_delta", map[string]interface{}{
				"index": i, "delta": map[string]interface{}{"type": "text_delta", "text": block.Text},
			})
		case "tool_use":
			inputJSON, _ := json.Marshal(block.Input)
			sw.writeEvent("content_block_delta", map[string]interface{}{
				"index": i, "delta": map[string]interface{}{"type": "input_json_delta", "partial_json": string(inputJSON)},
			})
		}
		sw.writeEvent("content_block_stop", map[string]interface{}{"index": i})
	}
	sw.writeEvent("message_delta", map[string]interface{}{
		"delta": map[string]interface{}{"stop_reason": claudeResp.StopReason},
		"usage": map[string]interface{}{"output_tokens": claudeResp.Usage.OutputTokens},
	})
	sw.writeEvent("message_stop", nil)
}

func (h *ProxyHandler) emitGeminiStreamResponse(w io.Writer, chatResp *translate.ChatResponse, flusher func()) {
	geminiResp := translate.ChatToGeminiResponse(chatResp)
	data, _ := json.Marshal(geminiResp)
	fmt.Fprintf(w, "%s\n", string(data))
	if flusher != nil {
		flusher()
	}
}
