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
	"github.com/fangxiusun/ai-adapter/internal/db"
	"github.com/fangxiusun/ai-adapter/internal/debuglog"
	"github.com/fangxiusun/ai-adapter/internal/log"
	"github.com/fangxiusun/ai-adapter/internal/translate"
)

type ProxyHandler struct {
	channels *channel.ChannelManager
	db       *db.DB
	logger   *log.Logger
	config   *config.Config
	deepDebug *debuglog.DeepDebugLogger
}

func NewProxyHandler(channels *channel.ChannelManager, database *db.DB, logger *log.Logger, cfg *config.Config, deepDebug *debuglog.DeepDebugLogger) *ProxyHandler {
	return &ProxyHandler{channels: channels, db: database, logger: logger, config: cfg, deepDebug: deepDebug}
}

type UpstreamResult struct {
	Body       []byte
	StatusCode int
	Key        *channel.KeyEntry
	LatencyMs  int64
	Error      error
}

type RetryState struct {
	start        time.Time
	excluded     map[string]bool
	maxRounds    int
	retryDelay   time.Duration
	maxTotalWait time.Duration
	lastResult   *UpstreamResult
	lastErr      error
}

func newRetryState(ch *channel.Channel) *RetryState {
	cfg := ch.Config.Retry
	return &RetryState{
		start:        time.Now(),
		excluded:     make(map[string]bool),
		maxRounds:    cfg.MaxRotationRounds,
		retryDelay:   time.Duration(cfg.RetryDelay429Ms) * time.Millisecond,
		maxTotalWait: time.Duration(cfg.MaxTotalWaitMs) * time.Millisecond,
		lastErr:      fmt.Errorf("all retries failed"),
	}
}

func (rs *RetryState) isTimedOut() bool {
	return time.Since(rs.start) >= rs.maxTotalWait
}

func (rs *RetryState) elapsed() time.Duration {
	return time.Since(rs.start)
}

// ==================== Entry Points ====================

func (h *ProxyHandler) HandleChat(w http.ResponseWriter, r *http.Request) {
	reqID := generateRequestID()
	h.logger.Debug("incoming request", "request_id", reqID, "path", "/v1/chat/completions", "target", "chat")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024*1024))
	if err != nil {
		h.sendError(w, 400, "read_body_failed", err.Error())
		return
	}
	body = stripUTF8BOM(body)
	h.logger.LogRequestBody(reqID, body)
	h.logger.LogClientInput(reqID, body)
	deepLog := h.deepDebug.BeginRequest(reqID, r.Method, r.URL.Path)
	deepLog.LogClientRequestHeader(r)
	deepLog.LogClientRequestBody(body)
	defer deepLog.Close()
	var req translate.ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		h.sendError(w, 400, "invalid_json", err.Error())
		return
	}
	if req.Model == "" {
		h.sendError(w, 400, "missing_model", "model is required")
		return
	}
	ch, modelInfo, err := h.channels.SelectChannel(req.Model)
	if err != nil {
		h.sendError(w, 404, "no_channel", err.Error())
		return
	}
	upstreamModel := req.Model
	if modelInfo != nil && modelInfo.ID != "" {
		upstreamModel = modelInfo.ID
	}
	req.Model = upstreamModel
	h.dispatch(w, r, reqID, ch, config.InterfaceChat, upstreamModel, req.Stream, body, &req, deepLog)
}

func (h *ProxyHandler) HandleResponses(w http.ResponseWriter, r *http.Request) {
	reqID := generateRequestID()
	h.logger.Debug("incoming request", "request_id", reqID, "path", "/v1/responses", "target", "responses")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024*1024))
	if err != nil {
		h.sendError(w, 400, "read_body_failed", err.Error())
		return
	}
	body = stripUTF8BOM(body)
	h.logger.LogRequestBody(reqID, body)
	h.logger.LogClientInput(reqID, body)
	deepLog := h.deepDebug.BeginRequest(reqID, r.Method, r.URL.Path)
	deepLog.LogClientRequestHeader(r)
	deepLog.LogClientRequestBody(body)
	defer deepLog.Close()
	var req translate.ResponsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		h.sendError(w, 400, "invalid_json", err.Error())
		return
	}
	if req.Model == "" {
		h.sendError(w, 400, "missing_model", "model is required")
		return
	}
	ch, modelInfo, err := h.channels.SelectChannel(req.Model)
	if err != nil {
		h.sendError(w, 404, "no_channel", err.Error())
		return
	}
	upstreamModel := req.Model
	if modelInfo != nil && modelInfo.ID != "" {
		upstreamModel = modelInfo.ID
	}
	req.Model = upstreamModel
	h.dispatch(w, r, reqID, ch, config.InterfaceResponses, upstreamModel, req.Stream, body, &req, deepLog)
}

func (h *ProxyHandler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	reqID := generateRequestID()
	h.logger.Debug("incoming request", "request_id", reqID, "path", "/v1/messages", "target", "messages")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024*1024))
	if err != nil {
		h.sendError(w, 400, "read_body_failed", err.Error())
		return
	}
	body = stripUTF8BOM(body)
	h.logger.LogRequestBody(reqID, body)
	h.logger.LogClientInput(reqID, body)
	deepLog := h.deepDebug.BeginRequest(reqID, r.Method, r.URL.Path)
	deepLog.LogClientRequestHeader(r)
	deepLog.LogClientRequestBody(body)
	defer deepLog.Close()
	var req translate.ClaudeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		h.sendError(w, 400, "invalid_json", err.Error())
		return
	}
	if req.Model == "" {
		h.sendError(w, 400, "missing_model", "model is required")
		return
	}
	ch, modelInfo, err := h.channels.SelectChannel(req.Model)
	if err != nil {
		h.sendError(w, 404, "no_channel", err.Error())
		return
	}
	upstreamModel := req.Model
	if modelInfo != nil && modelInfo.ID != "" {
		upstreamModel = modelInfo.ID
	}
	req.Model = upstreamModel
	h.dispatch(w, r, reqID, ch, config.InterfaceMessages, upstreamModel, req.Stream, body, &req, deepLog)
}

func (h *ProxyHandler) HandleGenerateContent(w http.ResponseWriter, r *http.Request) {
	reqID := generateRequestID()
	model := extractGeminiModel(r.URL.Path)
	stream := strings.Contains(r.URL.Path, "streamGenerateContent")
	h.logger.Debug("incoming request", "request_id", reqID, "path", r.URL.Path, "target", "generate_content", "model", model, "stream", stream)
	if model == "" {
		h.sendError(w, 400, "missing_model", "could not extract model from URL path")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024*1024))
	if err != nil {
		h.sendError(w, 400, "read_body_failed", err.Error())
		return
	}
	body = stripUTF8BOM(body)
	h.logger.LogRequestBody(reqID, body)
	h.logger.LogClientInput(reqID, body)
	deepLog := h.deepDebug.BeginRequest(reqID, r.Method, r.URL.Path)
	deepLog.LogClientRequestHeader(r)
	deepLog.LogClientRequestBody(body)
	defer deepLog.Close()
	var req translate.GeminiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		h.sendError(w, 400, "invalid_json", err.Error())
		return
	}
	ch, modelInfo, err := h.channels.SelectChannel(model)
	if err != nil {
		h.sendError(w, 404, "no_channel", err.Error())
		return
	}
	upstreamModel := model
	if modelInfo != nil && modelInfo.ID != "" {
		upstreamModel = modelInfo.ID
	}
	h.dispatch(w, r, reqID, ch, config.InterfaceGenerateContent, upstreamModel, stream, body, &req, deepLog)
}

// ==================== Core Dispatch ====================

func (h *ProxyHandler) dispatch(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, target config.InterfaceType, model string, stream bool, rawBody []byte, targetReq interface{}, deepLog *debuglog.RequestLog) {
	source, ok := config.BestSourceForTarget(target, &ch.Config)
	if !ok {
		h.sendError(w, 503, "no_conversion_path",
			fmt.Sprintf("channel %s has no native interface and no conversion path to %s", ch.Config.ID, target))
		return
	}
	h.logger.Debug("dispatch", "request_id", reqID, "target", target, "source", source, "native", source == target)
	if source == target {
		h.nativeForward(w, r, reqID, ch, source, rawBody, model, stream, deepLog)
		return
	}
	chatReq, err := h.buildChatRequest(target, targetReq, model, stream)
	if err != nil {
		h.sendError(w, 400, "convert_failed", err.Error())
		return
	}
	if stream {
		h.convertedStreamForward(w, r, reqID, ch, source, target, chatReq, model, targetReq, deepLog)
	} else {
		h.convertedNonStreamForward(w, r, reqID, ch, source, target, chatReq, model, targetReq, deepLog)
	}
}

func (h *ProxyHandler) buildChatRequest(target config.InterfaceType, targetReq interface{}, model string, stream bool) (*translate.ChatRequest, error) {
	switch target {
	case config.InterfaceChat:
		return targetReq.(*translate.ChatRequest), nil
	case config.InterfaceResponses:
		return translate.ReqToChat(targetReq.(*translate.ResponsesRequest), translate.TranslateOpts{ForceParallelTools: true})
	case config.InterfaceMessages:
		return translate.ClaudeToChatRequest(targetReq.(*translate.ClaudeRequest))
	case config.InterfaceGenerateContent:
		req, err := translate.GeminiToChatRequest(targetReq.(*translate.GeminiRequest))
		if req != nil {
			req.Model = model
			req.Stream = stream
		}
		return req, err
	default:
		return nil, fmt.Errorf("unsupported target: %s", target)
	}
}

// ==================== Native Forwarding ====================

func (h *ProxyHandler) nativeForward(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, iface config.InterfaceType, body []byte, model string, stream bool, deepLog *debuglog.RequestLog) {
	path := upstreamPathForInterface(iface, model, stream)
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
		url := ch.Config.NativeBaseURL(iface) + path
		httpReq, err := http.NewRequestWithContext(r.Context(), "POST", url, bytes.NewReader(body))
		if err != nil {
			h.sendError(w, 500, "create_request_failed", err.Error())
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+key.Value)
		deepLog.LogUpstreamRequestHeader("POST", url, httpReq.Header)
		deepLog.LogUpstreamRequestBody(body)
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
				"request_body", string(body),
				"upstream_body", string(errBodyBytes),
			)
			h.sendError(w, resp.StatusCode, "upstream_error",
				fmt.Sprintf("upstream returned %d: %s", resp.StatusCode, string(errBodyBytes)))
			return
		}

		if stream {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, no-transform")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(200)
			io.Copy(w, resp.Body)
		} else {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
			deepLog.LogUpstreamResponseHeader(resp.StatusCode, resp.Header)
			deepLog.LogUpstreamResponseBody(respBody)
			deepLog.LogClientResponseHeader(resp.StatusCode, w.Header())
			deepLog.LogClientResponseBody(respBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			w.Write(respBody)
		}
		resp.Body.Close()
		ch.RecordLatency(key.Value, rs.elapsed().Milliseconds())
		ch.ReportSuccess(key.Value)
		h.recordLog(reqID, ch.Config.ID, model, model, 200, rs.elapsed().Milliseconds(), key.Value, "", "")
		h.logger.LogRequest(reqID, "POST", logPath, 200, rs.elapsed().Milliseconds(), key.Value, ch.Config.ID, model)
		return
	}
}

// ==================== Converted Forwarding (Non-Streaming) ====================

func (h *ProxyHandler) convertedNonStreamForward(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, source config.InterfaceType, target config.InterfaceType, chatReq *translate.ChatRequest, model string, targetReq interface{}, deepLog *debuglog.RequestLog) {
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
	path := upstreamPathForInterface(source, model, false)
	logPath := path
	if idx := strings.Index(logPath, "?"); idx >= 0 {
		logPath = logPath[:idx]
	}
	rs := newRetryState(ch)
	var result *UpstreamResult
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
		h.logger.Debug("upstream request", "request_id", reqID, "method", "POST", "url", url, "body", string(sourceBody))
		deepLog.LogUpstreamRequestHeader("POST", url, httpReq.Header)
		deepLog.LogUpstreamRequestBody(sourceBody)
		start := time.Now()
		resp, err := ch.HTTPClient().Do(httpReq)
		if err != nil {
			ch.ReportError(key.Value, 0)
			rs.excluded[key.Value] = true
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
		resp.Body.Close()
		latency := time.Since(start).Milliseconds()
		if resp.StatusCode == 401 {
			ch.ReportError(key.Value, 401)
			rs.excluded[key.Value] = true
			continue
		}
		if resp.StatusCode == 429 {
			ch.ReportError(key.Value, 429)
			rs.excluded[key.Value] = true
			time.Sleep(rs.retryDelay)
			continue
		}
		if resp.StatusCode >= 500 {
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

		ch.RecordLatency(key.Value, latency)
		ch.ReportSuccess(key.Value)
		result = &UpstreamResult{Body: respBody, StatusCode: resp.StatusCode, Key: key, LatencyMs: latency}
		break
	}
	deepLog.LogUpstreamResponseHeader(result.StatusCode, nil)
	deepLog.LogUpstreamResponseBody(result.Body)
	chatResp, err := convertSourceToChat(source, result.Body, chatReq)
	if err != nil {
		h.sendError(w, 502, "convert_from_source_failed", err.Error())
		return
	}
	targetResp, err := convertChatToTarget(target, chatResp, targetReq)
	if err != nil {
		h.sendError(w, 500, "convert_to_target_failed", err.Error())
		return
	}
	deepLog.LogClientResponseHeader(200, w.Header())
	deepLog.LogClientResponseBody(func() []byte { b, _ := json.Marshal(targetResp); return b }())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	json.NewEncoder(w).Encode(targetResp)
	h.recordLog(reqID, ch.Config.ID, model, model, 200, result.LatencyMs, result.Key.Value, "", "")
	h.logger.LogRequest(reqID, "POST", logPath, 200, result.LatencyMs, result.Key.Value, ch.Config.ID, model)
}

// ==================== Converted Forwarding (Streaming) ====================

func (h *ProxyHandler) convertedStreamForward(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, source config.InterfaceType, target config.InterfaceType, chatReq *translate.ChatRequest, model string, targetReq interface{}, deepLog *debuglog.RequestLog) {
	if source == config.InterfaceChat {
		h.streamFromChatSource(w, r, reqID, ch, target, chatReq, model, targetReq, deepLog)
		return
	}
	h.streamChainConversion(w, r, reqID, ch, source, target, chatReq, model, targetReq, deepLog)
}

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
	delta := translate.ChatStreamDelta{}
	if len(resp.Choices) > 0 {
		delta.Role = resp.Choices[0].Message.Role
		delta.Content = resp.Choices[0].Message.Content
		for i, tc := range resp.Choices[0].Message.ToolCalls {
			delta.ToolCalls = append(delta.ToolCalls, translate.ToolCallDelta{
				Index: i, ID: tc.ID, Type: "function",
				Function: &translate.FunctionDelta{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
			})
		}
	}
	chunk := translate.ChatStreamChunk{
		ID: resp.ID, Object: "chat.completion.chunk", Created: resp.Created, Model: resp.Model,
		Choices: []translate.ChatStreamChoice{{Index: 0, Delta: delta}},
	}
	data, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", string(data))
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher()
	}
}

func (h *ProxyHandler) emitResponsesStreamResponse(w io.Writer, chatResp *translate.ChatResponse, chatReq *translate.ChatRequest, targetReq interface{}, flusher func()) {
	respReq, _ := targetReq.(*translate.ResponsesRequest)
	if respReq == nil {
		respReq = &translate.ResponsesRequest{Model: chatReq.Model}
	}
	respReq.Stream = true
	translator := translate.NewStreamTranslator(w, respReq, translate.TranslateOpts{ExtractInlineThink: true}, flusher)
	translator.Start()
	if len(chatResp.Choices) > 0 {
		msg := chatResp.Choices[0].Message
		if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
			rc := *msg.ReasoningContent
			translator.ProcessChunk(&translate.ChatStreamChunk{
				ID: chatResp.ID, Model: chatResp.Model,
				Choices: []translate.ChatStreamChoice{{Delta: translate.ChatStreamDelta{ReasoningContent: &rc}}},
			})
		}
		if msg.Content != nil && *msg.Content != "" {
			c := *msg.Content
			translator.ProcessChunk(&translate.ChatStreamChunk{
				ID: chatResp.ID, Model: chatResp.Model,
				Choices: []translate.ChatStreamChoice{{Delta: translate.ChatStreamDelta{Content: &c}}},
			})
		}
	}
	translator.Finish()
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

// ==================== Helpers ====================

func (h *ProxyHandler) getNextKey(ch *channel.Channel, rs *RetryState) *channel.KeyEntry {
	for i := 0; i < 10; i++ {
		key := ch.GetKey()
		if key == nil {
			return nil
		}
		if !rs.excluded[key.Value] {
			return key
		}
	}
	return nil
}

func (h *ProxyHandler) checkRotationAndTimeout(ch *channel.Channel, rs *RetryState, w http.ResponseWriter, reqID string, path string) bool {
	if rs.isTimedOut() {
		if rs.lastResult != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(rs.lastResult.StatusCode)
			w.Write(rs.lastResult.Body)
		} else {
			h.sendError(w, 504, "timeout", "max total wait exceeded")
		}
		return true
	}
	return false
}

func (h *ProxyHandler) sendError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{"type": "error", "code": code, "message": message, "status": status},
	})
}

func (h *ProxyHandler) recordLog(reqID, channelID, clientModel, upstreamModel string, status int, latencyMs int64, key, errorCode, errorMsg string) {
	if h.db != nil {
		h.db.InsertLog(reqID, channelID, clientModel, upstreamModel, status, latencyMs, key, errorCode, errorMsg)
	}
}

func generateRequestID() string {
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}

// stripUTF8BOM removes a leading UTF-8 BOM (EF BB BF) from the byte slice.
// Some HTTP clients (notably PowerShell on Windows) prepend a BOM to request
// bodies, which causes strict JSON parsers in upstream APIs to reject the payload.
func stripUTF8BOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}
