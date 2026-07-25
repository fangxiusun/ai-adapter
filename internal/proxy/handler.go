package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fangxiusun/ai-adapter/internal/metrics"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/db"
	"github.com/fangxiusun/ai-adapter/internal/debuglog"
	"github.com/fangxiusun/ai-adapter/internal/headerpolicy"
	"github.com/fangxiusun/ai-adapter/internal/log"
	"github.com/fangxiusun/ai-adapter/internal/stats"
	"github.com/fangxiusun/ai-adapter/internal/translate"
	"github.com/fangxiusun/ai-adapter/internal/util"
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
	requestLogs  sync.Map
}

type requestLogMeta struct {
	start         time.Time
	method        string
	path          string
	requestBytes  int
	responseBytes int
	clientModel   string
	channelID     string
	key           string
	upstreamModel string
}

// NewProxyHandler creates a new ProxyHandler.
func NewProxyHandler(channels *channel.ChannelManager, database *db.DB, logger *log.Logger, cfg *config.Config, deepDebug *debuglog.DeepDebugLogger, headerEngine *headerpolicy.Engine, statsInstance *stats.Stats, hub *websocket.Hub) *ProxyHandler {
	return &ProxyHandler{channels: channels, db: database, logger: logger, config: cfg, deepDebug: deepDebug, headerEngine: headerEngine, stats: statsInstance, wsHub: hub}
}

func (h *ProxyHandler) beginRequestLog(reqID string, r *http.Request) {
	h.requestLogs.Store(reqID, &requestLogMeta{start: time.Now(), method: r.Method, path: r.URL.Path})
	h.logger.RequestInfo(reqID, "接收请求", "method", r.Method, "path", r.URL.Path)
}

func (h *ProxyHandler) updateRequestLog(reqID string, update func(*requestLogMeta)) {
	if value, ok := h.requestLogs.Load(reqID); ok {
		update(value.(*requestLogMeta))
	}
}

func (h *ProxyHandler) setRequestBodyLog(reqID string, body []byte, model string) {
	h.updateRequestLog(reqID, func(meta *requestLogMeta) {
		meta.requestBytes = len(body)
		if model != "" {
			meta.clientModel = model
		}
	})
}

func (h *ProxyHandler) setRequestRouteLog(reqID, channelID, key, upstreamModel string) {
	h.updateRequestLog(reqID, func(meta *requestLogMeta) {
		if channelID != "" {
			meta.channelID = channelID
		}
		if key != "" {
			meta.key = key
		}
		if upstreamModel != "" && meta.upstreamModel == "" {
			meta.upstreamModel = upstreamModel
		}
	})
}

func (h *ProxyHandler) logChannelRequest(reqID string, ch *channel.Channel, key, url string, bodyBytes int) {
	h.setRequestRouteLog(reqID, ch.Config.ID, key, "")
	h.logger.RequestInfo(reqID, "请求子渠道",
		"channel_id", ch.Config.ID,
		"channel_key", util.MaskKey(key),
		"url", url,
		"request_bytes", bodyBytes,
	)
}

func (h *ProxyHandler) logChannelResponse(reqID string, ch *channel.Channel, key string, status int, responseBytes int, latencyMs int64) {
	if responseBytes >= 0 {
		h.updateRequestLog(reqID, func(meta *requestLogMeta) { meta.responseBytes = responseBytes })
	}
	h.logger.RequestInfo(reqID, "得到子渠道应答",
		"channel_id", ch.Config.ID,
		"channel_key", util.MaskKey(key),
		"status", status,
		"response_bytes", responseBytes,
		"latency_ms", latencyMs,
	)
}

func (h *ProxyHandler) finishRequestLog(reqID string, status int, fallbackLatencyMs int64) {
	value, ok := h.requestLogs.LoadAndDelete(reqID)
	if !ok {
		return
	}
	meta := value.(*requestLogMeta)
	latencyMs := time.Since(meta.start).Milliseconds()
	if latencyMs <= 0 {
		latencyMs = fallbackLatencyMs
	}
	h.logger.RequestInfo(reqID, "请求完成",
		"client_model", meta.clientModel,
		"channel_id", meta.channelID,
		"channel_key", util.MaskKey(meta.key),
		"upstream_model", meta.upstreamModel,
		"status", status,
		"request_bytes", meta.requestBytes,
		"response_bytes", meta.responseBytes,
		"latency_ms", latencyMs,
		"method", meta.method,
		"path", meta.path,
	)
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
		h.logger.RequestWarn(reqID, "请求体超过限制",
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

func (h *ProxyHandler) HandleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, "", 405, "method_not_allowed", "GET is required for /v1/models")
		return
	}

	now := time.Now().Unix()
	type modelItem struct {
		ID              string   `json:"id"`
		Name            string   `json:"name"`
		Object          string   `json:"object"`
		Created         int64    `json:"created"`
		OwnedBy         string   `json:"owned_by"`
		ContextLength   int      `json:"context_length"`
		MaxOutputLength int      `json:"max_output_length"`
		Aliases         []string `json:"aliases"`
	}

	seen := make(map[string]struct{})
	var items []modelItem
	for _, ch := range h.channels.ListChannels() {
		if !ch.Config.Enabled {
			continue
		}
		for _, m := range ch.Config.Models {
			id := m.ID
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}

			name := m.DisplayName
			if name == "" {
				name = id
			}
			contextWindow := m.ContextWindow
			if contextWindow <= 0 {
				contextWindow = 0
			}
			maxOutputTokens := m.MaxOutputTokens
			if maxOutputTokens <= 0 {
				maxOutputTokens = 0
			}

			aliases := m.Aliases
			if aliases == nil {
				aliases = []string{}
			}

			items = append(items, modelItem{
				ID:              id,
				Name:            name,
				Object:          "model",
				Created:         now,
				OwnedBy:         ch.Config.ID,
				ContextLength:   contextWindow,
				MaxOutputLength: maxOutputTokens,
				Aliases:         aliases,
			})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].OwnedBy == items[j].OwnedBy {
			return items[i].ID < items[j].ID
		}
		return items[i].OwnedBy < items[j].OwnedBy
	})

	if items == nil {
		items = make([]modelItem, 0)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"data":    items,
		"object":  "list",
		"success": true,
	}); err != nil {
		h.logger.Error("write_models_failed", "error", err)
	}
}

func (h *ProxyHandler) HandleChat(w http.ResponseWriter, r *http.Request) {
	metrics.IncActiveRequests()
	defer metrics.DecActiveRequests()

	reqID := generateRequestID()
	h.beginRequestLog(reqID, r)
	body, err := h.readRequestBody(w, reqID, r)
	if err != nil {
		h.sendError(w, reqID, 400, "read_body_failed", err.Error())
		return
	}
	body = stripUTF8BOM(body)
	h.setRequestBodyLog(reqID, body, "")
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
	h.setRequestBodyLog(reqID, body, req.Model)
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
	h.beginRequestLog(reqID, r)
	body, err := h.readRequestBody(w, reqID, r)
	if err != nil {
		h.sendError(w, reqID, 400, "read_body_failed", err.Error())
		return
	}
	body = stripUTF8BOM(body)
	h.setRequestBodyLog(reqID, body, "")
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
	h.setRequestBodyLog(reqID, body, req.Model)
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
	h.beginRequestLog(reqID, r)
	body, err := h.readRequestBody(w, reqID, r)
	if err != nil {
		h.sendError(w, reqID, 400, "read_body_failed", err.Error())
		return
	}
	body = stripUTF8BOM(body)
	h.setRequestBodyLog(reqID, body, "")
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
	h.setRequestBodyLog(reqID, body, req.Model)
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
	h.beginRequestLog(reqID, r)
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
	h.setRequestBodyLog(reqID, body, model)
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
		return handledError(http.StatusServiceUnavailable, "no conversion path")
	}
	upstreamModel := model
	if mi, ok := ch.ResolveModel(model); ok && mi.ID != "" {
		upstreamModel = mi.ID
	}
	h.setRequestRouteLog(reqID, ch.Config.ID, "", upstreamModel)

	h.logger.RequestDebug(reqID, "选择渠道", "channel_id", ch.Config.ID, "client_model", model, "upstream_model", upstreamModel, "target_api", target, "upstream_api", source, "native", source == target)
	if source == target {
		rawBody = replaceModelInBody(rawBody, model, upstreamModel)
		h.logger.RequestDebug(reqID, "构造渠道请求", "channel_id", ch.Config.ID, "client_model", model, "upstream_model", upstreamModel)
		return h.nativeForward(w, r, reqID, ch, source, rawBody, model, upstreamModel, stream, deepLog)
	}
	chatReq, err := h.buildChatRequest(target, targetReq, upstreamModel, stream)
	h.logger.RequestDebug(reqID, "转换渠道请求", "channel_id", ch.Config.ID, "client_model", model, "upstream_model", upstreamModel)
	if err != nil {
		h.sendError(w, reqID, 400, "convert_failed", err.Error())
		return handledError(http.StatusBadRequest, err.Error())
	}
	if stream {
		return h.convertedStreamForward(w, r, reqID, ch, source, target, chatReq, upstreamModel, targetReq, deepLog)
	}
	return h.convertedNonStreamForward(w, r, reqID, ch, source, target, chatReq, upstreamModel, targetReq, deepLog)
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

		if failErr := h.dispatch(w, r, reqID, ch, target, clientModel, stream, rawBody, targetReq, deepLog); failErr != nil && !failErr.Handled {
			h.sendError(w, reqID, failErr.StatusCode, "channel_dispatch_failed", failErr.Message)
		}
		return
	}

	// Reorder candidates based on load balance strategy (round-robin/random/priority)
	candidates = h.channels.ReorderCandidates(candidates)
	deadline := time.Now().Add(time.Duration(fc.TotalTimeoutMs) * time.Millisecond)
	failoverCtx, cancelFailover := context.WithDeadline(r.Context(), deadline)
	defer cancelFailover()
	r = r.WithContext(failoverCtx)
	tried := 0
	var lastErr *FailoverError

	for _, ch := range candidates {
		if tried >= fc.MaxChannelAttempts {
			break
		}
		if time.Now().After(deadline) {
			h.logger.RequestWarn(reqID, "渠道故障转移超时", "tried", tried)
			break
		}
		if !ch.IsHealthy() {
			h.logger.RequestDebug(reqID, "跳过不健康渠道", "channel_id", ch.Config.ID)
			continue
		}

		h.logger.RequestDebug(reqID, "尝试渠道", "channel_id", ch.Config.ID, "attempt", tried+1)
		failErr := h.dispatch(w, r, reqID, ch, target, clientModel, stream, rawBody, targetReq, deepLog)

		if failErr == nil {
			ch.ReportChannelSuccess()
			return
		}
		if failErr.Handled {
			return
		}

		// Only upstream 5xx and connection failures affect channel health.
		if failErr.AffectsChannelHealth {
			ch.ReportChannelFailure()
		}
		lastErr = failErr
		tried++
		h.logger.RequestWarn(reqID, "切换下一渠道",
			"channel_id", ch.Config.ID, "reason", failErr.Message, "tried", tried)
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
		return translate.ReqToChat(targetReq.(*translate.ResponsesRequest), translate.TranslateOpts{ForceParallelTools: true}, model)
	case config.InterfaceMessages:
		return translate.ClaudeToChatRequest(targetReq.(*translate.ClaudeRequest), model)
	case config.InterfaceGenerateContent:
		req, err := translate.GeminiToChatRequest(targetReq.(*translate.GeminiRequest), model)
		if req != nil {
			req.Model = model
			req.Stream = stream
		}
		return req, err
	default:
		return nil, fmt.Errorf("unsupported target: %s", target)
	}
}
