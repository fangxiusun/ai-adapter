package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/debuglog"
	"github.com/fangxiusun/ai-adapter/internal/translate"
	"github.com/fangxiusun/ai-adapter/internal/util"
)

// ==================== Fanout Forwarding ====================

// fanoutForward sends the same request to multiple keys concurrently and returns
// the first successful (or fastest) response. Only for non-streaming requests.
func (h *ProxyHandler) fanoutForward(w http.ResponseWriter, r *http.Request, reqID string,
	ch *channel.Channel, iface config.InterfaceType, body []byte, model string, deepLog *debuglog.RequestLog) *FailoverError {

	path := upstreamPathForInterface(iface, model, false)
	url := ch.Config.NativeBaseURL(iface) + path

	// Build headers for the fanout request.
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	if processed := h.processRequestHeaders(ch, model, r.Header); processed != nil {
		applyProcessedHeaders(headers, processed, "Content-Type", "Authorization")
	}

	deepLog.LogUpstreamRequestHeader("POST", url, headers)
	deepLog.LogUpstreamRequestBody(body)

	start := time.Now()
	h.logChannelRequest(reqID, ch, "", url, len(body))
	result := ch.Fanout(r.Context(), channel.FanoutRequest{
		Body:    body,
		URL:     url,
		Headers: headers,
	})
	latency := time.Since(start).Milliseconds()

	if result.Error != nil {
		if result.StatusCode == http.StatusBadRequest {
			h.logUpstreamHTTPError(reqID, ch, result.Key, model, url, http.StatusBadRequest, http.StatusTooManyRequests, nil, result.Response, nil, deepLog)
			h.sendErrorWithDebug(w, reqID, http.StatusTooManyRequests, upstreamBadRequestErrorCode, string(result.Response), deepLog)
			return nil
		}
		h.logger.RequestWarn(reqID, "子渠道并发请求失败", "channel_id", ch.Config.ID, "error", result.Error)
		return &FailoverError{StatusCode: result.StatusCode, Message: fmt.Sprintf("channel %s: fanout failed: %s", ch.Config.ID, result.Error), AffectsChannelHealth: result.AffectsChannelHealth}
	}
	h.logChannelResponse(reqID, ch, result.Key, result.StatusCode, len(result.Response), latency)

	pt, ct, tt, usageJSON := extractUsageFromRawBody(iface, result.Response)

	if processed := h.processResponseHeaders(ch, model, nil); processed != nil {
		applyProcessedHeaders(w.Header(), processed, "Content-Type", "Cache-Control", "Connection")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(result.StatusCode)
	w.Write(result.Response)

	deepLog.LogUpstreamResponseHeader(result.StatusCode, nil)
	deepLog.LogUpstreamResponseBody(result.Response)
	deepLog.LogClientResponseHeader(result.StatusCode, w.Header())
	deepLog.LogClientResponseBody(result.Response)

	h.recordLog(reqID, ch.Config.ID, string(iface), string(iface), model, model, result.StatusCode, latency, result.Key, "", "", pt, ct, tt, usageJSON, string(iface))
	return nil
}

// ==================== Native Forwarding ====================

func (h *ProxyHandler) nativeForward(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, iface config.InterfaceType, body []byte, model string, stream bool, deepLog *debuglog.RequestLog) *FailoverError {
	// For Chat requests, inject stream_options.include_usage=true unless explicitly disabled.
	if iface == config.InterfaceChat {
		body = injectStreamOptions(body)
	}

	// Fanout fast-path: non-streaming requests with fanout enabled.
	if !stream && ch.FanoutEnabled() {
		return h.fanoutForward(w, r, reqID, ch, iface, body, model, deepLog)
	}

	path := upstreamPathForInterface(iface, model, stream)
	rs := newRetryState(ch, h.config.Failover.ConsecutiveFailThreshold)
	retryCtx, cancelRetry := rs.withDeadline(r.Context())
	defer cancelRetry()
	for {
		key, fe := h.nextKey(retryCtx, ch, rs)
		if fe != nil {
			return fe
		}
		url := ch.Config.NativeBaseURL(iface) + path
		httpReq, err := http.NewRequestWithContext(retryCtx, "POST", url, bytes.NewReader(body))
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
		deepLog.LogUpstreamRequestBody(body)
		h.logChannelRequest(reqID, ch, key.Value, url, len(body))
		attemptStart := time.Now()
		resp, err := ch.HTTPClient().Do(httpReq)
		if err != nil {
			if retryCtx.Err() != nil {
				return retryContextError(ch, retryCtx.Err(), "upstream request ended")
			}
			attemptLatency := time.Since(attemptStart).Milliseconds()
			ch.RecordLatency(key.Value, attemptLatency)
			ch.ReportError(key.Value, 0)
			rs.noteFailure(0, true)
			h.logger.RequestWarn(reqID, "子渠道请求失败", "channel_id", ch.Config.ID, "channel_key", util.MaskKey(key.Value), "reason", "connection_error", "error", err)
			rs.consecFails++
			if rs.consecFails >= rs.consecFailThreshold {
				return &FailoverError{StatusCode: 0, Message: fmt.Sprintf("channel %s: connection failed after %d consecutive errors: %s", ch.Config.ID, rs.consecFails, err.Error()), AffectsChannelHealth: true}
			}
			continue
		}
		h.logChannelResponse(reqID, ch, key.Value, resp.StatusCode, -1, time.Since(attemptStart).Milliseconds())
		if resp.StatusCode == 401 {
			resp.Body.Close()
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, 401)
			rs.noteFailure(401, false)
			h.logger.RequestWarn(reqID, "渠道 Key 被排除", "channel_id", ch.Config.ID, "channel_key", util.MaskKey(key.Value), "reason", "unauthorized", "status", 401)
			rs.consecFails = 0
			continue
		}
		if resp.StatusCode == 429 {
			resp.Body.Close()
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, 429)
			rs.coolDown(key.Value)
			rs.noteFailure(429, false)
			h.logger.RequestWarn(reqID, "渠道 Key 暂时冷却", "channel_id", ch.Config.ID, "channel_key", util.MaskKey(key.Value), "reason", "rate_limited", "status", 429)
			continue
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, resp.StatusCode)
			rs.noteFailure(resp.StatusCode, true)
			h.logger.RequestWarn(reqID, "渠道 Key 本轮失败", "channel_id", ch.Config.ID, "channel_key", util.MaskKey(key.Value), "reason", "server_error", "status", resp.StatusCode)
			rs.consecFails++
			if rs.consecFails >= rs.consecFailThreshold {
				return &FailoverError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("channel %s: %d consecutive upstream failures (last status %d)", ch.Config.ID, rs.consecFails, resp.StatusCode), AffectsChannelHealth: true}
			}
			continue
		}
		if resp.StatusCode == 400 {
			errBodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, h.maxResponseBodyBytes()))
			resp.Body.Close()
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, http.StatusTooManyRequests)
			h.logUpstreamHTTPError(reqID, ch, key.Value, model, url, http.StatusBadRequest, http.StatusTooManyRequests, resp.Header, errBodyBytes, readErr, deepLog)
			h.sendErrorWithDebug(w, reqID, http.StatusTooManyRequests, upstreamBadRequestErrorCode, string(errBodyBytes), deepLog)
			return nil
		}
		if resp.StatusCode >= 400 {
			errBodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, h.maxResponseBodyBytes()))
			resp.Body.Close()
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, resp.StatusCode)
			rs.consecFails = 0
			rs.noteFailure(resp.StatusCode, false)
			h.logUpstreamHTTPError(reqID, ch, key.Value, model, url, resp.StatusCode, 0, resp.Header, errBodyBytes, readErr, deepLog)
			return &FailoverError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("channel %s: upstream returned %d", ch.Config.ID, resp.StatusCode)}
		}

		var pt, ct, tt int
		var usageJSON string

		if stream {
			deepLog.LogUpstreamResponseHeader(resp.StatusCode, resp.Header)
			upstreamDebug := deepLog.NewUpstreamStreamCapture(resp.StatusCode)
			upstreamReader := io.TeeReader(resp.Body, upstreamDebug)
			capture := newStreamUsageCapture(upstreamReader)
			if processed := h.processResponseHeaders(ch, model, resp.Header); processed != nil {
				applyProcessedHeaders(w.Header(), processed, "Content-Type", "Cache-Control", "Connection")
			}
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, no-transform")
			w.Header().Set("Connection", "keep-alive")
			deepLog.LogClientResponseHeader(resp.StatusCode, w.Header())
			w.WriteHeader(200)
			clientDebug := deepLog.NewClientStreamCapture(resp.StatusCode)
			clientWriter := io.MultiWriter(w, clientDebug)
			_, streamErr := io.Copy(clientWriter, capture)
			upstreamDebug.Close()
			clientDebug.Close()
			pt, ct, tt, usageJSON = capture.Usage()
			if streamErr != nil {
				ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
				ch.ReportStreamError(key.Value)
				h.logger.RequestWarn(reqID, "流式应答转发失败", "channel_id", ch.Config.ID, "channel_key", util.MaskKey(key.Value), "error", streamErr)
				resp.Body.Close()
				return nil
			}
		} else {
			respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, h.maxResponseBodyBytes()))
			if readErr != nil {
				h.logger.RequestWarn(reqID, "读取子渠道应答失败", "channel_id", ch.Config.ID, "error", readErr)
			}
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
		ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
		ch.ReportSuccess(key.Value)
		h.recordLog(reqID, ch.Config.ID, string(iface), string(iface), model, model, 200, rs.elapsed().Milliseconds(), key.Value, "", "", pt, ct, tt, usageJSON, string(iface))
		return nil
	}
}

// ==================== Converted Forwarding (Non-Streaming) ====================

func (h *ProxyHandler) convertedNonStreamForward(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, source config.InterfaceType, target config.InterfaceType, chatReq *translate.ChatRequest, model string, targetReq interface{}, deepLog *debuglog.RequestLog) *FailoverError {
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

	// Fanout fast-path for converted non-streaming requests.
	if ch.FanoutEnabled() {
		return h.convertedFanoutForward(w, r, reqID, ch, source, target, sourceBody, chatReq, model, targetReq, deepLog)
	}

	path := upstreamPathForInterface(source, model, false)
	rs := newRetryState(ch, h.config.Failover.ConsecutiveFailThreshold)
	retryCtx, cancelRetry := rs.withDeadline(r.Context())
	defer cancelRetry()
	var result *UpstreamResult
	for {
		key, fe := h.nextKey(retryCtx, ch, rs)
		if fe != nil {
			return fe
		}
		url := ch.Config.NativeBaseURL(source) + path
		httpReq, err := http.NewRequestWithContext(retryCtx, "POST", url, bytes.NewReader(sourceBody))
		if err != nil {
			h.sendError(w, reqID, 500, "create_request_failed", err.Error())
			return nil
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+key.Value)
		if processed := h.processRequestHeaders(ch, model, r.Header); processed != nil {
			applyProcessedHeaders(httpReq.Header, processed, "Content-Type", "Authorization")
		}
		h.logChannelRequest(reqID, ch, key.Value, url, len(sourceBody))
		deepLog.LogUpstreamRequestHeader("POST", url, httpReq.Header)
		deepLog.LogUpstreamRequestBody(sourceBody)
		start := time.Now()
		resp, err := ch.HTTPClient().Do(httpReq)
		if err != nil {
			if retryCtx.Err() != nil {
				return retryContextError(ch, retryCtx.Err(), "upstream request ended")
			}
			ch.RecordLatency(key.Value, time.Since(start).Milliseconds())
			ch.ReportError(key.Value, 0)
			rs.noteFailure(0, true)
			h.logger.RequestWarn(reqID, "子渠道请求失败", "channel_id", ch.Config.ID, "channel_key", util.MaskKey(key.Value), "reason", "connection_error", "error", err)
			rs.consecFails++
			if rs.consecFails >= rs.consecFailThreshold {
				return &FailoverError{StatusCode: 0, Message: fmt.Sprintf("channel %s: connection failed after %d consecutive errors: %s", ch.Config.ID, rs.consecFails, err.Error()), AffectsChannelHealth: true}
			}
			continue
		}
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, h.maxResponseBodyBytes()))
		if readErr != nil {
			h.logger.RequestWarn(reqID, "读取子渠道应答失败", "channel_id", ch.Config.ID, "error", readErr)
		}
		resp.Body.Close()
		latency := time.Since(start).Milliseconds()
		h.logChannelResponse(reqID, ch, key.Value, resp.StatusCode, len(respBody), latency)
		if resp.StatusCode == 401 {
			ch.RecordLatency(key.Value, latency)
			ch.ReportError(key.Value, 401)
			rs.consecFails = 0
			rs.noteFailure(401, false)
			continue
		}
		if resp.StatusCode == 429 {
			ch.RecordLatency(key.Value, latency)
			ch.ReportError(key.Value, 429)
			rs.coolDown(key.Value)
			rs.noteFailure(429, false)
			h.logger.RequestWarn(reqID, "渠道 Key 暂时冷却", "channel_id", ch.Config.ID, "channel_key", util.MaskKey(key.Value), "reason", "rate_limited", "status", 429)
			continue
		}
		if resp.StatusCode >= 500 {
			ch.RecordLatency(key.Value, latency)
			ch.ReportError(key.Value, resp.StatusCode)
			rs.noteFailure(resp.StatusCode, true)
			h.logger.RequestWarn(reqID, "渠道 Key 本轮失败", "channel_id", ch.Config.ID, "channel_key", util.MaskKey(key.Value), "reason", "server_error", "status", resp.StatusCode)
			rs.consecFails++
			if rs.consecFails >= rs.consecFailThreshold {
				return &FailoverError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("channel %s: %d consecutive upstream failures (last status %d)", ch.Config.ID, rs.consecFails, resp.StatusCode), AffectsChannelHealth: true}
			}
			continue
		}
		if resp.StatusCode == 400 {
			errBodyBytes := respBody
			ch.RecordLatency(key.Value, latency)
			ch.ReportError(key.Value, http.StatusTooManyRequests)
			h.logUpstreamHTTPError(reqID, ch, key.Value, model, url, http.StatusBadRequest, http.StatusTooManyRequests, resp.Header, errBodyBytes, readErr, deepLog)
			h.sendErrorWithDebug(w, reqID, http.StatusTooManyRequests, upstreamBadRequestErrorCode, string(errBodyBytes), deepLog)
			return nil
		}
		if resp.StatusCode >= 400 {
			errBodyBytes := respBody
			ch.RecordLatency(key.Value, latency)
			ch.ReportError(key.Value, resp.StatusCode)
			rs.consecFails = 0
			rs.noteFailure(resp.StatusCode, false)
			h.logUpstreamHTTPError(reqID, ch, key.Value, model, url, resp.StatusCode, 0, resp.Header, errBodyBytes, readErr, deepLog)
			return &FailoverError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("channel %s: upstream returned %d", ch.Config.ID, resp.StatusCode)}
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
		h.sendError(w, reqID, 502, "convert_from_source_failed", err.Error())
		return nil
	}
	targetResp, err := convertChatToTarget(target, chatResp, targetReq)
	if err != nil {
		h.sendError(w, reqID, 500, "convert_to_target_failed", err.Error())
		return nil
	}
	deepLog.LogClientResponseHeader(200, w.Header())
	deepLog.LogClientResponseBody(func() []byte { b, _ := json.Marshal(targetResp); return b }())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	json.NewEncoder(w).Encode(targetResp)
	pt, ct, tt, usageJSON := normalizeUsage(chatResp.Usage)
	h.recordLog(reqID, ch.Config.ID, string(target), string(source), model, model, 200, result.LatencyMs, result.Key.Value, "", "", pt, ct, tt, usageJSON, string(target))
	return nil
}

// ==================== Converted Fanout Forwarding (Non-Streaming) ====================

func (h *ProxyHandler) convertedFanoutForward(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, source config.InterfaceType, target config.InterfaceType, sourceBody []byte, chatReq *translate.ChatRequest, model string, targetReq interface{}, deepLog *debuglog.RequestLog) *FailoverError {
	path := upstreamPathForInterface(source, model, false)
	url := ch.Config.NativeBaseURL(source) + path

	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	if processed := h.processRequestHeaders(ch, model, r.Header); processed != nil {
		applyProcessedHeaders(headers, processed, "Content-Type", "Authorization")
	}

	deepLog.LogUpstreamRequestHeader("POST", url, headers)
	deepLog.LogUpstreamRequestBody(sourceBody)

	start := time.Now()
	h.logChannelRequest(reqID, ch, "", url, len(sourceBody))
	result := ch.Fanout(r.Context(), channel.FanoutRequest{
		Body:    sourceBody,
		URL:     url,
		Headers: headers,
	})
	latency := time.Since(start).Milliseconds()

	if result.Error != nil {
		if result.StatusCode == http.StatusBadRequest {
			h.logUpstreamHTTPError(reqID, ch, result.Key, model, url, http.StatusBadRequest, http.StatusTooManyRequests, nil, result.Response, nil, deepLog)
			h.sendErrorWithDebug(w, reqID, http.StatusTooManyRequests, upstreamBadRequestErrorCode, string(result.Response), deepLog)
			return nil
		}
		h.logger.RequestWarn(reqID, "子渠道并发请求失败", "channel_id", ch.Config.ID, "error", result.Error)
		return &FailoverError{StatusCode: result.StatusCode, Message: fmt.Sprintf("channel %s: converted fanout failed: %s", ch.Config.ID, result.Error), AffectsChannelHealth: result.AffectsChannelHealth}
	}
	h.logChannelResponse(reqID, ch, result.Key, result.StatusCode, len(result.Response), latency)

	deepLog.LogUpstreamResponseHeader(result.StatusCode, nil)
	deepLog.LogUpstreamResponseBody(result.Response)

	chatResp, err := convertSourceToChat(source, result.Response, chatReq)
	if err != nil {
		h.sendError(w, reqID, 502, "convert_from_source_failed", err.Error())
		return nil
	}
	targetResp, err := convertChatToTarget(target, chatResp, targetReq)
	if err != nil {
		h.sendError(w, reqID, 500, "convert_to_target_failed", err.Error())
		return nil
	}

	if processed := h.processResponseHeaders(ch, model, nil); processed != nil {
		applyProcessedHeaders(w.Header(), processed, "Content-Type", "Cache-Control", "Connection")
	}
	responseBody, _ := json.Marshal(targetResp)
	deepLog.LogClientResponseHeader(200, w.Header())
	deepLog.LogClientResponseBody(responseBody)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	json.NewEncoder(w).Encode(targetResp)

	pt, ct, tt, usageJSON := normalizeUsage(chatResp.Usage)
	h.recordLog(reqID, ch.Config.ID, string(target), string(source), model, model, 200, latency, result.Key, "", "", pt, ct, tt, usageJSON, string(target))
	return nil
}

// ==================== Converted Forwarding (Streaming) ====================

func (h *ProxyHandler) convertedStreamForward(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, source config.InterfaceType, target config.InterfaceType, chatReq *translate.ChatRequest, model string, targetReq interface{}, deepLog *debuglog.RequestLog) *FailoverError {
	if source == config.InterfaceChat {
		return h.streamFromChatSource(w, r, reqID, ch, target, chatReq, model, targetReq, deepLog)
	}
	return h.streamChainConversion(w, r, reqID, ch, source, target, chatReq, model, targetReq, deepLog)
}
