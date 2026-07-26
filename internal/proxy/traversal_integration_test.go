package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/debuglog"
	"github.com/fangxiusun/ai-adapter/internal/log"
)

const traversalSuccessBody = `{"id":"chatcmpl-ok","object":"chat.completion","created":1,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`

func TestChannelTraversalInterleavesKeysAndPrefersLastSuccess(t *testing.T) {
	var mu sync.Mutex
	phase := 1
	var attempts []string

	serverFor := func(channelID string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			route := channelID + "/" + key
			mu.Lock()
			attempts = append(attempts, route)
			currentPhase := phase
			mu.Unlock()

			success := (currentPhase <= 2 && route == "B/key-B2") ||
				(currentPhase == 3 && route == "A/key-A2")
			if !success {
				http.Error(w, "retry", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(traversalSuccessBody))
		}))
	}

	serverA := serverFor("A")
	defer serverA.Close()
	serverB := serverFor("B")
	defer serverB.Close()

	handler := newTraversalTestHandler(t, []config.ChannelConfig{
		traversalTestChannel("A", serverA.URL, 1, []string{"key-A1", "key-A2"}, 1, 1),
		traversalTestChannel("B", serverB.URL, 2, []string{"key-B1", "key-B2"}, 1, 1),
	})

	assertTraversalRequestOK(t, handler)
	assertAttemptOrder(t, attempts, []string{"A/key-A1", "B/key-B1", "A/key-A2", "B/key-B2"})

	mu.Lock()
	phase = 2
	attempts = nil
	mu.Unlock()
	assertTraversalRequestOK(t, handler)
	mu.Lock()
	secondAttempts := append([]string(nil), attempts...)
	mu.Unlock()
	assertAttemptOrder(t, secondAttempts, []string{"B/key-B2"})

	mu.Lock()
	phase = 3
	attempts = nil
	mu.Unlock()
	assertTraversalRequestOK(t, handler)
	mu.Lock()
	thirdAttempts := append([]string(nil), attempts...)
	mu.Unlock()
	assertAttemptOrder(t, thirdAttempts, []string{"B/key-B2", "A/key-A1", "B/key-B1", "A/key-A2"})
}

func TestChannelTraversalHonorsCompleteRounds(t *testing.T) {
	var mu sync.Mutex
	var attempts []string
	serverFor := func(channelID string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			mu.Lock()
			attempts = append(attempts, channelID+"/"+key)
			mu.Unlock()
			http.Error(w, "retry", http.StatusBadGateway)
		}))
	}
	serverA := serverFor("A")
	defer serverA.Close()
	serverB := serverFor("B")
	defer serverB.Close()

	handler := newTraversalTestHandler(t, []config.ChannelConfig{
		traversalTestChannel("A", serverA.URL, 1, []string{"key-A1"}, 2, 1),
		traversalTestChannel("B", serverB.URL, 2, []string{"key-B1"}, 2, 1),
	})

	rec := performTraversalRequest(handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	assertAttemptOrder(t, attempts, []string{"A/key-A1", "B/key-B1", "A/key-A1", "B/key-B1"})
}

func TestChannelTraversalCooldownReentersNextRound(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					http.Error(w, "cooldown", status)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(traversalSuccessBody))
			}))
			defer upstream.Close()

			handler := newTraversalTestHandler(t, []config.ChannelConfig{
				traversalTestChannel("A", upstream.URL, 1, []string{"key-A1"}, 2, 30),
			})
			started := time.Now()
			assertTraversalRequestOK(t, handler)
			if calls.Load() != 2 {
				t.Fatalf("upstream calls = %d, want 2", calls.Load())
			}
			if elapsed := time.Since(started); elapsed < 20*time.Millisecond {
				t.Fatalf("cooldown elapsed only %v", elapsed)
			}
		})
	}
}

func TestChannelTraversalPermanentlySkipsUnauthorizedKey(t *testing.T) {
	var key1Calls atomic.Int32
	var key2Calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key == "key-A1" {
			key1Calls.Add(1)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if key2Calls.Add(1) == 1 {
			http.Error(w, "retry", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(traversalSuccessBody))
	}))
	defer upstream.Close()

	handler := newTraversalTestHandler(t, []config.ChannelConfig{
		traversalTestChannel("A", upstream.URL, 1, []string{"key-A1", "key-A2"}, 2, 1),
	})
	assertTraversalRequestOK(t, handler)
	if key1Calls.Load() != 1 || key2Calls.Load() != 2 {
		t.Fatalf("calls key1=%d key2=%d, want 1 and 2", key1Calls.Load(), key2Calls.Load())
	}
	stats := handler.channels.ListChannels()[0].KeyPool().GetStats()
	if len(stats) != 2 || !stats[0].PermanentlySkipped || stats[0].Error401 != 1 || stats[0].RequestCount != 1 {
		t.Fatalf("unexpected unauthorized key stats: %+v", stats)
	}
}

func TestChannelTraversalContinuesAfterOtherClientError(t *testing.T) {
	primaryCalls := atomic.Int32{}
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer primary.Close()
	backupCalls := atomic.Int32{}
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(traversalSuccessBody))
	}))
	defer backup.Close()

	handler := newTraversalTestHandler(t, []config.ChannelConfig{
		traversalTestChannel("A", primary.URL, 1, []string{"key-A1"}, 1, 1),
		traversalTestChannel("B", backup.URL, 2, []string{"key-B1"}, 1, 1),
	})
	assertTraversalRequestOK(t, handler)
	if primaryCalls.Load() != 1 || backupCalls.Load() != 1 {
		t.Fatalf("calls primary=%d backup=%d, want 1 and 1", primaryCalls.Load(), backupCalls.Load())
	}
}

func TestCrossChannelTraversalBypassesFanout(t *testing.T) {
	var active atomic.Int32
	var maxActive atomic.Int32
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maxActive.Load()
			if current <= observed || maxActive.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		if calls.Add(1) == 1 {
			http.Error(w, "retry", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(traversalSuccessBody))
	}))
	defer upstream.Close()

	channelConfig := traversalTestChannel("A", upstream.URL, 1, []string{"key-A1", "key-A2"}, 1, 1)
	channelConfig.Fanout = config.FanoutConfig{Enabled: true, Count: 2}
	handler := newTraversalTestHandler(t, []config.ChannelConfig{channelConfig})
	assertTraversalRequestOK(t, handler)
	if maxActive.Load() != 1 {
		t.Fatalf("max concurrent upstream attempts = %d, want 1", maxActive.Load())
	}
}

func traversalTestChannel(id, baseURL string, priority int, keys []string, rounds, retryDelayMs int) config.ChannelConfig {
	keyConfigs := make([]config.KeyConfig, 0, len(keys))
	for _, key := range keys {
		keyConfigs = append(keyConfigs, config.KeyConfig{Value: key, Name: key})
	}
	return config.ChannelConfig{
		ID: id, Name: id, Enabled: true, Priority: priority,
		DefaultModel: "test-model",
		Models:       []config.ModelConfig{{ID: "test-model", DisplayName: "test-model"}},
		Keys:         keyConfigs,
		KeyStrategy:  "least-errors", RequestTimeoutMs: 1000, ChatURL: baseURL,
		Retry: config.RetryConfig{
			RetryDelay429Ms: retryDelayMs, MaxRotationRounds: rounds, MaxTotalWaitMs: 1000,
			ConsecErrorThreshold: 100, PauseMultiplierSec: 1, PauseMaxSec: 1,
		},
	}
}

func newTraversalTestHandler(t *testing.T, channels []config.ChannelConfig) *ProxyHandler {
	t.Helper()
	logger := log.New("error", "", false, false)
	logger.SetEnabled(false)
	cfg := &config.Config{
		Server: config.ServerConfig{MaxRequestBodySizeMB: 1},
		Failover: config.FailoverConfig{
			Enabled: true, TotalTimeoutMs: 2000, LoadBalance: "priority",
		},
		Channels: channels,
	}
	manager := channel.NewChannelManager(cfg.Channels, nil, logger, nil, "priority")
	t.Cleanup(func() {
		manager.Stop()
		logger.Close()
	})
	return NewProxyHandler(manager, nil, logger, cfg, debuglog.New(false), nil, nil, nil)
}

func performTraversalRequest(handler *ProxyHandler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	handler.HandleChat(rec, req)
	return rec
}

func assertTraversalRequestOK(t *testing.T, handler *ProxyHandler) {
	t.Helper()
	rec := performTraversalRequest(handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func assertAttemptOrder(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("attempt count = %d, want %d; got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attempt[%d] = %s, want %s; all=%v", i, got[i], want[i], got)
		}
	}
}
