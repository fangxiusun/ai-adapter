package proxy

import (
	"encoding/json"
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

func TestHandleModelsRejectsNonGetRequests(t *testing.T) {
	logger := log.New("error", "", false, false)
	logger.SetEnabled(false)

	cfg := &config.Config{
		Channels: []config.ChannelConfig{{
			ID:           "ch-1",
			Name:         "ch-1",
			Enabled:      true,
			DefaultModel: "m-1",
			Models:       []config.ModelConfig{{ID: "m-1", DisplayName: "Model One", ContextWindow: 1024, MaxOutputTokens: 256}},
		}},
	}

	cm := channel.NewChannelManager(cfg.Channels, nil, logger, nil, "priority")
	handler := &ProxyHandler{channels: cm, logger: logger, config: cfg}

	req := httptest.NewRequest(http.MethodPost, "/v1/models", nil)
	rec := httptest.NewRecorder()

	handler.HandleModels(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusMethodNotAllowed, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "method_not_allowed") {
		t.Fatalf("expected method_not_allowed message, got %q", rec.Body.String())
	}
}

func TestHandleModelsReturnsConfiguredModels(t *testing.T) {
	logger := log.New("error", "", false, false)
	logger.SetEnabled(false)

	modelCfg := &config.Config{
		Server: config.ServerConfig{APIToken: "sk-test-token"},
		Channels: []config.ChannelConfig{{
			ID:           "ch-1",
			Name:         "ch-1",
			Enabled:      true,
			DefaultModel: "m-1",
			Models: []config.ModelConfig{
				{ID: "m-1", DisplayName: "Model One", ContextWindow: 1024, MaxOutputTokens: 256},
				{ID: "m-2", ContextWindow: 2048},
			},
		}, {
			ID:           "ch-2",
			Name:         "ch-2",
			Enabled:      true,
			DefaultModel: "m-3",
			Models: []config.ModelConfig{
				{ID: "m-3", DisplayName: "Model Three", ContextWindow: 4096, MaxOutputTokens: 512},
				{ID: "m-1", DisplayName: "Model One Dup"},
			},
		}},
	}

	cm := channel.NewChannelManager(modelCfg.Channels, nil, logger, nil, "priority")
	handler := NewProxyHandler(cm, nil, logger, modelCfg, debuglog.New(false), nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-test-token")
	rec := httptest.NewRecorder()

	handler.HandleModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json", ct)
	}

	var payload struct {
		Data    []struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			Object          string `json:"object"`
			Created         int64 `json:"created"`
			OwnedBy         string `json:"owned_by"`
			ContextLength   int `json:"context_length"`
			MaxOutputLength int `json:"max_output_length"`
		} `json:"data"`
		Object  string `json:"object"`
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Object != "list" || !payload.Success {
		t.Fatalf("unexpected envelope: object=%s success=%v", payload.Object, payload.Success)
	}
	if len(payload.Data) != 3 {
		t.Fatalf("models len = %d, want 3; body=%s", len(payload.Data), rec.Body.String())
	}

	want := map[string]struct {
		Name          string
		OwnedBy       string
		ContextLength int
		MaxOutput     int
	}{
		"m-1": {Name: "Model One", OwnedBy: "ch-1", ContextLength: 1024, MaxOutput: 256},
		"m-2": {Name: "m-2", OwnedBy: "ch-1", ContextLength: 2048},
		"m-3": {Name: "Model Three", OwnedBy: "ch-2", ContextLength: 4096, MaxOutput: 512},
	}

	for _, item := range payload.Data {
		expected, ok := want[item.ID]
		if !ok {
			t.Fatalf("unexpected model id %s", item.ID)
		}
		if item.Name != expected.Name || item.OwnedBy != expected.OwnedBy || item.ContextLength != expected.ContextLength || item.MaxOutputLength != expected.MaxOutput {
			t.Fatalf("model %s mismatch: got %+v, want %+v", item.ID, item, expected)
		}
		if item.Object != "model" || item.Created == 0 {
			t.Fatalf("model %s missing meta fields: object=%s created=%d", item.ID, item.Object, item.Created)
		}
	}
}


