package channel

import (
	"testing"

	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/log"
)

func TestNextExcludingHonorsAllStrategies(t *testing.T) {
	for _, strategy := range []string{"round-robin", "random", "least-errors", "least-latency", "least-rate-limited"} {
		t.Run(strategy, func(t *testing.T) {
			logger := log.New("error", "", false, false)
			logger.SetEnabled(false)
			pool := NewKeyPool([]config.KeyConfig{{Value: "key-1"}, {Value: "key-2"}}, strategy, "test", logger, 3, 1, 1, nil, 0)
			defer pool.Stop()
			defer logger.Close()

			first := pool.NextExcluding(nil)
			if first == nil {
				t.Fatal("first selection is nil")
			}
			second := pool.NextExcluding(map[string]bool{first.Value: true})
			if second == nil || second.Value == first.Value {
				t.Fatalf("excluded key selected again: first=%v second=%v", first, second)
			}
		})
	}
}

func TestOn401CountsRequest(t *testing.T) {
	state := NewKeyState(3, 1, 1)
	state.On401()
	if state.RequestCount != 1 || state.Error401 != 1 || !state.PermanentlySkipped {
		t.Fatalf("unexpected 401 state: %+v", state)
	}
}

func TestOn429PausesAtConfiguredThreshold(t *testing.T) {
	state := NewKeyState(3, 1, 1)
	state.On429()
	state.On429()
	if state.Paused {
		t.Fatal("key paused before threshold")
	}
	state.On429()
	if !state.Paused {
		t.Fatal("key was not paused at threshold")
	}
}
