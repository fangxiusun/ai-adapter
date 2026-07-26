package proxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/db"
	"github.com/fangxiusun/ai-adapter/internal/debuglog"
	"github.com/fangxiusun/ai-adapter/internal/log"
	"github.com/fangxiusun/ai-adapter/internal/stats"
	"github.com/fangxiusun/ai-adapter/internal/translate"
)

func TestInvalidRequestIsRecordedInDBAndStats(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	logger := log.New("error", "", false, false)
	logger.SetEnabled(false)
	defer logger.Close()
	memoryStats := stats.NewStats()
	handler := NewProxyHandler(nil, database, logger, &config.Config{
		Server: config.ServerConfig{MaxRequestBodySizeMB: 1},
	}, debuglog.New(false), nil, memoryStats, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":`))
	rec := httptest.NewRecorder()
	handler.HandleChat(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	count, err := database.GetLogCount()
	if err != nil || count != 1 {
		t.Fatalf("database log count = %d, err=%v; want 1", count, err)
	}
	logs, err := database.QueryLogs("", 0, 0, 0, 0, 10, 0)
	if err != nil || len(logs) != 1 {
		t.Fatalf("query logs returned %d entries, err=%v", len(logs), err)
	}
	if logs[0].StatusCode != http.StatusBadRequest || logs[0].ErrorCode != "invalid_json" {
		t.Fatalf("unexpected error log: %+v", logs[0])
	}
	if rate := memoryStats.GetErrorRate(); rate != 1 {
		t.Fatalf("error rate = %v, want 1", rate)
	}
}

func TestNativeGeminiAliasUsesUpstreamModelInURL(t *testing.T) {
	var path string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer upstream.Close()

	logger := log.New("error", "", false, false)
	logger.SetEnabled(false)
	cfg := &config.Config{
		Server:   config.ServerConfig{MaxRequestBodySizeMB: 1},
		Failover: config.FailoverConfig{TotalTimeoutMs: 1000},
		Channels: []config.ChannelConfig{{
			ID:                 "gemini",
			Enabled:            true,
			Priority:           1,
			GenerateContentURL: upstream.URL,
			DefaultModel:       "gemini-upstream",
			Models:             []config.ModelConfig{{ID: "gemini-upstream", Aliases: []string{"gemini-alias"}}},
			Keys:               []config.KeyConfig{{Value: "key-1"}},
			RequestTimeoutMs:   1000,
			Retry:              config.RetryConfig{MaxRotationRounds: 1, MaxTotalWaitMs: 1000, RetryDelay429Ms: 1},
			KeyStatsSyncSec:    0,
		}},
	}
	manager := channel.NewChannelManager(cfg.Channels, nil, logger, nil, "priority")
	t.Cleanup(func() {
		manager.Stop()
		logger.Close()
	})
	handler := NewProxyHandler(manager, nil, logger, cfg, debuglog.New(false), nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-alias:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	rec := httptest.NewRecorder()
	handler.HandleGenerateContent(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if path != "/v1beta/models/gemini-upstream:generateContent" {
		t.Fatalf("upstream path = %q", path)
	}
}

func TestStreamConversionFailureIsNotReportedAsChannelSuccess(t *testing.T) {
	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {not-json}\n\n"))
	}))
	defer malformed.Close()
	var fallbackCalls atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fallback.Close()

	logger := log.New("error", "", false, false)
	logger.SetEnabled(false)
	retry := config.RetryConfig{
		RetryDelay429Ms: 1, MaxRotationRounds: 1, MaxTotalWaitMs: 1000,
		ConsecErrorThreshold: 10, PauseMultiplierSec: 1, PauseMaxSec: 1,
	}
	cfg := &config.Config{
		Server:   config.ServerConfig{MaxRequestBodySizeMB: 1},
		Failover: config.FailoverConfig{Enabled: true, TotalTimeoutMs: 1000},
		Channels: []config.ChannelConfig{
			{ID: "primary", Enabled: true, Priority: 1, ChatURL: malformed.URL, Models: []config.ModelConfig{{ID: "model"}}, Keys: []config.KeyConfig{{Value: "key-1"}}, RequestTimeoutMs: 1000, Retry: retry},
			{ID: "fallback", Enabled: true, Priority: 2, ChatURL: fallback.URL, Models: []config.ModelConfig{{ID: "model"}}, Keys: []config.KeyConfig{{Value: "key-2"}}, RequestTimeoutMs: 1000, Retry: retry},
		},
	}
	manager := channel.NewChannelManager(cfg.Channels, nil, logger, nil, "priority")
	t.Cleanup(func() {
		manager.Stop()
		logger.Close()
	})
	primary, _ := manager.GetChannel("primary")
	primary.ReportChannelFailure()
	primary.ReportChannelFailure()
	handler := NewProxyHandler(manager, nil, logger, cfg, debuglog.New(false), nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","input":"hi","stream":true}`))
	rec := httptest.NewRecorder()
	handler.HandleResponses(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("committed stream status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if fallbackCalls.Load() != 0 {
		t.Fatalf("fallback channel was called after a committed stream: %d", fallbackCalls.Load())
	}
	keyStats := primary.KeyPool().GetStats()
	if len(keyStats) != 1 || keyStats[0].ErrorStream != 1 || keyStats[0].RequestCount != 1 || !keyStats[0].LastSuccessTime.IsZero() {
		t.Fatalf("stream failure was not recorded correctly: %+v", keyStats)
	}
	primary.ReportChannelFailure()
	if primary.IsHealthy() {
		t.Fatal("stream failure incorrectly reset the channel consecutive-failure state")
	}
}

func TestPipeChatTargetFlushesWrites(t *testing.T) {
	var sink bytes.Buffer
	flushes := 0
	handler := &ProxyHandler{}
	err := handler.pipeChatStreamToTarget(
		context.Background(),
		config.InterfaceChat,
		strings.NewReader("data: first\n\n"),
		&sink,
		&translate.ChatRequest{},
		nil,
		func() { flushes++ },
	)
	if err != nil {
		t.Fatalf("pipeChatStreamToTarget: %v", err)
	}
	if flushes == 0 {
		t.Fatal("chat stream was copied without flushing")
	}
	if sink.String() != "data: first\n\n" {
		t.Fatalf("unexpected stream output: %q", sink.String())
	}
}
