package channel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// FanoutRequest holds the parameters for a fanout request.
type FanoutRequest struct {
	Body    []byte
	URL     string
	Headers http.Header
}

// FanoutResult holds the response from one fanout attempt.
type FanoutResult struct {
	Response   []byte
	Key        string
	LatencyMs  int64
	Error      error
	StatusCode int
	// AffectsChannelHealth is true when at least one failed attempt was a
	// connection error or an upstream 5xx response.
	AffectsChannelHealth bool
}

// Fanout sends the same request to multiple keys concurrently.
// If WaitAll is false, returns the first successful (2xx) result.
// If WaitAll is true, waits for all and returns the fastest 2xx result.
func (ch *Channel) Fanout(ctx context.Context, req FanoutRequest) *FanoutResult {
	count := ch.FanoutCount()
	keys := ch.KeyPool().GetN(count)
	if len(keys) == 0 {
		return &FanoutResult{Error: fmt.Errorf("no available keys")}
	}

	// Create a cancellable context so we can stop remaining goroutines early.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan *FanoutResult, len(keys))
	var wg sync.WaitGroup

	for _, key := range keys {
		wg.Add(1)
		go func(k *KeyEntry) {
			defer wg.Done()
			result := ch.sendFanoutRequest(ctx, k, req)
			results <- result
		}(key)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	if !ch.FanoutWaitAll() {
		// Return the first successful result, but keep draining losers so
		// key health/error statistics stay accurate.
		var lastStatus int
		affectsChannelHealth := false
		var badRequest *FanoutResult
		for result := range results {
			if isSuccessfulFanoutResult(result) {
				ch.reportFanoutResult(result)
				cancel()
				go ch.drainFanoutResults(results)
				return result
			}
			ch.reportFanoutResult(result)
			if result != nil {
				lastStatus = result.StatusCode
				affectsChannelHealth = affectsChannelHealth || fanoutResultAffectsChannelHealth(result)
				if result.StatusCode == http.StatusBadRequest {
					badRequest = result
				}
			}
		}
		if badRequest != nil {
			failed := *badRequest
			failed.Error = fmt.Errorf("fanout upstream returned 400")
			failed.AffectsChannelHealth = false
			return &failed
		}
		return &FanoutResult{Error: fmt.Errorf("all fanout keys failed"), StatusCode: lastStatus, AffectsChannelHealth: affectsChannelHealth}
	}

	// WaitAll mode: collect all results, pick fastest 2xx.
	var all []*FanoutResult
	for result := range results {
		all = append(all, result)
	}

	var best *FanoutResult
	for _, result := range all {
		if result.Error == nil && result.StatusCode >= 200 && result.StatusCode < 300 {
			if best == nil || result.LatencyMs < best.LatencyMs {
				best = result
			}
		}
	}

	if best != nil {
		for _, result := range all {
			ch.reportFanoutResult(result)
		}
		return best
	}

	// All failed.
	for _, result := range all {
		ch.reportFanoutResult(result)
	}
	result := &FanoutResult{Error: fmt.Errorf("all fanout keys failed")}
	for _, failed := range all {
		if failed != nil {
			if failed.StatusCode == http.StatusBadRequest {
				badRequest := *failed
				badRequest.Error = fmt.Errorf("fanout upstream returned 400")
				badRequest.AffectsChannelHealth = false
				return &badRequest
			}
			result.StatusCode = failed.StatusCode
			result.AffectsChannelHealth = result.AffectsChannelHealth || fanoutResultAffectsChannelHealth(failed)
		}
	}
	return result
}

func isSuccessfulFanoutResult(result *FanoutResult) bool {
	return result != nil && result.Error == nil && result.StatusCode >= 200 && result.StatusCode < 300
}

func (ch *Channel) reportFanoutResult(result *FanoutResult) {
	if result == nil {
		return
	}
	if result.Error != nil {
		if errors.Is(result.Error, context.Canceled) {
			return
		}
		ch.RecordLatency(result.Key, result.LatencyMs)
		ch.ReportError(result.Key, 0)
		return
	}
	ch.RecordLatency(result.Key, result.LatencyMs)
	if result.StatusCode >= 200 && result.StatusCode < 300 {
		ch.ReportSuccess(result.Key)
		return
	}
	ch.ReportError(result.Key, normalizeFanoutStatus(result.StatusCode))
}

func normalizeFanoutStatus(statusCode int) int {
	if statusCode == http.StatusBadRequest {
		return http.StatusTooManyRequests
	}
	return statusCode
}

func fanoutResultAffectsChannelHealth(result *FanoutResult) bool {
	if result == nil {
		return false
	}
	if result.Error != nil {
		return !errors.Is(result.Error, context.Canceled)
	}
	return result.StatusCode >= 500
}

func (ch *Channel) drainFanoutResults(results <-chan *FanoutResult) {
	for result := range results {
		ch.reportFanoutResult(result)
	}
}

// sendFanoutRequest sends a single request with the given key.
func (ch *Channel) sendFanoutRequest(ctx context.Context, key *KeyEntry, req FanoutRequest) *FanoutResult {
	start := time.Now()

	httpReq, err := http.NewRequestWithContext(ctx, "POST", req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return &FanoutResult{Key: key.Value, Error: fmt.Errorf("create request: %w", err), LatencyMs: time.Since(start).Milliseconds()}
	}

	// Copy headers from the prepared request.
	for k, v := range req.Headers {
		httpReq.Header[k] = v
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key.Value)

	resp, err := ch.httpClient.Do(httpReq)
	if err != nil {
		return &FanoutResult{Key: key.Value, Error: err, LatencyMs: time.Since(start).Milliseconds()}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
	if err != nil {
		return &FanoutResult{Key: key.Value, Error: err, LatencyMs: time.Since(start).Milliseconds()}
	}

	return &FanoutResult{
		Response:   respBody,
		Key:        key.Value,
		LatencyMs:  time.Since(start).Milliseconds(),
		StatusCode: resp.StatusCode,
	}
}

// FanoutStreamResult holds the result of a streaming fanout attempt.
type FanoutStreamResult struct {
	Response             *http.Response
	ResponseBody         []byte
	Key                  string
	LatencyMs            int64
	Error                error
	StatusCode           int
	AffectsChannelHealth bool
}

type cancelOnCloseReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelOnCloseReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
}

// FanoutStream sends the same request to multiple keys concurrently and returns
// the first one that responds with HTTP 200. The caller is responsible for closing
// the response body. All other responses are cancelled.
func (ch *Channel) FanoutStream(ctx context.Context, req FanoutRequest) *FanoutStreamResult {
	count := ch.FanoutCount()
	keys := ch.KeyPool().GetN(count)
	if len(keys) == 0 {
		return &FanoutStreamResult{Error: fmt.Errorf("no available keys")}
	}

	type attempt struct {
		resp      *http.Response
		key       *KeyEntry
		err       error
		index     int
		latencyMs int64
	}

	results := make(chan attempt, len(keys))
	var wg sync.WaitGroup

	cancels := make([]context.CancelFunc, len(keys))
	for index, key := range keys {
		attemptCtx, attemptCancel := context.WithCancel(ctx)
		cancels[index] = attemptCancel
		wg.Add(1)
		go func(i int, k *KeyEntry, requestCtx context.Context, requestCancel context.CancelFunc) {
			defer wg.Done()
			start := time.Now()
			httpReq, err := http.NewRequestWithContext(requestCtx, "POST", req.URL, bytes.NewReader(req.Body))
			if err != nil {
				requestCancel()
				results <- attempt{key: k, err: err, index: i, latencyMs: time.Since(start).Milliseconds()}
				return
			}
			for hk, hv := range req.Headers {
				httpReq.Header[hk] = hv
			}
			httpReq.Header.Set("Content-Type", "application/json")
			httpReq.Header.Set("Authorization", "Bearer "+k.Value)

			resp, err := ch.httpClient.Do(httpReq)
			if err != nil {
				requestCancel()
				results <- attempt{key: k, err: err, index: i, latencyMs: time.Since(start).Milliseconds()}
				return
			}
			results <- attempt{resp: resp, key: k, index: i, latencyMs: time.Since(start).Milliseconds()}
		}(index, key, attemptCtx, attemptCancel)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var failures []attempt
	var lastStatus int
	affectsChannelHealth := false
	var badRequest *FanoutStreamResult

	// Wait for the first 200 response or all failures.
	for a := range results {
		if a.err != nil {
			failures = append(failures, a)
			if !errors.Is(a.err, context.Canceled) {
				ch.RecordLatency(a.key.Value, a.latencyMs)
				ch.ReportError(a.key.Value, 0)
				affectsChannelHealth = true
			}
			continue
		}
		if a.resp.StatusCode >= 200 && a.resp.StatusCode < 300 {
			// Cancel only losing attempts. Cancelling the winner's request context
			// here would also interrupt reads from its response body.
			for i, cancel := range cancels {
				if i != a.index {
					cancel()
				}
			}
			// Drain failures in background to avoid goroutine leaks.
			go func() {
				for a := range results {
					if a.resp != nil {
						a.resp.Body.Close()
						cancels[a.index]()
					}
					if a.key != nil && a.err != nil && !errors.Is(a.err, context.Canceled) {
						ch.RecordLatency(a.key.Value, a.latencyMs)
						ch.ReportError(a.key.Value, 0)
					} else if a.key != nil && a.resp != nil && (a.resp.StatusCode < 200 || a.resp.StatusCode >= 300) {
						ch.RecordLatency(a.key.Value, a.latencyMs)
						ch.ReportError(a.key.Value, normalizeFanoutStatus(a.resp.StatusCode))
					}
				}
			}()
			a.resp.Body = &cancelOnCloseReadCloser{ReadCloser: a.resp.Body, cancel: cancels[a.index]}
			return &FanoutStreamResult{
				Response:   a.resp,
				Key:        a.key.Value,
				LatencyMs:  a.latencyMs,
				StatusCode: a.resp.StatusCode,
			}
		}
		// Non-200 — close and report, keep waiting.
		var responseBody []byte
		if a.resp.StatusCode == http.StatusBadRequest {
			responseBody, _ = io.ReadAll(io.LimitReader(a.resp.Body, 64*1024))
		}
		a.resp.Body.Close()
		cancels[a.index]()
		ch.RecordLatency(a.key.Value, a.latencyMs)
		ch.ReportError(a.key.Value, normalizeFanoutStatus(a.resp.StatusCode))
		lastStatus = a.resp.StatusCode
		affectsChannelHealth = affectsChannelHealth || a.resp.StatusCode >= 500
		if a.resp.StatusCode == http.StatusBadRequest {
			badRequest = &FanoutStreamResult{Key: a.key.Value, LatencyMs: a.latencyMs, StatusCode: a.resp.StatusCode, ResponseBody: responseBody}
		}
		failures = append(failures, a)
	}

	// All failed.
	for _, cancel := range cancels {
		cancel()
	}
	if badRequest != nil {
		badRequest.Error = fmt.Errorf("fanout upstream returned 400")
		return badRequest
	}
	return &FanoutStreamResult{Error: fmt.Errorf("all fanout keys failed (%d attempts)", len(failures)), StatusCode: lastStatus, AffectsChannelHealth: affectsChannelHealth}
}
