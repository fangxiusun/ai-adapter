package proxy

import (
	"fmt"
	"net/http"
	"time"

	"github.com/fangxiusun/ai-adapter/internal/channel"
)

// UpstreamResult holds the response from an upstream request.
type UpstreamResult struct {
	Body       []byte
	StatusCode int
	Headers    http.Header
	Key        *channel.KeyEntry
	LatencyMs  int64
	Error      error
}

// RetryState tracks retry progress for a single dispatch cycle.
type RetryState struct {
	start        time.Time
	excluded     map[string]bool
	maxRounds    int
	retryDelay   time.Duration
	maxTotalWait time.Duration
	lastResult   *UpstreamResult
	lastErr      error
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

// getNextKey returns the next available key that has not been excluded in this retry cycle.
func (h *ProxyHandler) getNextKey(ch *channel.Channel, rs *RetryState) *channel.KeyEntry {
	for i := 0; i < 10; i++ {
		key := ch.GetKey()
		if key == nil {
			return nil
		}
		if !rs.excluded[key.Value] {
			return key
		}
	}
	return nil
}

// checkRotationAndTimeout checks if the retry loop should stop due to timeout.
// Returns true if the caller should return (timeout reached).
func (h *ProxyHandler) checkRotationAndTimeout(ch *channel.Channel, rs *RetryState, w http.ResponseWriter, reqID string, path string) bool {
	if rs.isTimedOut() {
		if rs.lastResult != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(rs.lastResult.StatusCode)
			w.Write(rs.lastResult.Body)
		} else {
			h.sendError(w, 504, "timeout", "max total wait exceeded")
		}
		return true
	}
	return false
}
