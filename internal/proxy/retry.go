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
	lastResult           *UpstreamResult
	lastErr              error
	consecFails          int
	consecFailThreshold  int
}

func newRetryState(ch *channel.Channel, failoverThreshold int) *RetryState {
	cfg := ch.Config.Retry
	return &RetryState{
		start:        time.Now(),
		excluded:     make(map[string]bool),
		maxRounds:    cfg.MaxRotationRounds,
		retryDelay:   time.Duration(cfg.RetryDelay429Ms) * time.Millisecond,
		maxTotalWait: time.Duration(cfg.MaxTotalWaitMs) * time.Millisecond,
		lastErr:             fmt.Errorf("all retries failed"),
		consecFailThreshold: func() int {
			if failoverThreshold > 0 {
				return failoverThreshold
			}
			return 9999 // effectively disabled
		}(),
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
// Returns a FailoverError if timeout reached, nil otherwise.
func (h *ProxyHandler) checkRotationAndTimeout(ch *channel.Channel, rs *RetryState, reqID string) *FailoverError {
	if rs.isTimedOut() {
		return &FailoverError{StatusCode: 504, Message: fmt.Sprintf("channel %s: max total wait exceeded (%dms)", ch.Config.ID, rs.maxTotalWait.Milliseconds())}
	}
	return nil
}
