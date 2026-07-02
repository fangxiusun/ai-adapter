package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fangxiusun/ai-adapter/internal/config"
)

func TestHandleConfigMasksSensitiveServerTokens(t *testing.T) {
	handler := &WebHandler{
		config: &config.Config{
			Server: config.ServerConfig{
				Host:                 "127.0.0.1",
				Port:                 8080,
				APIToken:             "sk-live-1234567890abcdef",
				AdminToken:           "adm-1234567890abcdef",
				MaxRequestBodySizeMB: 32,
			},
			Logging: config.LoggingConfig{
				Level:          "info",
				File:           "logs/app.log",
				LogRequestBody: true,
			},
			Channels: []config.ChannelConfig{{ID: "c1"}, {ID: "c2"}},
			Proxies: []config.ProxyConfig{{
				ID:   "proxy-1",
				Type: "socks5",
				URL:  "socks5://user:secret@127.0.0.1:1080",
			}},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/api/config", nil)
	rec := httptest.NewRecorder()

	handler.handleConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if strings.Contains(body, "sk-live-1234567890abcdef") {
		t.Fatal("response leaked raw api_token")
	}
	if strings.Contains(body, "adm-1234567890abcdef") {
		t.Fatal("response leaked raw admin_token")
	}

	var got struct {
		Server  map[string]any `json:"server"`
		Logging struct {
			Level          string `json:"level"`
			File           string `json:"file"`
			LogRequestBody bool   `json:"log_request_body"`
		} `json:"logging"`
		Channels int `json:"channels"`
		Proxies  []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"proxies"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if _, ok := got.Server["api_token"]; ok {
		t.Fatal("server.api_token should not be returned")
	}
	if _, ok := got.Server["admin_token"]; ok {
		t.Fatal("server.admin_token should not be returned")
	}
	if got.Server["api_token_configured"] != true {
		t.Fatalf("server.api_token_configured = %#v, want true", got.Server["api_token_configured"])
	}
	if got.Server["admin_token_configured"] != true {
		t.Fatalf("server.admin_token_configured = %#v, want true", got.Server["admin_token_configured"])
	}
	if got.Server["host"] != "127.0.0.1" || got.Server["port"] != float64(8080) || got.Server["max_request_body_size_mb"] != float64(32) {
		t.Fatalf("unexpected server summary: %+v", got.Server)
	}
	if got.Logging.Level != "info" || got.Logging.File != "logs/app.log" || !got.Logging.LogRequestBody {
		t.Fatalf("unexpected logging summary: %+v", got.Logging)
	}
	if got.Channels != 2 {
		t.Fatalf("channels = %d, want 2", got.Channels)
	}
	if len(got.Proxies) != 1 {
		t.Fatalf("proxies len = %d, want 1", len(got.Proxies))
	}
	if strings.Contains(got.Proxies[0].URL, "secret") {
		t.Fatalf("proxy url leaked raw credentials: %q", got.Proxies[0].URL)
	}
}
