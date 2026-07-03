package debug

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
)

func TestCurlChatToResponsesUsesProductionResponsesConversion(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:     "127.0.0.1",
			Port:     8080,
			APIToken: "debug-token",
		},
		Channels: []config.ChannelConfig{
			{
				ID:           "responses-only",
				Enabled:      true,
				ResponsesURL: "https://upstream.example",
				Models: []config.ModelConfig{
					{ID: "test-model"},
				},
				DefaultModel: "test-model",
				Keys: []config.KeyConfig{
					{Value: "test-key"},
				},
			},
		},
	}
	cm := channel.NewChannelManager(cfg.Channels, nil, nil, nil, "priority")
	h := NewHandler(cm, cfg)

	reqBody := `{
		"model": "test-model",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": "answer", "reasoning_content": "think"}
		],
		"reasoning_effort": "high",
		"stream": false
	}`
	req := httptest.NewRequest(http.MethodPost, "/curl/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleCurlChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	upstreamBody := extractCurlJSONBody(t, rec.Body.String())

	reasoning, ok := upstreamBody["reasoning"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing reasoning config in converted body: %#v", upstreamBody)
	}
	if got := reasoning["effort"]; got != "high" {
		t.Fatalf("reasoning.effort = %v, want high", got)
	}

	input, ok := upstreamBody["input"].([]interface{})
	if !ok {
		t.Fatalf("input = %#v, want array", upstreamBody["input"])
	}
	for _, raw := range input {
		item, ok := raw.(map[string]interface{})
		if !ok || item["type"] != "reasoning" {
			continue
		}
		if got := item["encrypted_content"]; got != "think" {
			t.Fatalf("reasoning encrypted_content = %v, want think", got)
		}
		return
	}
	t.Fatalf("missing reasoning input item in converted input: %#v", input)
}

func extractCurlJSONBody(t *testing.T, curlCmd string) map[string]interface{} {
	t.Helper()

	const marker = "-d '"
	idx := strings.Index(curlCmd, marker)
	if idx < 0 {
		t.Fatalf("curl command missing JSON body: %s", curlCmd)
	}
	body := curlCmd[idx+len(marker):]
	end := strings.LastIndex(body, "'")
	if end < 0 {
		t.Fatalf("curl command JSON body is not closed: %s", curlCmd)
	}
	body = body[:end]

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("unmarshal curl JSON body: %v\nbody: %s", err, body)
	}
	return decoded
}
