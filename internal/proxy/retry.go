package proxy

import (
	"context"
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
	start                    time.Time
	attempted                map[string]bool
	cooldownUntil            map[string]time.Time
	maxRounds                int
	round                    int
	retryDelay               time.Duration
	maxTotalWait             time.Duration
	consecFails              int
	consecFailThreshold      int
	lastFailureAffectsHealth bool
}

func newRetryState(ch *channel.Channel, failoverThreshold int) *RetryState {
	cfg := ch.Config.Retry
	return &RetryState{
		start:         time.Now(),
		attempted:     make(map[string]bool),
		cooldownUntil: make(map[string]time.Time),
		maxRounds:     cfg.MaxRotationRounds,
		round:         1,
		retryDelay:    time.Duration(cfg.RetryDelay429Ms) * time.Millisecond,
		maxTotalWait:  time.Duration(cfg.MaxTotalWaitMs) * time.Millisecond,
		consecFailThreshold: func() int {
			if failoverThreshold > 0 {
				return failoverThreshold
			}
			return 9999 // effectively disabled
		}(),
	}
}

func (rs *RetryState) isTimedOut() bool {
	return rs.maxTotalWait > 0 && time.Since(rs.start) >= rs.maxTotalWait
}

func (rs *RetryState) elapsed() time.Duration {
	return time.Since(rs.start)
}

func (rs *RetryState) withDeadline(parent context.Context) (context.Context, context.CancelFunc) {
	if rs.maxTotalWait <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithDeadline(parent, rs.start.Add(rs.maxTotalWait))
}

func (rs *RetryState) coolDown(key string) {
	rs.cooldownUntil[key] = time.Now().Add(rs.retryDelay)
}

func (rs *RetryState) noteFailure(status int, affectsHealth bool) {
	rs.lastFailureAffectsHealth = affectsHealth
}

func (rs *RetryState) exclusionSet(now time.Time) (map[string]bool, time.Time) {
	excluded := make(map[string]bool, len(rs.attempted)+len(rs.cooldownUntil))
	for key := range rs.attempted {
		excluded[key] = true
	}
	var earliest time.Time
	for key, until := range rs.cooldownUntil {
		if !now.Before(until) {
			delete(rs.cooldownUntil, key)
			continue
		}
		excluded[key] = true
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	return excluded, earliest
}

// nextKey selects every currently available key at most once per round. A Key
// limited by 429 becomes a candidate again after its request-local cooldown.
func (h *ProxyHandler) nextKey(ctx context.Context, ch *channel.Channel, rs *RetryState) (*channel.KeyEntry, *FailoverError) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, retryContextError(ch, err, "retry context ended")
		}
		if rs.isTimedOut() {
			return nil, &FailoverError{StatusCode: http.StatusGatewayTimeout, Message: fmt.Sprintf("channel %s: max total wait exceeded (%dms)", ch.Config.ID, rs.maxTotalWait.Milliseconds())}
		}

		excluded, earliestCooldown := rs.exclusionSet(time.Now())
		if key := ch.KeyPool().NextExcluding(excluded); key != nil {
			rs.attempted[key.Value] = true
			return key, nil
		}

		if len(rs.attempted) == 0 {
			if earliestCooldown.IsZero() {
				return nil, &FailoverError{StatusCode: http.StatusServiceUnavailable, Message: fmt.Sprintf("channel %s: no available keys", ch.Config.ID)}
			}
			if fe := waitForRetry(ctx, ch, earliestCooldown); fe != nil {
				return nil, fe
			}
			continue
		}

		if rs.maxRounds > 0 && rs.round >= rs.maxRounds {
			return nil, &FailoverError{
				StatusCode:           http.StatusServiceUnavailable,
				Message:              fmt.Sprintf("channel %s: max rotation rounds exceeded (%d)", ch.Config.ID, rs.maxRounds),
				AffectsChannelHealth: rs.lastFailureAffectsHealth,
			}
		}
		rs.round++
		rs.attempted = make(map[string]bool)
	}
}

func waitForRetry(ctx context.Context, ch *channel.Channel, until time.Time) *FailoverError {
	timer := time.NewTimer(time.Until(until))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return retryContextError(ch, ctx.Err(), "retry wait ended")
	}
}

func retryContextError(ch *channel.Channel, err error, action string) *FailoverError {
	status := 0
	if err == context.DeadlineExceeded {
		status = http.StatusGatewayTimeout
	}
	return &FailoverError{StatusCode: status, Message: fmt.Sprintf("channel %s: %s: %v", ch.Config.ID, action, err)}
}
