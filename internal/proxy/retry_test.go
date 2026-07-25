package proxy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/log"
)

func newRetryTestChannel(t *testing.T, strategy string, retry config.RetryConfig) *channel.Channel {
	t.Helper()
	logger := log.New("error", "", false, false)
	logger.SetEnabled(false)
	cfg := config.ChannelConfig{
		ID: "retry-test", Enabled: true, KeyStrategy: strategy,
		Keys:  []config.KeyConfig{{Value: "key-1"}, {Value: "key-2"}},
		Retry: retry,
	}
	manager := channel.NewChannelManager([]config.ChannelConfig{cfg}, nil, logger, nil, "priority")
	t.Cleanup(func() {
		manager.Stop()
		logger.Close()
	})
	return manager.ListChannels()[0]
}

func TestRetryStateUsesCompleteKeyRounds(t *testing.T) {
	ch := newRetryTestChannel(t, "round-robin", config.RetryConfig{
		MaxRotationRounds: 2, MaxTotalWaitMs: 1000,
	})
	rs := newRetryState(ch, 99)
	ctx, cancel := rs.withDeadline(context.Background())
	defer cancel()

	for round := 1; round <= 2; round++ {
		seen := map[string]bool{}
		for range 2 {
			key, fe := (&ProxyHandler{}).nextKey(ctx, ch, rs)
			if fe != nil {
				t.Fatalf("round %d ended early: %v", round, fe)
			}
			if seen[key.Value] {
				t.Fatalf("key %q selected twice in round %d", key.Value, round)
			}
			seen[key.Value] = true
		}
	}

	if _, fe := (&ProxyHandler{}).nextKey(ctx, ch, rs); fe == nil || !strings.Contains(fe.Message, "max rotation rounds") {
		t.Fatalf("expected max-round error, got %v", fe)
	}
}

func TestRetryStateMakesRateLimitedKeyEligibleAfterCooldown(t *testing.T) {
	ch := newRetryTestChannel(t, "round-robin", config.RetryConfig{
		RetryDelay429Ms: 20, MaxRotationRounds: 2, MaxTotalWaitMs: 1000,
	})
	// Keep one candidate so the second selection must wait for its cooldown.
	ch.ReportError("key-2", 401)
	rs := newRetryState(ch, 99)
	ctx, cancel := rs.withDeadline(context.Background())
	defer cancel()
	h := &ProxyHandler{}

	first, fe := h.nextKey(ctx, ch, rs)
	if fe != nil || first.Value != "key-1" {
		t.Fatalf("first selection = %v, %v", first, fe)
	}
	rs.coolDown(first.Value)
	started := time.Now()
	second, fe := h.nextKey(ctx, ch, rs)
	if fe != nil || second.Value != first.Value {
		t.Fatalf("second selection = %v, %v", second, fe)
	}
	if elapsed := time.Since(started); elapsed < 15*time.Millisecond {
		t.Fatalf("cooldown elapsed only %v", elapsed)
	}
}
