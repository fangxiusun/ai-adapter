package channel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/log"
)

func TestFanoutFastReturnReportsFailuresBeforeWinner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer good-key":
			time.Sleep(80 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "Bearer bad-key":
			time.Sleep(10 * time.Millisecond)
			http.Error(w, "boom", http.StatusBadGateway)
		default:
			http.Error(w, "unexpected key", http.StatusUnauthorized)
		}
	}))
	defer server.Close()

	ch := newTestFanoutChannel(server.URL, false)

	result := ch.Fanout(context.Background(), FanoutRequest{
		Body:    []byte(`{"input":"ping"}`),
		URL:     server.URL,
		Headers: make(http.Header),
	})
	if result == nil || result.Error != nil {
		t.Fatalf("expected success result, got %#v", result)
	}
	if result.Key != "good-key" {
		t.Fatalf("expected good-key winner, got %q", result.Key)
	}

	stats := fanoutStatsByKey(ch)
	if stats["bad-key"].Error5xx != 1 {
		t.Fatalf("expected bad-key 5xx to be recorded, stats=%+v", stats["bad-key"])
	}
	if stats["good-key"].LastSuccessTime.IsZero() {
		t.Fatalf("expected good-key success to be recorded, stats=%+v", stats["good-key"])
	}
}

func TestFanoutFastReturnReportsAllFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer server.Close()

	ch := newTestFanoutChannel(server.URL, false)

	result := ch.Fanout(context.Background(), FanoutRequest{
		Body:    []byte(`{"input":"ping"}`),
		URL:     server.URL,
		Headers: make(http.Header),
	})
	if result == nil || result.Error == nil {
		t.Fatalf("expected all-failed error, got %#v", result)
	}

	waitForFanoutStat(t, time.Second, func(stats map[string]KeyStats) bool {
		return stats["good-key"].Error5xx == 1 && stats["bad-key"].Error5xx == 1
	}, ch)
}

func TestFanoutMapsBadRequestToRateLimitStats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	ch := newTestFanoutChannel(server.URL, false)
	result := ch.Fanout(context.Background(), FanoutRequest{Body: []byte(`{"input":"ping"}`), URL: server.URL, Headers: make(http.Header)})
	if result == nil || result.Error == nil || result.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected preserved 400 failure, got %#v", result)
	}
	stats := fanoutStatsByKey(ch)
	for _, key := range []string{"good-key", "bad-key"} {
		if stats[key].Error400 != 0 || stats[key].Error429 != 1 {
			t.Fatalf("expected %s 400 to count as 429, stats=%+v", key, stats[key])
		}
	}
}

func TestFanoutStreamKeepsWinnerReadableAndIgnoresCanceledLoser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer good-key":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			time.Sleep(30 * time.Millisecond)
			_, _ = io.WriteString(w, "data: ok\n\n")
		case "Bearer bad-key":
			time.Sleep(100 * time.Millisecond)
			http.Error(w, "late failure", http.StatusBadGateway)
		}
	}))
	defer server.Close()

	ch := newTestFanoutChannel(server.URL, false)
	result := ch.FanoutStream(context.Background(), FanoutRequest{Body: []byte(`{"input":"ping"}`), URL: server.URL, Headers: make(http.Header)})
	if result == nil || result.Error != nil || result.Response == nil {
		t.Fatalf("expected stream winner, got %#v", result)
	}
	body, err := io.ReadAll(result.Response.Body)
	_ = result.Response.Body.Close()
	if err != nil || string(body) != "data: ok\n\n" {
		t.Fatalf("winner body = %q, err=%v", body, err)
	}

	time.Sleep(150 * time.Millisecond)
	stats := fanoutStatsByKey(ch)
	if stats["bad-key"].ErrorCount != 0 {
		t.Fatalf("canceled loser must not be reported as an error, stats=%+v", stats["bad-key"])
	}
}

func newTestFanoutChannel(targetURL string, waitAll bool) *Channel {
	logger := log.New("error", "", false, false)
	logger.SetEnabled(false)

	return newChannel(config.ChannelConfig{
		ID:               "test-channel",
		Name:             "test-channel",
		Enabled:          true,
		DefaultModel:     "test-model",
		Models:           []config.ModelConfig{{ID: "test-model", DisplayName: "test-model"}},
		Keys:             []config.KeyConfig{{Value: "good-key", Name: "good-key"}, {Value: "bad-key", Name: "bad-key"}},
		KeyStrategy:      "round-robin",
		RequestTimeoutMs: 1000,
		KeyStatsSyncSec:  0,
		Retry: config.RetryConfig{
			ConsecErrorThreshold: 3,
			PauseMultiplierSec:   30,
			PauseMaxSec:          600,
		},
		Fanout: config.FanoutConfig{
			Enabled: true,
			Count:   2,
			WaitAll: waitAll,
		},
		ChatURL: targetURL,
	}, nil, logger, nil)
}

func fanoutStatsByKey(ch *Channel) map[string]KeyStats {
	stats := make(map[string]KeyStats)
	for _, stat := range ch.KeyPool().GetStats() {
		stats[stat.Value] = stat
	}
	return stats
}

func waitForFanoutStat(t *testing.T, timeout time.Duration, predicate func(map[string]KeyStats) bool, ch *Channel) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		stats := fanoutStatsByKey(ch)
		if predicate(stats) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("condition not met before timeout, stats=%+v", fanoutStatsByKey(ch))
}
