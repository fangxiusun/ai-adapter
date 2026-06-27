package proxy

import (
	"github.com/fangxiusun/ai-adapter/internal/metrics"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

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

// ==================== Entry Points ====================

func (h *ProxyHandler) HandleChat(w http.ResponseWriter, r *http.Request) {
	metrics.ActiveRequests.Inc()
	defer metrics.ActiveRequests.Dec()

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
	metrics.ActiveRequests.Inc()
	defer metrics.ActiveRequests.Dec()

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
	metrics.ActiveRequests.Inc()
	defer metrics.ActiveRequests.Dec()

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
	metrics.ActiveRequests.Inc()
	defer metrics.ActiveRequests.Dec()

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










