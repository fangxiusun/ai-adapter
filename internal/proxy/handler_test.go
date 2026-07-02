package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/debuglog"
	"github.com/fangxiusun/ai-adapter/internal/log"
)

func TestHandleChatReturnsErrorWhenSingleChannelRequestFails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer upstream.Close()

	logger := log.New("error", "", false, false)
	logger.SetEnabled(false)

	cfg := &config.Config{
		Server: config.ServerConfig{MaxRequestBodySizeMB: 1},
		Failover: config.FailoverConfig{
			Enabled:                  false,
			MaxChannelAttempts:       1,
			TotalTimeoutMs:           1000,
			ConsecutiveFailThreshold: 1,
		},
		Channels: []config.ChannelConfig{{
			ID:               "test-channel",
			Name:             "test-channel",
			Enabled:          true,
			DefaultModel:     "test-model",
			Models:           []config.ModelConfig{{ID: "test-model", DisplayName: "test-model"}},
			Keys:             []config.KeyConfig{{Value: "sk-test-401", Name: "key-1"}},
			KeyStrategy:      "round-robin",
			RequestTimeoutMs: 1000,
			Retry: config.RetryConfig{
				RetryDelay429Ms:      1,
				MaxRotationRounds:    1,
				MaxTotalWaitMs:       1000,
				ConsecErrorThreshold: 1,
				PauseMultiplierSec:   1,
				PauseMaxSec:          1,
			},
			ChatURL: upstream.URL,
		}},
	}

	cm := channel.NewChannelManager(cfg.Channels, nil, logger, nil, "priority")
	handler := &ProxyHandler{
		channels:  cm,
		logger:    logger,
		config:    cfg,
		deepDebug: debuglog.New(false),
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()

	handler.HandleChat(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("expected json error body, got %q", rec.Body.String())
	}
}
