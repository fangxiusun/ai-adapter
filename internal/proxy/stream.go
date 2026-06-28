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



// fanoutStreamForward sends the stream request to multiple keys concurrently
// and forwards the first successful response to the client.
func (h *ProxyHandler) fanoutStreamForward(w http.ResponseWriter, r *http.Request, reqID string,
	ch *channel.Channel, target config.InterfaceType, chatReq *translate.ChatRequest,
	model string, sourceBody []byte, path string, targetReq interface{}, deepLog *debuglog.RequestLog) *FailoverError {

	url := ch.Config.NativeBaseURL(config.InterfaceChat) + path

	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	if processed := h.processRequestHeaders(ch, model, r.Header); processed != nil {
		applyProcessedHeaders(headers, processed, "Content-Type", "Authorization")
	}

	deepLog.LogUpstreamRequestHeader("POST", url, headers)
	deepLog.LogUpstreamRequestBody(sourceBody)

	start := time.Now()
	result := ch.FanoutStream(r.Context(), channel.FanoutRequest{
		Body:    sourceBody,
		URL:     url,
		Headers: headers,
	})

	if result.Error != nil {
		h.logger.Warn("fanout_stream_failed", "request_id", reqID, "channel", ch.Config.ID, "error", result.Error)
		return &FailoverError{StatusCode: 0, Message: fmt.Sprintf("channel %s: fanout stream failed: %s", ch.Config.ID, result.Error)}
	}

	resp := result.Response
	defer resp.Body.Close()

	ch.RecordLatency(result.Key, time.Since(start).Milliseconds())
	ch.ReportSuccess(result.Key)

	deepLog.LogUpstreamResponseHeader(resp.StatusCode, resp.Header)
	deepLog.LogUpstreamStreamResponse(resp.StatusCode, nil)

	capture := newStreamUsageCapture(resp.Body)
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
		io.Copy(w, capture)
	case config.InterfaceResponses:
		respReq, _ := targetReq.(*translate.ResponsesRequest)
		translate.PipeChatStreamToResponses(r.Context(), capture, w, respReq, translate.TranslateOpts{ExtractInlineThink: true})
	case config.InterfaceMessages:
		translate.PipeChatStreamToClaude(r.Context(), capture, w, chatReq, flusher)
	case config.InterfaceGenerateContent:
		translate.PipeChatStreamToGemini(r.Context(), capture, w, chatReq, flusher)
	}

	pt, ct, tt, usageJSON := capture.Usage()
	h.recordLog(reqID, ch.Config.ID, model, model, 200, time.Since(start).Milliseconds(), result.Key, "", "", pt, ct, tt, usageJSON, string(target))
	h.logger.LogRequest(reqID, "POST", path, 200, time.Since(start).Milliseconds(), result.Key, ch.Config.ID, model)
	return nil
}

// ==================== Stream Forwarding ====================

func (h *ProxyHandler) streamFromChatSource(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, target config.InterfaceType, chatReq *translate.ChatRequest, model string, targetReq interface{}, deepLog *debuglog.RequestLog) *FailoverError {
	// Inject stream_options.include_usage=true for Chat upstream.
	injectedReq := *chatReq
	injectedReq.StreamOptions = &translate.StreamOptions{IncludeUsage: true}
	sourceBody, err := json.Marshal(&injectedReq)
	if err != nil {
		h.sendError(w, reqID, 500, "marshal_failed", err.Error())
		return nil
	}
	path := upstreamPathForInterface(config.InterfaceChat, model, true)
	logPath := "/v1/chat/completions"

	// Fanout fast-path for streaming requests.
	if ch.FanoutEnabled() {
		return h.fanoutStreamForward(w, r, reqID, ch, target, chatReq, model, sourceBody, path, targetReq, deepLog)
	}
	rs := newRetryState(ch, h.config.Failover.ConsecutiveFailThreshold)
	for {
		if fe := h.checkRotationAndTimeout(ch, rs, reqID); fe != nil {
			return fe
		}
		key := h.getNextKey(ch, rs)
		if key == nil {
			return &FailoverError{StatusCode: 503, Message: fmt.Sprintf("channel %s: no available keys", ch.Config.ID)}
		}
		url := ch.Config.NativeBaseURL(config.InterfaceChat) + path
		httpReq, err := http.NewRequestWithContext(r.Context(), "POST", url, bytes.NewReader(sourceBody))
		if err != nil {
			h.sendError(w, reqID, 500, "create_request_failed", err.Error())
			return nil
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
			rs.consecFails++
			if rs.consecFails >= rs.consecFailThreshold {
				return &FailoverError{StatusCode: 0, Message: fmt.Sprintf("channel %s: connection failed after %d consecutive errors: %s", ch.Config.ID, rs.consecFails, err.Error())}
			}
			continue
		}
		if resp.StatusCode == 401 {
			resp.Body.Close()
			ch.ReportError(key.Value, 401)
			rs.excluded[key.Value] = true
			rs.consecFails = 0
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
			rs.consecFails++
			if rs.consecFails >= rs.consecFailThreshold {
				return &FailoverError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("channel %s: %d consecutive %d errors", ch.Config.ID, rs.consecFails, resp.StatusCode)}
			}
			continue
		}
		if resp.StatusCode == 400 {
			errBodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			ch.ReportError(key.Value, 400)
			h.sendError(w, reqID, 400, "bad_request", string(errBodyBytes))
			return nil
		}
		if resp.StatusCode >= 400 {
			errBodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			ch.ReportError(key.Value, resp.StatusCode)
			rs.excluded[key.Value] = true
			rs.consecFails = 0
			h.logger.Warn("upstream_error",
				"request_id", reqID,
				"channel", ch.Config.ID,
				"model", model,
				"status", resp.StatusCode,
				"url", url,
				"request_body", string(sourceBody),
				"upstream_body", string(errBodyBytes),
			)
			return &FailoverError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("channel %s: upstream returned %d", ch.Config.ID, resp.StatusCode)}
		}

		deepLog.LogUpstreamResponseHeader(resp.StatusCode, resp.Header)
		deepLog.LogUpstreamStreamResponse(resp.StatusCode, nil)
		capture := newStreamUsageCapture(resp.Body)
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
			io.Copy(w, capture)
		case config.InterfaceResponses:
			respReq, _ := targetReq.(*translate.ResponsesRequest)
			translate.PipeChatStreamToResponses(r.Context(), capture, w, respReq, translate.TranslateOpts{ExtractInlineThink: true})
		case config.InterfaceMessages:
			translate.PipeChatStreamToClaude(r.Context(), capture, w, chatReq, flusher)
		case config.InterfaceGenerateContent:
			translate.PipeChatStreamToGemini(r.Context(), capture, w, chatReq, flusher)
		}
		resp.Body.Close()
		pt, ct, tt, usageJSON := capture.Usage()
		ch.RecordLatency(key.Value, rs.elapsed().Milliseconds())
		ch.ReportSuccess(key.Value)
		h.recordLog(reqID, ch.Config.ID, model, model, 200, rs.elapsed().Milliseconds(), key.Value, "", "", pt, ct, tt, usageJSON, string(target))
		h.logger.LogRequest(reqID, "POST", logPath, 200, rs.elapsed().Milliseconds(), key.Value, ch.Config.ID, model)
		return nil
	}
}

func (h *ProxyHandler) streamChainConversion(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, source config.InterfaceType, target config.InterfaceType, chatReq *translate.ChatRequest, model string, targetReq interface{}, deepLog *debuglog.RequestLog) *FailoverError {
	sourceReq, err := convertChatToSource(source, chatReq)
	if err != nil {
		h.sendError(w, reqID, 400, "convert_to_source_failed", err.Error())
		return nil
	}
	sourceBody, err := json.Marshal(sourceReq)
	if err != nil {
		h.sendError(w, reqID, 500, "marshal_source_failed", err.Error())
		return nil
	}
	path := upstreamPathForInterface(source, model, true)
	logPath := path
	if idx := strings.Index(logPath, "?"); idx >= 0 {
		logPath = logPath[:idx]
	}
	rs := newRetryState(ch, h.config.Failover.ConsecutiveFailThreshold)
	for {
		if fe := h.checkRotationAndTimeout(ch, rs, reqID); fe != nil {
			return fe
		}
		key := h.getNextKey(ch, rs)
		if key == nil {
			return &FailoverError{StatusCode: 503, Message: fmt.Sprintf("channel %s: no available keys", ch.Config.ID)}
		}
		url := ch.Config.NativeBaseURL(source) + path
		httpReq, err := http.NewRequestWithContext(r.Context(), "POST", url, bytes.NewReader(sourceBody))
		if err != nil {
			h.sendError(w, reqID, 500, "create_request_failed", err.Error())
			return nil
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
			rs.consecFails++
			if rs.consecFails >= rs.consecFailThreshold {
				return &FailoverError{StatusCode: 0, Message: fmt.Sprintf("channel %s: connection failed after %d consecutive errors: %s", ch.Config.ID, rs.consecFails, err.Error())}
			}
			continue
		}
		if resp.StatusCode == 401 {
			resp.Body.Close()
			ch.ReportError(key.Value, 401)
			rs.excluded[key.Value] = true
			rs.consecFails = 0
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
			rs.consecFails++
			if rs.consecFails >= rs.consecFailThreshold {
				return &FailoverError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("channel %s: %d consecutive %d errors", ch.Config.ID, rs.consecFails, resp.StatusCode)}
			}
			continue
		}
		if resp.StatusCode == 400 {
			errBodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			ch.ReportError(key.Value, 400)
			h.sendError(w, reqID, 400, "bad_request", string(errBodyBytes))
			return nil
		}
		if resp.StatusCode >= 400 {
			errBodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			ch.ReportError(key.Value, resp.StatusCode)
			rs.excluded[key.Value] = true
			rs.consecFails = 0
			h.logger.Warn("upstream_error",
				"request_id", reqID,
				"channel", ch.Config.ID,
				"model", model,
				"status", resp.StatusCode,
				"url", url,
				"request_body", string(sourceBody),
				"upstream_body", string(errBodyBytes),
			)
			return &FailoverError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("channel %s: upstream returned %d", ch.Config.ID, resp.StatusCode)}
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
		pt, ct, tt, usageJSON := normalizeUsage(chatResp.Usage)
		h.recordLog(reqID, ch.Config.ID, model, model, 200, rs.elapsed().Milliseconds(), key.Value, "", "", pt, ct, tt, usageJSON, string(target))
		h.logger.LogRequest(reqID, "POST", logPath, 200, rs.elapsed().Milliseconds(), key.Value, ch.Config.ID, model)
		return nil
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


