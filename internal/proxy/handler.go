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
	"github.com/fangxiusun/ai-adapter/internal/log"
	"github.com/fangxiusun/ai-adapter/internal/translate"
)

type ProxyHandler struct {
	channels *channel.ChannelManager
	db       *db.DB
	logger   *log.Logger
	config   *config.Config
}

func NewProxyHandler(channels *channel.ChannelManager, database *db.DB, logger *log.Logger, cfg *config.Config) *ProxyHandler {
	return &ProxyHandler{
		channels: channels,
		db:       database,
		logger:   logger,
		config:   cfg,
	}
}

type UpstreamResult struct {
	Body       []byte
	StatusCode int
	Key        *channel.KeyEntry
	LatencyMs  int64
	Error      error
}

type RetryState struct {
	start          time.Time
	excluded       map[string]bool
	rotationRound  int
	maxRounds      int
	retryDelay     time.Duration
	maxTotalWait   time.Duration
	lastResult     *UpstreamResult
	lastErr        error
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

func (h *ProxyHandler) HandleResponses(w http.ResponseWriter, r *http.Request) {
	reqID := generateRequestID()
	h.logger.Debug("incoming request", "request_id", reqID, "path", "/v1/responses")

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024*1024))
	if err != nil {
		h.sendError(w, 400, "read_body_failed", err.Error())
		return
	}
	h.logger.LogRequestBody(reqID, body)
	h.logger.LogClientInput(reqID, body)

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

	if ch.WireAPI() == "chat" {
		h.handleResponsesToChat(w, r, reqID, ch, &req, upstreamModel)
	} else {
		h.handleResponsesPassthrough(w, r, reqID, ch, &req, upstreamModel)
	}
}

func (h *ProxyHandler) HandleChat(w http.ResponseWriter, r *http.Request) {
	reqID := generateRequestID()
	h.logger.Debug("incoming request", "request_id", reqID, "path", "/v1/chat/completions")

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024*1024))
	if err != nil {
		h.sendError(w, 400, "read_body_failed", err.Error())
		return
	}
	h.logger.LogRequestBody(reqID, body)
	h.logger.LogClientInput(reqID, body)

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

	if ch.WireAPI() == "responses" {
		h.handleChatToResponses(w, r, reqID, ch, &req, upstreamModel)
	} else {
		h.handleChatPassthrough(w, r, reqID, ch, &req, upstreamModel)
	}
}

func (h *ProxyHandler) handleResponsesToChat(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, req *translate.ResponsesRequest, upstreamModel string) {
	opts := translate.TranslateOpts{
		ForceParallelTools: true,
		EnableWebSearch:    ch.Config.WebSearch.Enabled,
	}
	chatReq, err := translate.ReqToChat(req, opts)
	if err != nil {
		h.sendError(w, 400, "translate_failed", err.Error())
		return
	}
	chatReq.Model = upstreamModel
	h.logger.LogTranslate("responses->chat", len(chatReq.Tools), 0)

	if req.Stream {
		h.handleStreamingWithRetry(w, r, reqID, ch, chatReq, req)
	} else {
		h.handleNonStreamingWithRetry(w, r, reqID, ch, chatReq, req)
	}
}

func (h *ProxyHandler) handleChatToResponses(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, req *translate.ChatRequest, upstreamModel string) {
	opts := translate.TranslateOpts{}
	respReq, err := translate.ReqToResponses(req, opts)
	if err != nil {
		h.sendError(w, 400, "translate_failed", err.Error())
		return
	}
	respReq.Model = upstreamModel
	h.logger.LogTranslate("chat->responses", len(respReq.Tools), 0)

	if req.Stream {
		h.handleStreamingResponsesWithRetry(w, r, reqID, ch, respReq, req)
	} else {
		h.handleNonStreamingResponsesWithRetry(w, r, reqID, ch, respReq, req)
	}
}

func (h *ProxyHandler) handleResponsesPassthrough(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, req *translate.ResponsesRequest, upstreamModel string) {
	req.Model = upstreamModel
	h.logger.LogTranslate("responses->responses (passthrough)", 0, 0)
	if req.Stream {
		h.handleStreamingResponsesPassthroughWithRetry(w, r, reqID, ch, req)
	} else {
		h.handleNonStreamingResponsesPassthroughWithRetry(w, r, reqID, ch, req)
	}
}

func (h *ProxyHandler) handleChatPassthrough(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, req *translate.ChatRequest, upstreamModel string) {
	req.Model = upstreamModel
	h.logger.LogTranslate("chat->chat (passthrough)", 0, 0)
	if req.Stream {
		h.handleStreamingChatPassthroughWithRetry(w, r, reqID, ch, req)
	} else {
		h.handleNonStreamingChatPassthroughWithRetry(w, r, reqID, ch, req)
	}
}

// ==================== Core retry logic ====================

func (h *ProxyHandler) getNextKey(ch *channel.Channel, rs *RetryState) *channel.KeyEntry {
	key := ch.GetKey()
	if key == nil {
		return nil
	}
	if rs.excluded[key.Value] {
		key = ch.KeyPool().NextExcluding(rs.excluded)
	}
	return key
}

func (h *ProxyHandler) handleRetryResult(ch *channel.Channel, rs *RetryState, key *channel.KeyEntry, result *UpstreamResult) (done bool) {
	if result.Error != nil {
		ch.ReportError(key.Value, 0)
		rs.lastErr = result.Error
		h.logger.Warn("upstream request failed", "error", result.Error)
		return false
	}

	if result.StatusCode == 401 {
		ch.ReportError(key.Value, 401)
		rs.excluded[key.Value] = true
		h.logger.Warn("key permanently skipped (401)", "key_name", key.Name, "key_value", key.Value)
		return false
	}

	if result.StatusCode == 429 {
		ch.ReportError(key.Value, 429)
		rs.excluded[key.Value] = true
		rs.lastResult = result
		h.logger.Warn("key rate limited (429)", "key_name", key.Name, "delay", rs.retryDelay)
		time.Sleep(rs.retryDelay)
		return false
	}

	if result.StatusCode >= 500 {
		ch.ReportError(key.Value, result.StatusCode)
		rs.excluded[key.Value] = true
		rs.lastErr = fmt.Errorf("upstream error %d", result.StatusCode)
		rs.lastResult = result
		h.logger.Warn("upstream server error", "status", result.StatusCode)
		return false
	}

	ch.ReportSuccess(key.Value)
	rs.lastResult = result
	return true
}

func (h *ProxyHandler) checkRotationAndTimeout(ch *channel.Channel, rs *RetryState, w http.ResponseWriter, reqID string, endpoint string) (shouldStop bool) {
	allExcluded := true
	for _, k := range ch.KeyPool().GetStats() {
		if !rs.excluded[k.Name] {
			allExcluded = false
			break
		}
	}

	if allExcluded {
		rs.rotationRound++
		if rs.rotationRound > rs.maxRounds {
			h.sendError(w, 502, "all_retries_failed", fmt.Sprintf("all %d rotation rounds exhausted", rs.maxRounds))
			h.recordLog(reqID, ch.Config.ID, "", "", 502, rs.elapsed().Milliseconds(), "", "all_retries_failed", "rotation rounds exhausted")
			return true
		}
		rs.excluded = make(map[string]bool)
		h.logger.Warn("all keys excluded, starting new rotation round", "round", rs.rotationRound, "max_rounds", rs.maxRounds)
	}

	if rs.isTimedOut() {
		h.sendError(w, 502, "timeout", fmt.Sprintf("max total wait time %v exceeded", rs.maxTotalWait))
		h.recordLog(reqID, ch.Config.ID, "", "", 502, rs.elapsed().Milliseconds(), "", "timeout", "max total wait exceeded")
		return true
	}

	return false
}

// ==================== Non-streaming handlers ====================

func (h *ProxyHandler) handleNonStreamingWithRetry(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, chatReq *translate.ChatRequest, origReq *translate.ResponsesRequest) {
	rs := newRetryState(ch)
	cw := newCapturingWriter(w, reqID, h.logger)

	for {
		if h.checkRotationAndTimeout(ch, rs, cw, reqID, "/v1/responses") {
			return
		}

		key := h.getNextKey(ch, rs)
		if key == nil {
			h.sendError(cw, 503, "no_available_keys", "all keys are unavailable")
			return
		}

		result := h.sendToUpstreamWithStatus(r.Context(), ch, key, "/chat/completions", chatReq)
		if h.handleRetryResult(ch, rs, key, result) {
			var chatResp translate.ChatResponse
			if err := json.Unmarshal(result.Body, &chatResp); err != nil {
				h.sendError(cw, 502, "invalid_upstream_response", err.Error())
				return
			}
			resp := translate.RespToResponses(&chatResp, origReq, translate.TranslateOpts{})
			respBytes, _ := json.Marshal(resp)
			cw.Header().Set("Content-Type", "application/json")
			cw.Write(respBytes)
			h.recordLog(reqID, ch.Config.ID, origReq.Model, chatReq.Model, 200, rs.elapsed().Milliseconds(), key.Value, "", "")
			h.logger.LogRequest(reqID, "POST", "/v1/responses", 200, rs.elapsed().Milliseconds(), key.Value, ch.Config.ID, chatReq.Model)
			return
		}
	}
}

func (h *ProxyHandler) handleNonStreamingResponsesWithRetry(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, respReq *translate.ResponsesRequest, origReq *translate.ChatRequest) {
	rs := newRetryState(ch)
	cw := newCapturingWriter(w, reqID, h.logger)

	for {
		if h.checkRotationAndTimeout(ch, rs, cw, reqID, "/v1/chat/completions") {
			return
		}

		key := h.getNextKey(ch, rs)
		if key == nil {
			h.sendError(cw, 503, "no_available_keys", "all keys are unavailable")
			return
		}

		result := h.sendToUpstreamWithStatus(r.Context(), ch, key, "/responses", respReq)
		if h.handleRetryResult(ch, rs, key, result) {
			var responsesResp translate.ResponsesObject
			if err := json.Unmarshal(result.Body, &responsesResp); err != nil {
				h.sendError(cw, 502, "invalid_upstream_response", err.Error())
				return
			}
			chatResp := translate.RespToChat(&responsesResp, origReq, translate.TranslateOpts{})
			respBytes, _ := json.Marshal(chatResp)
			cw.Header().Set("Content-Type", "application/json")
			cw.Write(respBytes)
			h.recordLog(reqID, ch.Config.ID, origReq.Model, respReq.Model, 200, rs.elapsed().Milliseconds(), key.Value, "", "")
			h.logger.LogRequest(reqID, "POST", "/v1/chat/completions", 200, rs.elapsed().Milliseconds(), key.Value, ch.Config.ID, respReq.Model)
			return
		}
	}
}

func (h *ProxyHandler) handleNonStreamingResponsesPassthroughWithRetry(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, req *translate.ResponsesRequest) {
	rs := newRetryState(ch)
	cw := newCapturingWriter(w, reqID, h.logger)

	for {
		if h.checkRotationAndTimeout(ch, rs, cw, reqID, "/v1/responses") {
			return
		}

		key := h.getNextKey(ch, rs)
		if key == nil {
			h.sendError(cw, 503, "no_available_keys", "all keys are unavailable")
			return
		}

		result := h.sendToUpstreamWithStatus(r.Context(), ch, key, "/responses", req)
		if h.handleRetryResult(ch, rs, key, result) {
			cw.Header().Set("Content-Type", "application/json")
			cw.Write(result.Body)
			h.recordLog(reqID, ch.Config.ID, req.Model, req.Model, 200, rs.elapsed().Milliseconds(), key.Value, "", "")
			h.logger.LogRequest(reqID, "POST", "/v1/responses", 200, rs.elapsed().Milliseconds(), key.Value, ch.Config.ID, req.Model)
			return
		}
	}
}

func (h *ProxyHandler) handleNonStreamingChatPassthroughWithRetry(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, req *translate.ChatRequest) {
	rs := newRetryState(ch)
	cw := newCapturingWriter(w, reqID, h.logger)

	for {
		if h.checkRotationAndTimeout(ch, rs, cw, reqID, "/v1/chat/completions") {
			return
		}

		key := h.getNextKey(ch, rs)
		if key == nil {
			h.sendError(cw, 503, "no_available_keys", "all keys are unavailable")
			return
		}

		result := h.sendToUpstreamWithStatus(r.Context(), ch, key, "/chat/completions", req)
		if h.handleRetryResult(ch, rs, key, result) {
			cw.Header().Set("Content-Type", "application/json")
			cw.Write(result.Body)
			h.recordLog(reqID, ch.Config.ID, req.Model, req.Model, 200, rs.elapsed().Milliseconds(), key.Value, "", "")
			h.logger.LogRequest(reqID, "POST", "/v1/chat/completions", 200, rs.elapsed().Milliseconds(), key.Value, ch.Config.ID, req.Model)
			return
		}
	}
}

// ==================== Streaming handlers ====================

func (h *ProxyHandler) handleStreamingWithRetry(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, chatReq *translate.ChatRequest, origReq *translate.ResponsesRequest) {
	rs := newRetryState(ch)

	for {
		if h.checkRotationAndTimeout(ch, rs, w, reqID, "/v1/responses") {
			return
		}

		key := h.getNextKey(ch, rs)
		if key == nil {
			h.sendError(w, 503, "no_available_keys", "all keys are unavailable")
			return
		}

		url := ch.UpstreamBaseURL() + "/chat/completions"
		body, _ := json.Marshal(chatReq)
		httpReq, err := http.NewRequestWithContext(r.Context(), "POST", url, bytes.NewReader(body))
		if err != nil {
			h.sendError(w, 500, "create_request_failed", err.Error())
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+key.Value)

		resp, err := ch.HTTPClient().Do(httpReq)
		if err != nil {
			ch.ReportError(key.Value, 0)
			rs.excluded[key.Value] = true
			h.logger.Warn("stream upstream request failed", "error", err)
			continue
		}

		if resp.StatusCode == 401 {
			resp.Body.Close()
			ch.ReportError(key.Value, 401)
			rs.excluded[key.Value] = true
			h.logger.Warn("stream key skipped (401)", "key_name", key.Name, "key_value", key.Value)
			continue
		}

		if resp.StatusCode == 429 {
			resp.Body.Close()
			ch.ReportError(key.Value, 429)
			rs.excluded[key.Value] = true
			h.logger.Warn("stream key rate limited (429)", "key_name", key.Name, "delay", rs.retryDelay)
			time.Sleep(rs.retryDelay)
			continue
		}

		if resp.StatusCode >= 500 {
			resp.Body.Close()
			ch.ReportError(key.Value, resp.StatusCode)
			rs.excluded[key.Value] = true
			h.logger.Warn("stream upstream server error", "status", resp.StatusCode)
			continue
		}

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(200)

		flusher, canFlush := w.(http.Flusher)
		keepalive := time.NewTicker(15 * time.Second)
		defer keepalive.Stop()
		go func() {
			for range keepalive.C {
				if canFlush {
					fmt.Fprintf(w, ": keepalive\n\n")
					flusher.Flush()
				}
			}
		}()

		translator := translate.NewStreamTranslator(w, origReq, translate.TranslateOpts{}, func() {
			if canFlush {
				flusher.Flush()
			}
		})
		translator.Start()

		scanner := newChunkScanner(resp.Body)
		for scanner.Next() {
			chunk := scanner.Chunk()
			translator.ProcessChunk(chunk)
		}
		resp.Body.Close()

		translator.Finish()
		ch.ReportSuccess(key.Value)
		h.recordLog(reqID, ch.Config.ID, origReq.Model, chatReq.Model, 200, rs.elapsed().Milliseconds(), key.Value, "", "")
		h.logger.LogRequest(reqID, "POST", "/v1/responses", 200, rs.elapsed().Milliseconds(), key.Value, ch.Config.ID, chatReq.Model)
		return
	}
}

func (h *ProxyHandler) handleStreamingResponsesWithRetry(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, respReq *translate.ResponsesRequest, origReq *translate.ChatRequest) {
	rs := newRetryState(ch)

	for {
		if h.checkRotationAndTimeout(ch, rs, w, reqID, "/v1/chat/completions") {
			return
		}

		key := h.getNextKey(ch, rs)
		if key == nil {
			h.sendError(w, 503, "no_available_keys", "all keys are unavailable")
			return
		}

		url := ch.UpstreamBaseURL() + "/responses"
		body, _ := json.Marshal(respReq)
		httpReq, err := http.NewRequestWithContext(r.Context(), "POST", url, bytes.NewReader(body))
		if err != nil {
			h.sendError(w, 500, "create_request_failed", err.Error())
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+key.Value)

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

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(200)

		flusher, canFlush := w.(http.Flusher)
		keepalive := time.NewTicker(15 * time.Second)
		defer keepalive.Stop()
		go func() {
			for range keepalive.C {
				if canFlush {
					fmt.Fprintf(w, ": keepalive\n\n")
					flusher.Flush()
				}
			}
		}()

		_, err = translate.PipeResponsesStreamToChat(r.Context(), resp.Body, w, origReq, translate.TranslateOpts{})
		resp.Body.Close()

		ch.ReportSuccess(key.Value)
		h.recordLog(reqID, ch.Config.ID, origReq.Model, respReq.Model, 200, rs.elapsed().Milliseconds(), key.Value, "", "")
		h.logger.LogRequest(reqID, "POST", "/v1/chat/completions", 200, rs.elapsed().Milliseconds(), key.Value, ch.Config.ID, respReq.Model)
		return
	}
}

func (h *ProxyHandler) handleStreamingResponsesPassthroughWithRetry(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, req *translate.ResponsesRequest) {
	rs := newRetryState(ch)

	for {
		if h.checkRotationAndTimeout(ch, rs, w, reqID, "/v1/responses") {
			return
		}

		key := h.getNextKey(ch, rs)
		if key == nil {
			h.sendError(w, 503, "no_available_keys", "all keys are unavailable")
			return
		}

		url := ch.UpstreamBaseURL() + "/responses"
		body, _ := json.Marshal(req)
		httpReq, err := http.NewRequestWithContext(r.Context(), "POST", url, bytes.NewReader(body))
		if err != nil {
			h.sendError(w, 500, "create_request_failed", err.Error())
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+key.Value)

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

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(200)

		io.Copy(w, resp.Body)
		resp.Body.Close()
		ch.ReportSuccess(key.Value)
		h.recordLog(reqID, ch.Config.ID, req.Model, req.Model, 200, rs.elapsed().Milliseconds(), key.Value, "", "")
		h.logger.LogRequest(reqID, "POST", "/v1/responses", 200, rs.elapsed().Milliseconds(), key.Value, ch.Config.ID, req.Model)
		return
	}
}

func (h *ProxyHandler) handleStreamingChatPassthroughWithRetry(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, req *translate.ChatRequest) {
	rs := newRetryState(ch)

	for {
		if h.checkRotationAndTimeout(ch, rs, w, reqID, "/v1/chat/completions") {
			return
		}

		key := h.getNextKey(ch, rs)
		if key == nil {
			h.sendError(w, 503, "no_available_keys", "all keys are unavailable")
			return
		}

		url := ch.UpstreamBaseURL() + "/chat/completions"
		body, _ := json.Marshal(req)
		httpReq, err := http.NewRequestWithContext(r.Context(), "POST", url, bytes.NewReader(body))
		if err != nil {
			h.sendError(w, 500, "create_request_failed", err.Error())
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+key.Value)

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

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(200)

		io.Copy(w, resp.Body)
		resp.Body.Close()
		ch.ReportSuccess(key.Value)
		h.recordLog(reqID, ch.Config.ID, req.Model, req.Model, 200, rs.elapsed().Milliseconds(), key.Value, "", "")
		h.logger.LogRequest(reqID, "POST", "/v1/chat/completions", 200, rs.elapsed().Milliseconds(), key.Value, ch.Config.ID, req.Model)
		return
	}
}

// ==================== Core upstream call ====================

func (h *ProxyHandler) sendToUpstreamWithStatus(ctx context.Context, ch *channel.Channel, key *channel.KeyEntry, path string, body interface{}) *UpstreamResult {
	data, err := json.Marshal(body)
	if err != nil {
		return &UpstreamResult{Error: fmt.Errorf("marshal body: %w", err)}
	}

	url := ch.UpstreamBaseURL() + path
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return &UpstreamResult{Error: fmt.Errorf("create request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)

	start := time.Now()
	resp, err := ch.HTTPClient().Do(req)
	if err != nil {
		return &UpstreamResult{Error: fmt.Errorf("upstream request: %w", err)}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
	if err != nil {
		return &UpstreamResult{Error: fmt.Errorf("read response: %w", err)}
	}

	latency := time.Since(start).Milliseconds()
	key.State.RecordLatency(latency)

	return &UpstreamResult{
		Body:       respBody,
		StatusCode: resp.StatusCode,
		Key:        key,
		LatencyMs:  latency,
	}
}

func (h *ProxyHandler) sendError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"type":    "error",
			"code":    code,
			"message": message,
			"status":  status,
		},
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

type chunkScanner struct {
	reader *bufio.Reader
	err    error
	chunk  *translate.ChatStreamChunk
}

func newChunkScanner(r io.Reader) *chunkScanner {
	return &chunkScanner{reader: bufio.NewReader(r)}
}

func (s *chunkScanner) Next() bool {
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			s.err = err
			return false
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return false
		}
		var chunk translate.ChatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		s.chunk = &chunk
		return true
	}
}

func (s *chunkScanner) Chunk() *translate.ChatStreamChunk {
	return s.chunk
}

