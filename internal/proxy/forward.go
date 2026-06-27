package proxy

import (
	"bytes"
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

// ==================== Native Forwarding ====================

func (h *ProxyHandler) nativeForward(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, iface config.InterfaceType, body []byte, model string, stream bool, deepLog *debuglog.RequestLog) {
	// For Chat requests, inject stream_options.include_usage=true unless explicitly disabled.
	if iface == config.InterfaceChat {
		body = injectStreamOptions(body)
	}

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
		if processed := h.processRequestHeaders(ch, model, r.Header); processed != nil {
			applyProcessedHeaders(httpReq.Header, processed, "Content-Type", "Authorization")
		}
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

		var pt, ct, tt int
		var usageJSON string

		if stream {
			capture := newStreamUsageCapture(resp.Body)
			if processed := h.processResponseHeaders(ch, model, resp.Header); processed != nil {
				applyProcessedHeaders(w.Header(), processed, "Content-Type", "Cache-Control", "Connection")
			}
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, no-transform")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(200)
			io.Copy(w, capture)
			pt, ct, tt, usageJSON = capture.Usage()
		} else {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
			deepLog.LogUpstreamResponseHeader(resp.StatusCode, resp.Header)
			deepLog.LogUpstreamResponseBody(respBody)
			if processed := h.processResponseHeaders(ch, model, resp.Header); processed != nil {
				applyProcessedHeaders(w.Header(), processed, "Content-Type", "Cache-Control", "Connection")
			}
			deepLog.LogClientResponseHeader(resp.StatusCode, w.Header())
			deepLog.LogClientResponseBody(respBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			w.Write(respBody)
			pt, ct, tt, usageJSON = extractUsageFromRawBody(iface, respBody)
		}
		resp.Body.Close()
		ch.RecordLatency(key.Value, rs.elapsed().Milliseconds())
		ch.ReportSuccess(key.Value)
		h.recordLog(reqID, ch.Config.ID, model, model, 200, rs.elapsed().Milliseconds(), key.Value, "", "", pt, ct, tt, usageJSON)
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
		if processed := h.processRequestHeaders(ch, model, r.Header); processed != nil {
			applyProcessedHeaders(httpReq.Header, processed, "Content-Type", "Authorization")
		}
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
		result = &UpstreamResult{Body: respBody, StatusCode: resp.StatusCode, Headers: resp.Header, Key: key, LatencyMs: latency}
		break
	}
	deepLog.LogUpstreamResponseHeader(result.StatusCode, nil)
	deepLog.LogUpstreamResponseBody(result.Body)
	if processed := h.processResponseHeaders(ch, model, result.Headers); processed != nil {
		applyProcessedHeaders(w.Header(), processed, "Content-Type", "Cache-Control", "Connection")
	}
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
	pt, ct, tt, usageJSON := normalizeUsage(chatResp.Usage)
	h.recordLog(reqID, ch.Config.ID, model, model, 200, result.LatencyMs, result.Key.Value, "", "", pt, ct, tt, usageJSON)
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
