package proxy

import (
	"testing"

	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/translate"
)

func TestExtractUsageFromRawBody_Chat(t *testing.T) {
	body := []byte(`{"id":"1","model":"gpt-4","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`)
	pt, ct, tt, usageJSON := extractUsageFromRawBody(config.InterfaceChat, body)
	if pt != 100 || ct != 50 || tt != 150 {
		t.Errorf("got pt=%d ct=%d tt=%d, want 100/50/150", pt, ct, tt)
	}
	if usageJSON == "" {
		t.Error("usageJSON should not be empty")
	}
}

func TestExtractUsageFromRawBody_Responses(t *testing.T) {
	body := []byte(`{"id":"1","usage":{"input_tokens":200,"output_tokens":80,"total_tokens":280}}`)
	pt, ct, tt, _ := extractUsageFromRawBody(config.InterfaceResponses, body)
	if pt != 200 || ct != 80 || tt != 280 {
		t.Errorf("got pt=%d ct=%d tt=%d, want 200/80/280", pt, ct, tt)
	}
}

func TestExtractUsageFromRawBody_Claude(t *testing.T) {
	body := []byte(`{"id":"1","usage":{"input_tokens":300,"output_tokens":120}}`)
	pt, ct, tt, _ := extractUsageFromRawBody(config.InterfaceMessages, body)
	if pt != 300 || ct != 120 || tt != 420 {
		t.Errorf("got pt=%d ct=%d tt=%d, want 300/120/420", pt, ct, tt)
	}
}

func TestExtractUsageFromRawBody_Gemini(t *testing.T) {
	body := []byte(`{"candidates":[],"usageMetadata":{"promptTokenCount":500,"candidatesTokenCount":200,"totalTokenCount":700}}`)
	pt, ct, tt, _ := extractUsageFromRawBody(config.InterfaceGenerateContent, body)
	if pt != 500 || ct != 200 || tt != 700 {
		t.Errorf("got pt=%d ct=%d tt=%d, want 500/200/700", pt, ct, tt)
	}
}

func TestExtractUsageFromRawBody_EmptyBody(t *testing.T) {
	pt, ct, tt, usageJSON := extractUsageFromRawBody(config.InterfaceChat, nil)
	if pt != 0 || ct != 0 || tt != 0 {
		t.Errorf("got pt=%d ct=%d tt=%d, want 0/0/0", pt, ct, tt)
	}
	if usageJSON != "" {
		t.Error("usageJSON should be empty for nil body")
	}
}

func TestExtractUsageFromRawBody_InvalidJSON(t *testing.T) {
	pt, ct, tt, usageJSON := extractUsageFromRawBody(config.InterfaceChat, []byte("not json"))
	if pt != 0 || ct != 0 || tt != 0 {
		t.Errorf("got pt=%d ct=%d tt=%d, want 0/0/0", pt, ct, tt)
	}
	if usageJSON != "" {
		t.Error("usageJSON should be empty for invalid JSON")
	}
}

func TestExtractUsageFromRawBody_NoUsage(t *testing.T) {
	body := []byte(`{"id":"1","choices":[{"message":{"content":"hi"}}]}`)
	pt, ct, tt, usageJSON := extractUsageFromRawBody(config.InterfaceChat, body)
	if pt != 0 || ct != 0 || tt != 0 {
		t.Errorf("got pt=%d ct=%d tt=%d, want 0/0/0", pt, ct, tt)
	}
	if usageJSON != "" {
		t.Error("usageJSON should be empty when no usage")
	}
}

func TestNormalizeUsage(t *testing.T) {
	usage := &translate.ChatUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	pt, ct, tt, usageJSON := normalizeUsage(usage)
	if pt != 100 || ct != 50 || tt != 150 {
		t.Errorf("got pt=%d ct=%d tt=%d, want 100/50/150", pt, ct, tt)
	}
	if usageJSON == "" {
		t.Error("usageJSON should not be empty")
	}
}

func TestNormalizeUsage_Nil(t *testing.T) {
	pt, ct, tt, usageJSON := normalizeUsage(nil)
	if pt != 0 || ct != 0 || tt != 0 {
		t.Errorf("got pt=%d ct=%d tt=%d, want 0/0/0", pt, ct, tt)
	}
	if usageJSON != "" {
		t.Error("usageJSON should be empty for nil usage")
	}
}

func TestInjectStreamOptions_NoExisting(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[],"stream":true}`)
	result := injectStreamOptions(body)
	if !contains(result, `"include_usage":true`) {
		t.Errorf("expected include_usage:true in %s", string(result))
	}
}

func TestInjectStreamOptions_AlreadyTrue(t *testing.T) {
	body := []byte(`{"model":"gpt-4","stream":true,"stream_options":{"include_usage":true}}`)
	result := injectStreamOptions(body)
	if !contains(result, `"include_usage":true`) {
		t.Errorf("expected include_usage:true in %s", string(result))
	}
}

func TestInjectStreamOptions_ExplicitlyFalse(t *testing.T) {
	body := []byte(`{"model":"gpt-4","stream":true,"stream_options":{"include_usage":false}}`)
	result := injectStreamOptions(body)
	if contains(result, `"include_usage":true`) {
		t.Errorf("should not override explicit false, got %s", string(result))
	}
}

func contains(data []byte, s string) bool {
	return len(data) > 0 && len(s) > 0 && len(data) >= len(s) && findSubstring(data, s)
}

func findSubstring(data []byte, s string) bool {
	for i := 0; i <= len(data)-len(s); i++ {
		if string(data[i:i+len(s)]) == s {
			return true
		}
	}
	return false
}
