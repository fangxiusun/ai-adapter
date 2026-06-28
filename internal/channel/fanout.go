package channel

import (
	"bytes"
	"context"
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

	ch.logger.LogFanout(ch.Config.ID, len(keys), ch.Config.KeyStrategy)

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
		// Return the first successful result, cancel the rest.
		for result := range results {
			if result.Error == nil && result.StatusCode >= 200 && result.StatusCode < 300 {
				ch.ReportSuccess(result.Key)
				cancel()
				return result
			}
		}
		// All failed — report errors for all keys used.
		for result := range results {
			if result.Error != nil {
				ch.ReportError(result.Key, 0)
			} else {
				ch.ReportError(result.Key, result.StatusCode)
			}
		}
		return &FanoutResult{Error: fmt.Errorf("all fanout keys failed")}
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
		ch.ReportSuccess(best.Key)
		// Report errors for non-winning keys.
		for _, result := range all {
			if result == best {
				continue
			}
			if result.Error != nil {
				ch.ReportError(result.Key, 0)
			} else if result.StatusCode >= 200 && result.StatusCode < 300 {
				ch.ReportSuccess(result.Key)
			} else {
				ch.ReportError(result.Key, result.StatusCode)
			}
		}
		return best
	}

	// All failed.
	for _, result := range all {
		if result.Error != nil {
			ch.ReportError(result.Key, 0)
		} else {
			ch.ReportError(result.Key, result.StatusCode)
		}
	}
	return &FanoutResult{Error: fmt.Errorf("all fanout keys failed")}
}

// sendFanoutRequest sends a single request with the given key.
func (ch *Channel) sendFanoutRequest(ctx context.Context, key *KeyEntry, req FanoutRequest) *FanoutResult {
	start := time.Now()

	httpReq, err := http.NewRequestWithContext(ctx, "POST", req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return &FanoutResult{Key: key.Value, Error: fmt.Errorf("create request: %w", err)}
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
	Response   *http.Response
	Key        string
	Error      error
	StatusCode int
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

	ch.logger.LogFanout(ch.Config.ID, len(keys), ch.Config.KeyStrategy)

	ctx, cancel := context.WithCancel(ctx)

	type attempt struct {
		resp *http.Response
		key  *KeyEntry
		err  error
	}

	results := make(chan attempt, len(keys))
	var wg sync.WaitGroup

	for _, key := range keys {
		wg.Add(1)
		go func(k *KeyEntry) {
			defer wg.Done()
			httpReq, err := http.NewRequestWithContext(ctx, "POST", req.URL, bytes.NewReader(req.Body))
			if err != nil {
				results <- attempt{key: k, err: err}
				return
			}
			for hk, hv := range req.Headers {
				httpReq.Header[hk] = hv
			}
			httpReq.Header.Set("Content-Type", "application/json")
			httpReq.Header.Set("Authorization", "Bearer "+k.Value)

			resp, err := ch.httpClient.Do(httpReq)
			if err != nil {
				results <- attempt{key: k, err: err}
				return
			}
			results <- attempt{resp: resp, key: k}
		}(key)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var failures []attempt

	// Wait for the first 200 response or all failures.
	for a := range results {
		if a.err != nil {
			failures = append(failures, a)
			ch.ReportError(a.key.Value, 0)
			continue
		}
		if a.resp.StatusCode >= 200 && a.resp.StatusCode < 300 {
			// Winner — cancel all others.
			cancel()
			// Drain failures in background to avoid goroutine leaks.
			go func() {
				for a := range results {
					if a.resp != nil {
						a.resp.Body.Close()
					}
					if a.key != nil {
						ch.ReportError(a.key.Value, 0)
					}
				}
			}()
			return &FanoutStreamResult{
				Response:   a.resp,
				Key:        a.key.Value,
				StatusCode: a.resp.StatusCode,
			}
		}
		// Non-200 — close and report, keep waiting.
		a.resp.Body.Close()
		ch.ReportError(a.key.Value, a.resp.StatusCode)
		failures = append(failures, a)
	}

	// All failed.
	cancel()
	return &FanoutStreamResult{Error: fmt.Errorf("all fanout keys failed (%d attempts)", len(failures))}
}
