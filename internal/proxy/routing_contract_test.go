package proxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/debuglog"
	"github.com/fangxiusun/ai-adapter/internal/log"
)

func TestUnknownModelFallbackIsExplicitlyLogged(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(traversalSuccessBody))
	}))
	defer upstream.Close()

	logPath := filepath.Join(t.TempDir(), "fallback.log")
	logger := log.New("warn", logPath, false, false)
	cfg := &config.Config{
		Server:   config.ServerConfig{MaxRequestBodySizeMB: 1},
		Failover: config.FailoverConfig{Enabled: true, TotalTimeoutMs: 1000, LoadBalance: "priority"},
		Channels: []config.ChannelConfig{{
			ID: "default", Name: "default", Enabled: true, ChatURL: upstream.URL,
			DefaultModel: "actual-model",
			Models:       []config.ModelConfig{{ID: "actual-model"}},
			Keys:         []config.KeyConfig{{Value: "key-1"}},
			KeyStrategy:  "least-errors", RequestTimeoutMs: 1000,
			Retry: config.RetryConfig{MaxRotationRounds: 1, MaxTotalWaitMs: 1000},
		}},
	}
	manager := channel.NewChannelManager(cfg.Channels, nil, logger, nil, "priority")
	handler := NewProxyHandler(manager, nil, logger, cfg, debuglog.New(false), nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"unknown-model","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	handler.HandleChat(rec, req)
	manager.Stop()
	logger.Close()

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fallback log: %v", err)
	}
	text := string(data)
	for _, field := range []string{
		"model_route_fallback=true",
		"requested_model=unknown-model",
		"resolved_model=actual-model",
		"fallback_channel=default",
		"fallback_reason=unknown_model",
	} {
		if !strings.Contains(text, field) {
			t.Fatalf("fallback log missing %q: %s", field, text)
		}
	}
}

func TestFanoutBadRequestIsNotWrittenAsRateLimitResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer upstream.Close()

	logger := log.New("error", "", false, false)
	logger.SetEnabled(false)
	defer logger.Close()
	cfg := &config.Config{
		Server:   config.ServerConfig{MaxRequestBodySizeMB: 1},
		Failover: config.FailoverConfig{Enabled: false, TotalTimeoutMs: 1000},
		Channels: []config.ChannelConfig{{
			ID: "fanout", Name: "fanout", Enabled: true, ChatURL: upstream.URL,
			DefaultModel: "test-model",
			Models:       []config.ModelConfig{{ID: "test-model"}},
			Keys:         []config.KeyConfig{{Value: "key-1"}, {Value: "key-2"}},
			KeyStrategy:  "least-errors", RequestTimeoutMs: 1000,
			Fanout: config.FanoutConfig{Enabled: true, Count: 2},
			Retry:  config.RetryConfig{MaxRotationRounds: 1, MaxTotalWaitMs: 1000},
		}},
	}
	manager := channel.NewChannelManager(cfg.Channels, nil, logger, nil, "priority")
	defer manager.Stop()
	handler := NewProxyHandler(manager, nil, logger, cfg, debuglog.New(false), nil, nil, nil)

	rec := performTraversalRequest(handler)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	var mappedRateLimits int64
	for _, keyStats := range manager.ListChannels()[0].KeyPool().GetStats() {
		mappedRateLimits += keyStats.Error429
	}
	if mappedRateLimits != 2 {
		t.Fatalf("mapped key rate-limit count = %d, want 2", mappedRateLimits)
	}
}
