package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/debuglog"
	"github.com/fangxiusun/ai-adapter/internal/log"
)

func TestUpstreamBadRequestIsMappedToRateLimit(t *testing.T) {
	const upstreamReason = `{"error":{"message":"real upstream reason"}}`

	tests := []struct {
		name     string
		source   config.InterfaceType
		target   config.InterfaceType
		endpoint string
		body     string
	}{
		{
			name:     "native non-stream",
			source:   config.InterfaceChat,
			target:   config.InterfaceChat,
			endpoint: "/v1/chat/completions",
			body:     `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name:     "native stream",
			source:   config.InterfaceChat,
			target:   config.InterfaceChat,
			endpoint: "/v1/chat/completions",
			body:     `{"model":"test-model","messages":[{"role":"user","content":"hi"}],"stream":true}`,
		},
		{
			name:     "converted non-stream",
			source:   config.InterfaceChat,
			target:   config.InterfaceResponses,
			endpoint: "/v1/responses",
			body:     `{"model":"test-model","input":"hi"}`,
		},
		{
			name:     "converted stream from chat",
			source:   config.InterfaceChat,
			target:   config.InterfaceResponses,
			endpoint: "/v1/responses",
			body:     `{"model":"test-model","input":"hi","stream":true}`,
		},
		{
			name:     "converted stream chain",
			source:   config.InterfaceResponses,
			target:   config.InterfaceChat,
			endpoint: "/v1/chat/completions",
			body:     `{"model":"test-model","messages":[{"role":"user","content":"hi"}],"stream":true}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(upstreamReason))
			}))
			defer upstream.Close()

			logPath := filepath.Join(t.TempDir(), "proxy.log")
			logger := log.New("warn", logPath, false, false)

			channelConfig := config.ChannelConfig{
				ID:               "test-channel",
				Name:             "test-channel",
				Enabled:          true,
				DefaultModel:     "test-model",
				Models:           []config.ModelConfig{{ID: "test-model", DisplayName: "test-model"}},
				Keys:             []config.KeyConfig{{Value: "sk-test", Name: "key-1"}},
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
			}
			switch tt.source {
			case config.InterfaceChat:
				channelConfig.ChatURL = upstream.URL
			case config.InterfaceResponses:
				channelConfig.ResponsesURL = upstream.URL
			default:
				t.Fatalf("unsupported test source: %s", tt.source)
			}

			cfg := &config.Config{
				Server: config.ServerConfig{MaxRequestBodySizeMB: 1},
				Failover: config.FailoverConfig{
					Enabled:                  false,
					MaxChannelAttempts:       1,
					TotalTimeoutMs:           1000,
					ConsecutiveFailThreshold: 1,
				},
				Channels: []config.ChannelConfig{channelConfig},
			}
			cm := channel.NewChannelManager(cfg.Channels, nil, logger, nil, "priority")
			handler := NewProxyHandler(cm, nil, logger, cfg, debuglog.New(false), nil, nil, nil)

			req := httptest.NewRequest(http.MethodPost, tt.endpoint, strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			switch tt.target {
			case config.InterfaceChat:
				handler.HandleChat(rec, req)
			case config.InterfaceResponses:
				handler.HandleResponses(rec, req)
			default:
				t.Fatalf("unsupported test target: %s", tt.target)
			}

			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusTooManyRequests, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), upstreamBadRequestErrorCode) {
				t.Fatalf("response does not contain error code %q: %s", upstreamBadRequestErrorCode, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "real upstream reason") {
				t.Fatalf("response does not preserve upstream reason: %s", rec.Body.String())
			}

			logger.Close()
			logBytes, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read log: %v", err)
			}
			logText := string(logBytes)
			for _, expected := range []string{
				"upstream_status=400",
				"client_status=429",
				"error_reason=",
				"real upstream reason",
				"error_code=upstream_bad_request",
			} {
				if !strings.Contains(logText, expected) {
					t.Errorf("log does not contain %q: %s", expected, logText)
				}
			}
		})
	}
}

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
	remaining := 0
	handler.requestLogs.Range(func(_, _ any) bool {
		remaining++
		return true
	})
	if remaining != 0 {
		t.Fatalf("request log metadata leaked after request: %d", remaining)
	}
}

func TestChannelHealthCountsOnlyServerAndConnectionFailures(t *testing.T) {
	tests := []struct {
		name             string
		primaryStatus    int
		wantPrimaryCalls int32
	}{
		{name: "client errors do not affect channel health", primaryStatus: http.StatusForbidden, wantPrimaryCalls: 4},
		{name: "server errors affect channel health", primaryStatus: http.StatusBadGateway, wantPrimaryCalls: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var primaryCalls atomic.Int32
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryCalls.Add(1)
				http.Error(w, http.StatusText(tt.primaryStatus), tt.primaryStatus)
			}))
			defer primary.Close()

			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer backup.Close()

			logger := log.New("error", "", false, false)
			logger.SetEnabled(false)
			channelConfig := func(id, target string, priority int) config.ChannelConfig {
				return config.ChannelConfig{
					ID: id, Name: id, Enabled: true, Priority: priority,
					DefaultModel: "test-model",
					Models:       []config.ModelConfig{{ID: "test-model", DisplayName: "test-model"}},
					Keys:         []config.KeyConfig{{Value: "sk-" + id, Name: "key-1"}},
					KeyStrategy:  "round-robin", RequestTimeoutMs: 1000, ChatURL: target,
					Retry: config.RetryConfig{RetryDelay429Ms: 1, MaxRotationRounds: 1, MaxTotalWaitMs: 1000, ConsecErrorThreshold: 100, PauseMultiplierSec: 1, PauseMaxSec: 1},
				}
			}
			cfg := &config.Config{
				Server:   config.ServerConfig{MaxRequestBodySizeMB: 1},
				Failover: config.FailoverConfig{Enabled: true, MaxChannelAttempts: 2, TotalTimeoutMs: 2000, ConsecutiveFailThreshold: 1, LoadBalance: "priority"},
				Channels: []config.ChannelConfig{channelConfig("primary", primary.URL, 1), channelConfig("backup", backup.URL, 2)},
			}
			cm := channel.NewChannelManager(cfg.Channels, nil, logger, nil, "priority")
			handler := NewProxyHandler(cm, nil, logger, cfg, debuglog.New(false), nil, nil, nil)

			for i := 0; i < 4; i++ {
				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`))
				rec := httptest.NewRecorder()
				handler.HandleChat(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("request %d status = %d, want 200; body=%s", i+1, rec.Code, rec.Body.String())
				}
			}
			if got := primaryCalls.Load(); got != tt.wantPrimaryCalls {
				t.Fatalf("primary calls = %d, want %d", got, tt.wantPrimaryCalls)
			}
		})
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
		Data []struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			Object          string `json:"object"`
			Created         int64  `json:"created"`
			OwnedBy         string `json:"owned_by"`
			ContextLength   int    `json:"context_length"`
			MaxOutputLength int    `json:"max_output_length"`
		} `json:"data"`
		Object  string `json:"object"`
		Success bool   `json:"success"`
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

func TestHandleResponsesConvertedFanoutReturnsResponsesFormat(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		if _, ok := payload["messages"]; !ok {
			t.Fatalf("upstream request should be converted to chat format, got %v", payload)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":123,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
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
			Keys:             []config.KeyConfig{{Value: "sk-test-1", Name: "key-1"}, {Value: "sk-test-2", Name: "key-2"}},
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
			Fanout:  config.FanoutConfig{Enabled: true, Count: 2},
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"ping"}`))
	rec := httptest.NewRecorder()

	handler.HandleResponses(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Object string `json:"object"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v; body=%s", err, rec.Body.String())
	}
	if payload.Object != "response" {
		t.Fatalf("object = %q, want response; body=%s", payload.Object, rec.Body.String())
	}
	if len(payload.Output) != 1 || payload.Output[0].Type != "message" || payload.Output[0].Role != "assistant" {
		t.Fatalf("unexpected output: %+v; body=%s", payload.Output, rec.Body.String())
	}
	if len(payload.Output[0].Content) != 1 || payload.Output[0].Content[0].Text != "pong" {
		t.Fatalf("unexpected content: %+v; body=%s", payload.Output[0].Content, rec.Body.String())
	}
}
