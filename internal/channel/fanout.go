package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type FanoutRequest struct {
	Body      interface{}
	URL       string
	Headers   map[string]string
}

type FanoutResult struct {
	Response  []byte
	Key       string
	LatencyMs int64
	Error     error
	StatusCode int
}

func (ch *Channel) Fanout(ctx context.Context, req FanoutRequest) *FanoutResult {
	count := ch.FanoutCount()
	keys := ch.KeyPool().GetN(count)
	if len(keys) == 0 {
		return &FanoutResult{Error: fmt.Errorf("no available keys")}
	}

	ch.logger.LogFanout(ch.Config.ID, len(keys), ch.Config.KeyStrategy)

	results := make(chan *FanoutResult, len(keys))
	var wg sync.WaitGroup

	for _, key := range keys {
		wg.Add(1)
		go func(k *KeyEntry) {
			defer wg.Done()
			result := ch.sendRequest(ctx, k, req)
			results <- result
		}(key)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	if !ch.FanoutWaitAll() {
		for result := range results {
			if result.Error == nil && result.StatusCode >= 200 && result.StatusCode < 300 {
				return result
			}
		}
		return &FanoutResult{Error: fmt.Errorf("all keys failed")}
	}

	var best *FanoutResult
	for result := range results {
		if result.Error == nil && result.StatusCode >= 200 && result.StatusCode < 300 {
			if best == nil || result.LatencyMs < best.LatencyMs {
				best = result
			}
		}
	}
	if best != nil {
		return best
	}
	return &FanoutResult{Error: fmt.Errorf("all keys failed")}
}

func (ch *Channel) sendRequest(ctx context.Context, key *KeyEntry, req FanoutRequest) *FanoutResult {
	start := time.Now()

	body, err := json.Marshal(req.Body)
	if err != nil {
		return &FanoutResult{Key: key.Value, Error: fmt.Errorf("marshal body: %w", err)}
	}

	url := req.URL
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return &FanoutResult{Key: key.Value, Error: fmt.Errorf("create request: %w", err)}
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key.Value)
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := ch.httpClient.Do(httpReq)
	if err != nil {
		ch.ReportError(key.Value, 1)
		return &FanoutResult{Key: key.Value, Error: err, LatencyMs: time.Since(start).Milliseconds()}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		ch.ReportError(key.Value, 1)
		return &FanoutResult{Key: key.Value, Error: err, LatencyMs: time.Since(start).Milliseconds()}
	}

	latency := time.Since(start).Milliseconds()
	ch.KeyPool().ReportSuccess(key.Value)

	return &FanoutResult{
		Response:   respBody,
		Key:        key.Value,
		LatencyMs:  latency,
		StatusCode: resp.StatusCode,
	}
}
