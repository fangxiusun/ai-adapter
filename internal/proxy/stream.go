package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	h.logChannelRequest(reqID, ch, "", url, len(sourceBody))
	result := ch.FanoutStream(r.Context(), channel.FanoutRequest{
		Body:    sourceBody,
		URL:     url,
		Headers: headers,
	})

	if result.Error != nil {
		if classifyAttempt(result.StatusCode, nil) == attemptMappedBadRequest {
			h.logUpstreamHTTPError(reqID, ch, result.Key, model, url, http.StatusBadRequest, 0, nil, result.ResponseBody, nil, deepLog)
			return &FailoverError{StatusCode: http.StatusBadRequest, Message: fmt.Sprintf("channel %s: fanout stream upstream returned 400", ch.Config.ID)}
		}
		h.logger.RequestWarn(reqID, "子渠道并发流式请求失败", "channel_id", ch.Config.ID, "error", result.Error)
		return &FailoverError{StatusCode: result.StatusCode, Message: fmt.Sprintf("channel %s: fanout stream failed: %s", ch.Config.ID, result.Error), AffectsChannelHealth: result.AffectsChannelHealth}
	}

	resp := result.Response
	defer resp.Body.Close()
	h.logChannelResponse(reqID, ch, result.Key, resp.StatusCode, -1, result.LatencyMs)

	deepLog.LogUpstreamResponseHeader(resp.StatusCode, resp.Header)
	upstreamDebug := deepLog.NewUpstreamStreamCapture(resp.StatusCode)
	defer upstreamDebug.Close()
	upstreamReader := io.TeeReader(resp.Body, upstreamDebug)
	capture := newStreamUsageCapture(upstreamReader)
	if processed := h.processResponseHeaders(ch, model, resp.Header); processed != nil {
		applyProcessedHeaders(w.Header(), processed, "Content-Type", "Cache-Control", "Connection")
	}
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
	clientDebug := deepLog.NewClientStreamCapture(200)
	defer clientDebug.Close()
	clientWriter := io.MultiWriter(w, clientDebug)

	streamErr := h.pipeChatStreamToTarget(r.Context(), target, capture, clientWriter, chatReq, targetReq, flusher)
	attemptLatency := time.Since(start).Milliseconds()
	ch.RecordLatency(result.Key, attemptLatency)
	if streamErr != nil {
		ch.ReportStreamError(result.Key)
		h.logger.RequestWarn(reqID, "并发流式转发失败", "channel_id", ch.Config.ID, "channel_key", result.Key, "error", streamErr)
		h.recordErrorLog(reqID, http.StatusBadGateway, "stream_forward_failed", streamErr.Error())
		return handledError(http.StatusBadGateway, streamErr.Error())
	}
	ch.ReportSuccess(result.Key)

	pt, ct, tt, usageJSON := capture.Usage()
	h.recordLog(reqID, ch.Config.ID, string(target), string(config.InterfaceChat), model, model, 200, attemptLatency, result.Key, "", "", pt, ct, tt, usageJSON, string(target))
	return nil
}

// ==================== Stream Forwarding ====================

func (h *ProxyHandler) streamFromChatSource(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, target config.InterfaceType, chatReq *translate.ChatRequest, model string, targetReq interface{}, deepLog *debuglog.RequestLog, rs *RetryState) *FailoverError {
	// Inject stream_options.include_usage=true for Chat upstream.
	injectedReq := *chatReq
	injectedReq.StreamOptions = &translate.StreamOptions{IncludeUsage: true}
	sourceBody, err := json.Marshal(&injectedReq)
	if err != nil {
		h.sendError(w, reqID, 500, "marshal_failed", err.Error())
		return handledError(http.StatusInternalServerError, err.Error())
	}
	path := upstreamPathForInterface(config.InterfaceChat, model, true)

	// Fanout fast-path for streaming requests.
	if ch.FanoutEnabled() && rs == nil {
		return h.fanoutStreamForward(w, r, reqID, ch, target, chatReq, model, sourceBody, path, targetReq, deepLog)
	}
	if rs == nil {
		rs = newRetryState(ch)
	}
	retryCtx, cancelRetry := rs.withDeadline(r.Context())
	defer cancelRetry()
	for {
		key, fe := h.nextKey(retryCtx, ch, rs)
		if fe != nil {
			return fe
		}
		url := ch.Config.NativeBaseURL(config.InterfaceChat) + path
		httpReq, err := http.NewRequestWithContext(retryCtx, "POST", url, bytes.NewReader(sourceBody))
		if err != nil {
			h.sendError(w, reqID, 500, "create_request_failed", err.Error())
			return handledError(http.StatusInternalServerError, err.Error())
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+key.Value)
		if processed := h.processRequestHeaders(ch, model, r.Header); processed != nil {
			applyProcessedHeaders(httpReq.Header, processed, "Content-Type", "Authorization")
		}
		deepLog.LogUpstreamRequestHeader("POST", url, httpReq.Header)
		deepLog.LogUpstreamRequestBody(sourceBody)
		h.logChannelRequest(reqID, ch, key.Value, url, len(sourceBody))
		attemptStart := time.Now()
		resp, err := ch.HTTPClient().Do(httpReq)
		if err != nil {
			if retryCtx.Err() != nil {
				return retryContextError(ch, retryCtx.Err(), "upstream request ended")
			}
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, 0)
			rs.noteFailure(0, true)
			continue
		}
		h.logChannelResponse(reqID, ch, key.Value, resp.StatusCode, -1, time.Since(attemptStart).Milliseconds())
		resultClass := classifyAttempt(resp.StatusCode, nil)
		if resultClass == attemptUnauthorized {
			resp.Body.Close()
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, 401)
			rs.noteFailure(401, false)
			continue
		}
		if resultClass == attemptRateLimited {
			resp.Body.Close()
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, 429)
			rs.coolDown(key.Value)
			rs.noteFailure(429, false)
			continue
		}
		if resultClass == attemptServerError {
			resp.Body.Close()
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, resp.StatusCode)
			rs.noteFailure(resp.StatusCode, true)
			continue
		}
		if resultClass == attemptMappedBadRequest {
			errBodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, http.StatusTooManyRequests)
			rs.coolDown(key.Value)
			rs.noteFailure(400, false)
			h.logUpstreamHTTPError(reqID, ch, key.Value, model, url, http.StatusBadRequest, 0, resp.Header, errBodyBytes, readErr, deepLog)
			continue
		}
		if resultClass == attemptClientError {
			errBodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, resp.StatusCode)
			rs.noteFailure(resp.StatusCode, false)
			h.logUpstreamHTTPError(reqID, ch, key.Value, model, url, resp.StatusCode, 0, resp.Header, errBodyBytes, readErr, deepLog)
			return &FailoverError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("channel %s: upstream returned %d", ch.Config.ID, resp.StatusCode)}
		}

		deepLog.LogUpstreamResponseHeader(resp.StatusCode, resp.Header)
		upstreamDebug := deepLog.NewUpstreamStreamCapture(resp.StatusCode)
		upstreamReader := io.TeeReader(resp.Body, upstreamDebug)
		capture := newStreamUsageCapture(upstreamReader)
		if processed := h.processResponseHeaders(ch, model, resp.Header); processed != nil {
			applyProcessedHeaders(w.Header(), processed, "Content-Type", "Cache-Control", "Connection")
		}
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
		clientDebug := deepLog.NewClientStreamCapture(200)
		clientWriter := io.MultiWriter(w, clientDebug)
		streamErr := h.pipeChatStreamToTarget(retryCtx, target, capture, clientWriter, chatReq, targetReq, flusher)
		upstreamDebug.Close()
		clientDebug.Close()
		resp.Body.Close()
		pt, ct, tt, usageJSON := capture.Usage()
		attemptLatency := time.Since(attemptStart).Milliseconds()
		ch.RecordLatency(key.Value, attemptLatency)
		if streamErr != nil {
			ch.ReportStreamError(key.Value)
			h.logger.RequestWarn(reqID, "流式转换失败", "channel_id", ch.Config.ID, "error", streamErr)
			h.recordErrorLog(reqID, http.StatusBadGateway, "stream_conversion_failed", streamErr.Error())
			return handledError(http.StatusBadGateway, streamErr.Error())
		}
		ch.ReportSuccess(key.Value)
		h.recordLog(reqID, ch.Config.ID, string(target), string(config.InterfaceChat), model, model, 200, rs.elapsed().Milliseconds(), key.Value, "", "", pt, ct, tt, usageJSON, string(target))
		return nil
	}
}

func (h *ProxyHandler) streamChainConversion(w http.ResponseWriter, r *http.Request, reqID string, ch *channel.Channel, source config.InterfaceType, target config.InterfaceType, chatReq *translate.ChatRequest, model string, targetReq interface{}, deepLog *debuglog.RequestLog, rs *RetryState) *FailoverError {
	sourceReq, err := convertChatToSource(source, chatReq)
	if err != nil {
		h.sendError(w, reqID, 400, "convert_to_source_failed", err.Error())
		return handledError(http.StatusBadRequest, err.Error())
	}
	sourceBody, err := json.Marshal(sourceReq)
	if err != nil {
		h.sendError(w, reqID, 500, "marshal_source_failed", err.Error())
		return handledError(http.StatusInternalServerError, err.Error())
	}
	path := upstreamPathForInterface(source, model, true)
	if rs == nil {
		rs = newRetryState(ch)
	}
	retryCtx, cancelRetry := rs.withDeadline(r.Context())
	defer cancelRetry()
	for {
		key, fe := h.nextKey(retryCtx, ch, rs)
		if fe != nil {
			return fe
		}
		url := ch.Config.NativeBaseURL(source) + path
		httpReq, err := http.NewRequestWithContext(retryCtx, "POST", url, bytes.NewReader(sourceBody))
		if err != nil {
			h.sendError(w, reqID, 500, "create_request_failed", err.Error())
			return handledError(http.StatusInternalServerError, err.Error())
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+key.Value)
		if processed := h.processRequestHeaders(ch, model, r.Header); processed != nil {
			applyProcessedHeaders(httpReq.Header, processed, "Content-Type", "Authorization")
		}
		deepLog.LogUpstreamRequestHeader("POST", url, httpReq.Header)
		deepLog.LogUpstreamRequestBody(sourceBody)
		h.logChannelRequest(reqID, ch, key.Value, url, len(sourceBody))
		attemptStart := time.Now()
		resp, err := ch.HTTPClient().Do(httpReq)
		if err != nil {
			if retryCtx.Err() != nil {
				return retryContextError(ch, retryCtx.Err(), "upstream request ended")
			}
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, 0)
			rs.noteFailure(0, true)
			continue
		}
		h.logChannelResponse(reqID, ch, key.Value, resp.StatusCode, -1, time.Since(attemptStart).Milliseconds())
		resultClass := classifyAttempt(resp.StatusCode, nil)
		if resultClass == attemptUnauthorized {
			resp.Body.Close()
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, 401)
			rs.noteFailure(401, false)
			continue
		}
		if resultClass == attemptRateLimited {
			resp.Body.Close()
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, 429)
			rs.coolDown(key.Value)
			rs.noteFailure(429, false)
			continue
		}
		if resultClass == attemptServerError {
			resp.Body.Close()
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, resp.StatusCode)
			rs.noteFailure(resp.StatusCode, true)
			continue
		}
		if resultClass == attemptMappedBadRequest {
			errBodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, http.StatusTooManyRequests)
			rs.coolDown(key.Value)
			rs.noteFailure(400, false)
			h.logUpstreamHTTPError(reqID, ch, key.Value, model, url, http.StatusBadRequest, 0, resp.Header, errBodyBytes, readErr, deepLog)
			continue
		}
		if resultClass == attemptClientError {
			errBodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			ch.RecordLatency(key.Value, time.Since(attemptStart).Milliseconds())
			ch.ReportError(key.Value, resp.StatusCode)
			rs.noteFailure(resp.StatusCode, false)
			h.logUpstreamHTTPError(reqID, ch, key.Value, model, url, resp.StatusCode, 0, resp.Header, errBodyBytes, readErr, deepLog)
			return &FailoverError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("channel %s: upstream returned %d", ch.Config.ID, resp.StatusCode)}
		}

		deepLog.LogUpstreamResponseHeader(resp.StatusCode, resp.Header)
		upstreamDebug := deepLog.NewUpstreamStreamCapture(resp.StatusCode)
		upstreamReader := io.TeeReader(resp.Body, upstreamDebug)
		if processed := h.processResponseHeaders(ch, model, resp.Header); processed != nil {
			applyProcessedHeaders(w.Header(), processed, "Content-Type", "Cache-Control", "Connection")
		}
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
		clientDebug := deepLog.NewClientStreamCapture(200)
		clientWriter := io.MultiWriter(w, clientDebug)
		chatResp, streamErr := h.pipeConvertedStream(retryCtx, source, target, upstreamReader, clientWriter, chatReq, targetReq, flusher)
		upstreamDebug.Close()
		clientDebug.Close()
		resp.Body.Close()
		attemptLatency := time.Since(attemptStart).Milliseconds()
		ch.RecordLatency(key.Value, attemptLatency)
		if streamErr != nil {
			ch.ReportStreamError(key.Value)
			h.logger.RequestWarn(reqID, "跨协议流式转换失败", "channel_id", ch.Config.ID, "error", streamErr)
			h.recordErrorLog(reqID, http.StatusBadGateway, "stream_conversion_failed", streamErr.Error())
			return handledError(http.StatusBadGateway, streamErr.Error())
		}
		ch.ReportSuccess(key.Value)
		pt, ct, tt, usageJSON := normalizeUsage(chatResp.Usage)
		h.recordLog(reqID, ch.Config.ID, string(target), string(source), model, model, 200, rs.elapsed().Milliseconds(), key.Value, "", "", pt, ct, tt, usageJSON, string(target))
		return nil
	}
}

type sourceStreamResult struct {
	response *translate.ChatResponse
	err      error
}

func (h *ProxyHandler) pipeConvertedStream(ctx context.Context, source, target config.InterfaceType, upstream io.Reader, sink io.Writer, chatReq *translate.ChatRequest, targetReq interface{}, flusher func()) (*translate.ChatResponse, error) {
	if target == config.InterfaceChat {
		return h.pipeSourceStreamToChat(ctx, source, upstream, sink, chatReq, flusher)
	}

	chatReader, chatWriter := io.Pipe()
	sourceDone := make(chan sourceStreamResult, 1)
	go func() {
		resp, err := h.pipeSourceStreamToChat(ctx, source, upstream, chatWriter, chatReq, nil)
		if err != nil {
			_ = chatWriter.CloseWithError(err)
		} else {
			_ = chatWriter.Close()
		}
		sourceDone <- sourceStreamResult{response: resp, err: err}
	}()

	targetErr := h.pipeChatStreamToTarget(ctx, target, chatReader, sink, chatReq, targetReq, flusher)
	if targetErr != nil {
		_ = chatReader.CloseWithError(targetErr)
	} else {
		_ = chatReader.Close()
	}
	sourceResult := <-sourceDone
	if sourceResult.err != nil {
		return sourceResult.response, sourceResult.err
	}
	if targetErr != nil {
		return sourceResult.response, targetErr
	}
	return sourceResult.response, nil
}

func (h *ProxyHandler) pipeSourceStreamToChat(ctx context.Context, source config.InterfaceType, upstream io.Reader, sink io.Writer, chatReq *translate.ChatRequest, flusher func()) (*translate.ChatResponse, error) {
	switch source {
	case config.InterfaceResponses:
		return translate.PipeResponsesStreamToChat(ctx, upstream, flushEachWrite(sink, flusher), chatReq, translate.TranslateOpts{})
	case config.InterfaceMessages:
		return translate.PipeClaudeStreamToChat(ctx, upstream, sink, chatReq, flusher)
	case config.InterfaceGenerateContent:
		return translate.PipeGeminiStreamToChat(ctx, upstream, sink, chatReq, flusher)
	default:
		return nil, fmt.Errorf("unsupported streaming source: %s", source)
	}
}

func (h *ProxyHandler) pipeChatStreamToTarget(ctx context.Context, target config.InterfaceType, upstream io.Reader, sink io.Writer, chatReq *translate.ChatRequest, targetReq interface{}, flusher func()) error {
	switch target {
	case config.InterfaceChat:
		_, err := io.Copy(flushEachWrite(sink, flusher), upstream)
		return err
	case config.InterfaceResponses:
		respReq, _ := targetReq.(*translate.ResponsesRequest)
		_, err := translate.PipeChatStreamToResponses(ctx, upstream, flushEachWrite(sink, flusher), respReq, translate.TranslateOpts{ExtractInlineThink: true})
		return err
	case config.InterfaceMessages:
		_, err := translate.PipeChatStreamToClaude(ctx, upstream, sink, chatReq, flusher)
		return err
	case config.InterfaceGenerateContent:
		_, err := translate.PipeChatStreamToGemini(ctx, upstream, sink, chatReq, flusher)
		return err
	default:
		return fmt.Errorf("unsupported streaming target: %s", target)
	}
}

type flushingWriter struct {
	sink    io.Writer
	flusher func()
}

func (w *flushingWriter) Write(p []byte) (int, error) {
	n, err := w.sink.Write(p)
	if err == nil && w.flusher != nil {
		w.flusher()
	}
	return n, err
}

func flushEachWrite(sink io.Writer, flusher func()) io.Writer {
	if flusher == nil {
		return sink
	}
	return &flushingWriter{sink: sink, flusher: flusher}
}
