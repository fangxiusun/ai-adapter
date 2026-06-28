package proxy

import (
	"github.com/fangxiusun/ai-adapter/internal/metrics"
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
	"github.com/fangxiusun/ai-adapter/internal/headerpolicy"
	"github.com/fangxiusun/ai-adapter/internal/log"
	"github.com/fangxiusun/ai-adapter/internal/stats"
	"github.com/fangxiusun/ai-adapter/internal/translate"
	"github.com/fangxiusun/ai-adapter/internal/websocket"
)

// ProxyHandler handles incoming API requests and dispatches them to upstream services.
type ProxyHandler struct {
	channels     *channel.ChannelManager
	db           *db.DB
	logger       *log.Logger
	config       *config.Config
	deepDebug    *debuglog.DeepDebugLogger
	headerEngine *headerpolicy.Engine
	stats        *stats.Stats
	wsHub        *websocket.Hub
}

// NewProxyHandler creates a new ProxyHandler.
func NewProxyHandler(channels *channel.ChannelManager, database *db.DB, logger *log.Logger, cfg *config.Config, deepDebug *debuglog.DeepDebugLogger, headerEngine *headerpolicy.Engine, statsInstance *stats.Stats, hub *websocket.Hub) *ProxyHandler {
	return &ProxyHandler{channels: channels, db: database, logger: logger, config: cfg, deepDebug: deepDebug, headerEngine: headerEngine, stats: statsInstance, wsHub: hub}
}

// maxRequestBodyBytes returns the maximum allowed request body size in bytes.
func (h *ProxyHandler) maxRequestBodyBytes() int64 {
	mb := h.config.Server.MaxRequestBodySizeMB
	if mb <= 0 {
		mb = 64 // default 64MB
	}
	return int64(mb) * 1024 * 1024
}

// maxResponseBodyBytes returns the maximum allowed upstream response body size in bytes.
// Uses the same config as request body size.
func (h *ProxyHandler) maxResponseBodyBytes() int64 {
	return h.maxRequestBodyBytes()
}

// readRequestBody reads the request body with size limit and logs truncation warnings.
func (h *ProxyHandler) readRequestBody(w http.ResponseWriter, reqID string, r *http.Request) ([]byte, error) {
	maxSize := h.maxRequestBodyBytes()
	// Use LimitReader to enforce max size (+1 to detect truncation)
	limitedReader := io.LimitReader(r.Body, maxSize+1)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}

	// Check if body was truncated
	if int64(len(body)) > maxSize {
		h.logger.Warn("request_body_truncated",
			"request_id", reqID,
			"original_size_hint", fmt.Sprintf(">%dMB", maxSize/1024/1024),
			"truncated_size", len(body),
			"max_allowed", maxSize,
		)
		// Return truncated body (first maxSize bytes)
		return body[:maxSize], nil
	}

	return body, nil
}

// ==================== Entry Points ====================

func (h *ProxyHandler) HandleChat(w http.ResponseWriter, r *http.Request) {
	metrics.IncActiveRequests()
	defer metrics.DecActiveRequests()

	reqID := generateRequestID()
	h.logger.Debug("incoming request", "request_id", reqID, "path", "/v1/chat/completions", "target", "chat")
	body, err := h.readRequestBody(w, reqID, r)
	if err != nil {
		h.sendError(w, reqID, 400, "read_body_failed", err.Error())
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
		h.sendError(w, reqID, 400, "invalid_json", err.Error())
		return
	}
	if req.Model == "" {
		h.sendError(w, reqID, 400, "missing_model", "model is required")
		return
	}
	candidates := h.channels.SelectChannelCandidates(req.Model)
	if len(candidates) == 0 {
		h.sendError(w, reqID, 404, "no_channel", "no channel found for model: "+req.Model)
		return
	}
	h.failoverLoop(w, r, reqID, candidates, config.InterfaceChat, req.Model, req.Stream, body, &req, deepLog)
}

func (h *ProxyHandler) HandleResponses(w http.ResponseWriter, r *http.Request) {
	metrics.IncActiveRequests()
	defer metrics.DecActiveRequests()

	reqID := generateRequestID()
	h.logger.Debug("incoming request", "request_id", reqID, "path", "/v1/responses", "target", "responses")
	body, err := h.readRequestBody(w, reqID, r)
	if err != nil {
		h.sendError(w, reqID, 400, "read_body_failed", err.Error())
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
		h.sendError(w, reqID, 400, "invalid_json", err.Error())
		return
	}
	if req.Model == "" {
		h.sendError(w, reqID, 400, "missing_model", "model is required")
		return
	}
	candidates := h.channels.SelectChannelCandidates(req.Model)
	if len(candidates) == 0 {
		h.sendError(w, reqID, 404, "no_channel", "no channel found for model: "+req.Model)
		return
	}
	h.failoverLoop(w, r, reqID, candidates, config.InterfaceResponses, req.Model, req.Stream, body, &req, deepLog)
}

func (h *ProxyHandler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	metrics.IncActiveRequests()
	defer metrics.DecActiveRequests()

	reqID := generateRequestID()
	h.logger.Debug("incoming request", "request_id", reqID, "path", "/v1/messages", "target", "messages")
	body, err := h.readRequestBody(w, reqID, r)
	if err != nil {
		h.sendError(w, reqID, 400, "read_body_failed", err.Error())
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
		h.sendError(w, reqID, 400, "invalid_json", err.Error())
		return
	}
	if req.Model == "" {
		h.sendError(w, reqID, 400, "missing_model", "model is required")
		return
	}
	candidates := h.channels.SelectChannelCandidates(req.Model)
	if len(candidates) == 0 {
		h.sendError(w, reqID, 404, "no_channel", "no channel found for model: "+req.Model)
		return
	}
	h.failoverLoop(w, r, reqID, candidates, config.InterfaceMessages, req.Model, req.Stream, body, &req, deepLog)
}

func (h *ProxyHandler) HandleGenerateContent(w http.ResponseWriter, r *http.Request) {
	metrics.IncActiveRequests()
	defer metrics.DecActiveRequests()

	reqID := generateRequestID()
	h.logger.Debug("incoming request", "request_id", reqID, "path", "/v1beta/models/*:generateContent", "target", "generateContent")
	model := extractGeminiModel(r.URL.Path)
	if model == "" {
		h.sendError(w, reqID, 400, "missing_model", "could not extract model from URL path")
		return
	}
	body, err := h.readRequestBody(w, reqID, r)
	if err != nil {
		h.sendError(w, reqID, 400, "read_body_failed", err.Error())
		return
	}
	body = stripUTF8BOM(body)
	h.logger.LogRequestBody(reqID, body)
	h.logger.LogClientInput(reqID, body)
	deepLog := h.deepDebug.BeginRequest(reqID, r.Method, r.URL.Path)
	deepLog.LogClientRequestHeader(r)
	deepLog.LogClientRequestBody(body)
	defer deepLog.Close()
	stream := strings.Contains(r.URL.Path, "streamGenerateContent")
	var req translate.GeminiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		h.sendError(w, reqID, 400, "invalid_json", err.Error())
		return
	}
	candidates := h.channels.SelectChannelCandidates(model)
	if len(candidates) == 0 {
		h.sendError(w, reqID, 404, "no_channel", "no channel found for model: "+model)
		return
	}
	h.failoverLoop(w, r, reqID, candidates, config.InterfaceGenerateContent, model, stream, body, &req, deepLog)
}

// ==================== Core Dispatch ====================

func (h *ProxyHandler) dispatch(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, target config.InterfaceType, model string, stream bool, rawBody []byte, targetReq interface{}, deepLog *debuglog.RequestLog) *FailoverError {
	source, ok := config.BestSourceForTarget(target, &ch.Config)
	if !ok {
		h.sendError(w, reqID, 503, "no_conversion_path",
			fmt.Sprintf("channel %s has no native interface and no conversion path to %s", ch.Config.ID, target))
		return nil
	}
	h.logger.Debug("dispatch", "request_id", reqID, "target", target, "source", source, "native", source == target)
	if source == target {
		return h.nativeForward(w, r, reqID, ch, source, rawBody, model, stream, deepLog)
	}
	chatReq, err := h.buildChatRequest(target, targetReq, model, stream)
	if err != nil {
		h.sendError(w, reqID, 400, "convert_failed", err.Error())
		return nil
	}
	if stream {
		return h.convertedStreamForward(w, r, reqID, ch, source, target, chatReq, model, targetReq, deepLog)
	}
	return h.convertedNonStreamForward(w, r, reqID, ch, source, target, chatReq, model, targetReq, deepLog)
}



// failoverLoop tries dispatching to each candidate channel in order.
// On failoverable errors, it moves to the next channel.
// On success or non-failoverable errors, it returns immediately.
func (h *ProxyHandler) failoverLoop(w http.ResponseWriter, r *http.Request, reqID string,
	candidates []*channel.Channel, target config.InterfaceType, clientModel string,
	stream bool, rawBody []byte, targetReq interface{}, deepLog *debuglog.RequestLog) {

	fc := h.config.Failover
	if !fc.Enabled || len(candidates) <= 1 {
		// No failover — use balanced selection
		ch := h.channels.SelectBalanced(candidates)
		upstreamModel := clientModel
		if mi, ok := ch.ResolveModel(clientModel); ok && mi.ID != "" {
			upstreamModel = mi.ID
		}
		rawBody = replaceModelInBody(rawBody, clientModel, upstreamModel)
		h.dispatch(w, r, reqID, ch, target, upstreamModel, stream, rawBody, targetReq, deepLog)
		return
	}

	// Reorder candidates based on load balance strategy (round-robin/random/priority)
	candidates = h.channels.ReorderCandidates(candidates)
	deadline := time.Now().Add(time.Duration(fc.TotalTimeoutMs) * time.Millisecond)
	tried := 0
	var lastErr *FailoverError

	for _, ch := range candidates {
		if tried >= fc.MaxChannelAttempts {
			break
		}
		if time.Now().After(deadline) {
			h.logger.Warn("failover_timeout", "request_id", reqID, "tried", tried)
			break
		}
		if !ch.IsHealthy() {
			h.logger.Debug("failover_skip_unhealthy", "request_id", reqID, "channel", ch.Config.ID)
			continue
		}

		upstreamModel := clientModel
		if mi, ok := ch.ResolveModel(clientModel); ok && mi.ID != "" {
			upstreamModel = mi.ID
		}
		dispatchBody := replaceModelInBody(rawBody, clientModel, upstreamModel)

		h.logger.Debug("failover_attempt", "request_id", reqID, "channel", ch.Config.ID, "attempt", tried+1)
		failErr := h.dispatch(w, r, reqID, ch, target, upstreamModel, stream, dispatchBody, targetReq, deepLog)

		if failErr == nil {
			// Success or non-failoverable error already handled
			ch.ReportChannelSuccess()
			return
		}

		// Failoverable error — report to health tracker and try next channel
		ch.ReportChannelFailure()
		lastErr = failErr
		tried++
		h.logger.Warn("failover_next", "request_id", reqID,
			"failed_channel", ch.Config.ID, "reason", failErr.Message, "tried", tried)
	}

	// All channels failed
	if lastErr != nil {
		h.sendError(w, reqID, lastErr.StatusCode, "all_channels_failed",
			fmt.Sprintf("all %d channels failed, last error: %s", tried, lastErr.Message))
	} else {
		h.sendError(w, reqID, 503, "no_healthy_channel", "no healthy channels available")
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
