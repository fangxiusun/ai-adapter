package headerpolicy

import (
	"net/http"
	"testing"

	"github.com/fangxiusun/ai-adapter/internal/config"
)

// ==================== Matcher Tests ====================

func TestMatchExact(t *testing.T) {
	tests := []struct {
		pattern, key string
		want         bool
	}{
		{"Content-Type", "content-type", true},
		{"Content-Type", "Content-Type", true},
		{"Authorization", "authorization", true},
		{"X-Custom", "x-custom", true},
		{"X-Custom", "x-other", false},
		{"X-Internal-ID", "x-internal-id", true},
	}
	for _, tt := range tests {
		got := MatchExact(tt.pattern, tt.key)
		if got != tt.want {
			t.Errorf("MatchExact(%q, %q) = %v, want %v", tt.pattern, tt.key, got, tt.want)
		}
	}
}

func TestMatchWildcard(t *testing.T) {
	tests := []struct {
		pattern, key string
		want         bool
	}{
		{"X-Internal-*", "x-internal-id", true},
		{"X-Internal-*", "x-internal-secret", true},
		{"X-Internal-*", "x-other", false},
		{"*-Type", "content-type", true},
		{"*-Type", "accept-type", true},
		{"*-Type", "content-length", false},
		{"X-*-ID", "x-request-id", true},
		{"X-*-ID", "x-response-id", true},
		{"X-*-ID", "x-request-name", false},
		{"*", "anything", true},
		{"X-Custom", "x-custom", true}, // no wildcard, but still matches exactly
	}
	for _, tt := range tests {
		got := MatchWildcard(tt.pattern, tt.key)
		if got != tt.want {
			t.Errorf("MatchWildcard(%q, %q) = %v, want %v", tt.pattern, tt.key, got, tt.want)
		}
	}
}

func TestMatchRegex(t *testing.T) {
	tests := []struct {
		pattern, key string
		want         bool
	}{
		{`^x-internal-.*$`, "x-internal-id", true},
		{`^x-internal-.*$`, "x-other", false},
		{`^x-(request|response)-id$`, "x-request-id", true},
		{`^x-(request|response)-id$`, "x-response-id", true},
		{`^x-(request|response)-id$`, "x-other-id", false},
		{`[invalid`, "anything", false}, // invalid regex
	}
	for _, tt := range tests {
		got := MatchRegex(tt.pattern, tt.key)
		if got != tt.want {
			t.Errorf("MatchRegex(%q, %q) = %v, want %v", tt.pattern, tt.key, got, tt.want)
		}
	}
}

func TestMatch(t *testing.T) {
	tests := []struct {
		matchType config.HeaderMatchType
		pattern   string
		key       string
		want      bool
	}{
		{config.MatchExact, "Content-Type", "content-type", true},
		{config.MatchWildcard, "X-Internal-*", "x-internal-id", true},
		{config.MatchRegex, `^x-custom-.*$`, "x-custom-header", true},
		{config.HeaderMatchType("unknown"), "pattern", "key", false},
	}
	for _, tt := range tests {
		got := Match(tt.matchType, tt.pattern, tt.key)
		if got != tt.want {
			t.Errorf("Match(%q, %q, %q) = %v, want %v", tt.matchType, tt.pattern, tt.key, got, tt.want)
		}
	}
}

// ==================== DetectMatchType Tests ====================

func TestDetectMatchType(t *testing.T) {
	tests := []struct {
		pattern      string
		wantType     config.HeaderMatchType
		wantClean    string
	}{
		{"~^x-.*$", config.MatchRegex, "^x-.*$"},
		{"X-Internal-*", config.MatchWildcard, "X-Internal-*"},
		{"X-*-ID", config.MatchWildcard, "X-*-ID"},
		{"*-Type", config.MatchWildcard, "*-Type"},
		{"Authorization", config.MatchExact, "Authorization"},
		{"Content-Type", config.MatchExact, "Content-Type"},
	}
	for _, tt := range tests {
		gotType, gotClean := config.DetectMatchType(tt.pattern)
		if gotType != tt.wantType || gotClean != tt.wantClean {
			t.Errorf("DetectMatchType(%q) = (%v, %q), want (%v, %q)",
				tt.pattern, gotType, gotClean, tt.wantType, tt.wantClean)
		}
	}
}

// ==================== Safety Rules Tests ====================

func TestSafetyRules_DropAuthorization(t *testing.T) {
	headers := http.Header{
		"Authorization": []string{"Bearer sk-secret-key"},
		"Content-Type":  []string{"application/json"},
		"X-Custom":      []string{"value"},
	}
	applySafetyRules(headers, "request")
	if headers.Get("Authorization") != "" {
		t.Error("Authorization header should be dropped by safety rules")
	}
	if headers.Get("Content-Type") == "" {
		t.Error("Content-Type should NOT be dropped")
	}
	if headers.Get("X-Custom") == "" {
		t.Error("X-Custom should NOT be dropped")
	}
}

func TestSafetyRules_DropCookie(t *testing.T) {
	headers := http.Header{
		"Cookie":       []string{"session=abc123"},
		"Content-Type": []string{"application/json"},
	}
	applySafetyRules(headers, "request")
	if headers.Get("Cookie") != "" {
		t.Error("Cookie header should be dropped by safety rules")
	}
}

func TestSafetyRules_DropXInternalWildcard(t *testing.T) {
	headers := http.Header{
		"X-Internal-ID":     []string{"secret-id"},
		"X-Internal-Secret": []string{"secret-value"},
		"X-Custom":          []string{"value"},
	}
	applySafetyRules(headers, "request")
	if headers.Get("X-Internal-ID") != "" {
		t.Error("X-Internal-ID should be dropped by safety rules")
	}
	if headers.Get("X-Internal-Secret") != "" {
		t.Error("X-Internal-Secret should be dropped by safety rules")
	}
	if headers.Get("X-Custom") == "" {
		t.Error("X-Custom should NOT be dropped")
	}
}

func TestSafetyRules_ResponsePhase(t *testing.T) {
	headers := http.Header{
		"Authorization": []string{"Bearer sk-secret-key"},
		"X-Upstream-ID": []string{"upstream-123"},
	}
	applySafetyRules(headers, "response")
	if headers.Get("Authorization") == "" {
		t.Error("Authorization should NOT be dropped in response phase")
	}
}

// ==================== Engine Tests (Simplified Format) ====================

func TestEngine_SimplifiedFormat_Basic(t *testing.T) {
	cfg := &config.Config{
		Headers: &config.HeadersConfig{
			Request: &config.HeaderPolicyConfig{
				Enabled: true,
				Drop:    []string{"X-Debug-*"},
				Set:     map[string]string{"X-Gateway-ID": "ai-adapter"},
			},
		},
	}
	engine := NewEngine(cfg)

	clientHeaders := http.Header{
		"X-Debug-Trace": []string{"true"},
		"X-Custom":      []string{"value"},
	}

	result := engine.ProcessRequest("ch1", "model1", clientHeaders)

	if result.Get("X-Debug-Trace") != "" {
		t.Error("X-Debug-Trace should be dropped")
	}
	if result.Get("X-Gateway-ID") != "ai-adapter" {
		t.Errorf("X-Gateway-ID should be 'ai-adapter', got %q", result.Get("X-Gateway-ID"))
	}
	if result.Get("X-Custom") != "value" {
		t.Error("X-Custom should pass through")
	}
}

func TestEngine_SimplifiedFormat_RegexPrefix(t *testing.T) {
	cfg := &config.Config{
		Headers: &config.HeadersConfig{
			Request: &config.HeaderPolicyConfig{
				Enabled: true,
				Drop:    []string{`~^x-debug-.*$`},
			},
		},
	}
	engine := NewEngine(cfg)

	clientHeaders := http.Header{
		"X-Debug-Trace": []string{"true"},
		"X-Custom":      []string{"value"},
	}

	result := engine.ProcessRequest("ch1", "model1", clientHeaders)

	if result.Get("X-Debug-Trace") != "" {
		t.Error("X-Debug-Trace should be dropped by regex rule")
	}
	if result.Get("X-Custom") == "" {
		t.Error("X-Custom should pass through")
	}
}

func TestEngine_SimplifiedFormat_AllActions(t *testing.T) {
	cfg := &config.Config{
		Headers: &config.HeadersConfig{
			Request: &config.HeaderPolicyConfig{
				Enabled: true,
				Drop:    []string{"X-Drop-Me"},
				Pass:    []string{"X-Pass-Me"},
				Set:     map[string]string{"X-Set-Me": "new-value"},
				Rename:  map[string]string{"X-Rename-Me": "X-Renamed"},
				Append:  map[string]string{"X-Append-Me": "suffix"},
				Prepend: map[string]string{"X-Prepend-Me": "prefix"},
				Copy:    map[string]string{"X-Copy-Me": "X-Copied"},
			},
		},
	}
	engine := NewEngine(cfg)

	clientHeaders := http.Header{
		"X-Drop-Me":    []string{"drop"},
		"X-Pass-Me":    []string{"pass"},
		"X-Set-Me":     []string{"old"},
		"X-Rename-Me":  []string{"rename"},
		"X-Append-Me":  []string{"base"},
		"X-Prepend-Me": []string{"base"},
		"X-Copy-Me":    []string{"copy"},
	}

	result := engine.ProcessRequest("ch1", "model1", clientHeaders)

	if result.Get("X-Drop-Me") != "" {
		t.Error("X-Drop-Me should be dropped")
	}
	if result.Get("X-Pass-Me") != "pass" {
		t.Error("X-Pass-Me should pass through")
	}
	if result.Get("X-Set-Me") != "new-value" {
		t.Errorf("X-Set-Me should be 'new-value', got %q", result.Get("X-Set-Me"))
	}
	if result.Get("X-Rename-Me") != "" {
		t.Error("X-Rename-Me should be removed")
	}
	if result.Get("X-Renamed") != "rename" {
		t.Errorf("X-Renamed should be 'rename', got %q", result.Get("X-Renamed"))
	}
	if result.Get("X-Append-Me") != "base, suffix" {
		t.Errorf("X-Append-Me should be 'base, suffix', got %q", result.Get("X-Append-Me"))
	}
	if result.Get("X-Prepend-Me") != "prefix, base" {
		t.Errorf("X-Prepend-Me should be 'prefix, base', got %q", result.Get("X-Prepend-Me"))
	}
	if result.Get("X-Copy-Me") != "copy" {
		t.Errorf("X-Copy-Me should be 'copy', got %q", result.Get("X-Copy-Me"))
	}
	if result.Get("X-Copied") != "copy" {
		t.Errorf("X-Copied should be 'copy', got %q", result.Get("X-Copied"))
	}
}

func TestEngine_SimplifiedFormat_ResponseHeaders(t *testing.T) {
	cfg := &config.Config{
		Headers: &config.HeadersConfig{
			Response: &config.HeaderPolicyConfig{
				Enabled: true,
				Drop:    []string{"X-Upstream-*"},
			},
		},
	}
	engine := NewEngine(cfg)

	upstreamHeaders := http.Header{
		"Content-Type":       []string{"application/json"},
		"X-Upstream-Request": []string{"req-123"},
		"X-Custom":           []string{"value"},
	}

	result := engine.ProcessResponse("ch1", "model1", upstreamHeaders)

	if result.Get("X-Upstream-Request") != "" {
		t.Error("X-Upstream-Request should be dropped")
	}
	if result.Get("Content-Type") == "" {
		t.Error("Content-Type should pass through")
	}
	if result.Get("X-Custom") == "" {
		t.Error("X-Custom should pass through")
	}
}

// ==================== Engine Tests (Legacy Format) ====================

func TestEngine_ProcessRequest_SafetyRules(t *testing.T) {
	cfg := &config.Config{}
	engine := NewEngine(cfg)

	clientHeaders := http.Header{
		"Authorization":    []string{"Bearer sk-client-key"},
		"Cookie":           []string{"session=abc"},
		"X-Internal-Token": []string{"internal-secret"},
		"Content-Type":     []string{"application/json"},
		"X-Custom":         []string{"custom-value"},
	}

	result := engine.ProcessRequest("ch1", "model1", clientHeaders)

	if result.Get("Authorization") != "" {
		t.Error("Authorization should be dropped")
	}
	if result.Get("Cookie") != "" {
		t.Error("Cookie should be dropped")
	}
	if result.Get("X-Internal-Token") != "" {
		t.Error("X-Internal-Token should be dropped")
	}
	if result.Get("Content-Type") == "" {
		t.Error("Content-Type should pass through")
	}
	if result.Get("X-Custom") == "" {
		t.Error("X-Custom should pass through")
	}
}

func TestEngine_ProcessRequest_GlobalRules(t *testing.T) {
	cfg := &config.Config{
		GlobalRequestHeaders: &config.HeaderPolicyConfig{
			Enabled:       true,
			DefaultAction: config.ActionPass,
			Rules: []config.HeaderRule{
				{
					Name:      "set-gateway-id",
					Phase:     config.PhaseRequest,
					MatchType: config.MatchExact,
					Pattern:   "x-gateway-id",
					Action:    config.ActionSet,
					Value:     "ai-adapter",
				},
			},
		},
	}
	engine := NewEngine(cfg)

	clientHeaders := http.Header{
		"Content-Type": []string{"application/json"},
		"X-Custom":     []string{"value"},
	}

	result := engine.ProcessRequest("ch1", "model1", clientHeaders)

	if result.Get("X-Gateway-Id") != "ai-adapter" {
		t.Errorf("X-Gateway-Id should be 'ai-adapter', got %q", result.Get("X-Gateway-Id"))
	}
	if result.Get("X-Custom") != "value" {
		t.Error("X-Custom should pass through")
	}
}

func TestEngine_ProcessRequest_ChannelOverridesGlobal(t *testing.T) {
	cfg := &config.Config{
		GlobalRequestHeaders: &config.HeaderPolicyConfig{
			Enabled: true,
			Rules: []config.HeaderRule{
				{
					Name:      "global-drop",
					Phase:     config.PhaseRequest,
					MatchType: config.MatchExact,
					Pattern:   "x-custom",
					Action:    config.ActionDrop,
				},
			},
		},
		Channels: []config.ChannelConfig{
			{
				ID: "ch1",
				RequestHeaders: &config.HeaderPolicyConfig{
					Enabled: true,
					Rules: []config.HeaderRule{
						{
							Name:      "channel-pass",
							Phase:     config.PhaseRequest,
							MatchType: config.MatchExact,
							Pattern:   "x-custom",
							Action:    config.ActionPass,
						},
					},
				},
			},
		},
	}
	engine := NewEngine(cfg)

	clientHeaders := http.Header{
		"X-Custom": []string{"value"},
	}

	result := engine.ProcessRequest("ch1", "model1", clientHeaders)

	if result.Get("X-Custom") != "value" {
		t.Errorf("X-Custom should pass through (channel overrides global), got %q", result.Get("X-Custom"))
	}
}

func TestEngine_ProcessRequest_ModelOverridesChannel(t *testing.T) {
	cfg := &config.Config{
		Channels: []config.ChannelConfig{
			{
				ID: "ch1",
				RequestHeaders: &config.HeaderPolicyConfig{
					Enabled: true,
					Rules: []config.HeaderRule{
						{
							Name:      "channel-drop",
							Phase:     config.PhaseRequest,
							MatchType: config.MatchExact,
							Pattern:   "x-model-flag",
							Action:    config.ActionDrop,
						},
					},
				},
				Models: []config.ModelConfig{
					{
						ID: "model-pro",
						RequestHeaders: &config.HeaderPolicyConfig{
							Enabled: true,
							Rules: []config.HeaderRule{
								{
									Name:      "model-set",
									Phase:     config.PhaseRequest,
									MatchType: config.MatchExact,
									Pattern:   "x-model-flag",
									Action:    config.ActionSet,
									Value:     "pro",
								},
							},
						},
					},
				},
			},
		},
	}
	engine := NewEngine(cfg)

	clientHeaders := http.Header{
		"X-Model-Flag": []string{"original"},
	}

	result := engine.ProcessRequest("ch1", "model-pro", clientHeaders)

	if result.Get("X-Model-Flag") != "pro" {
		t.Errorf("X-Model-Flag should be 'pro' (model overrides channel), got %q", result.Get("X-Model-Flag"))
	}
}

func TestEngine_ProcessRequest_DefaultActionDrop(t *testing.T) {
	cfg := &config.Config{
		GlobalRequestHeaders: &config.HeaderPolicyConfig{
			Enabled:       true,
			DefaultAction: config.ActionDrop,
			Rules: []config.HeaderRule{
				{
					Name:      "keep-content-type",
					Phase:     config.PhaseRequest,
					MatchType: config.MatchExact,
					Pattern:   "content-type",
					Action:    config.ActionPass,
				},
			},
		},
	}
	engine := NewEngine(cfg)

	clientHeaders := http.Header{
		"Content-Type": []string{"application/json"},
		"X-Custom":     []string{"value"},
		"X-Other":      []string{"other"},
	}

	result := engine.ProcessRequest("ch1", "model1", clientHeaders)

	if result.Get("Content-Type") == "" {
		t.Error("Content-Type should pass through (explicit pass rule)")
	}
	if result.Get("X-Custom") != "" {
		t.Error("X-Custom should be dropped (default_action=drop)")
	}
	if result.Get("X-Other") != "" {
		t.Error("X-Other should be dropped (default_action=drop)")
	}
}

func TestEngine_ProcessResponse(t *testing.T) {
	cfg := &config.Config{
		GlobalResponseHeaders: &config.HeaderPolicyConfig{
			Enabled: true,
			Rules: []config.HeaderRule{
				{
					Name:      "strip-upstream",
					Phase:     config.PhaseResponse,
					MatchType: config.MatchExact,
					Pattern:   "x-upstream-request-id",
					Action:    config.ActionDrop,
				},
			},
		},
	}
	engine := NewEngine(cfg)

	upstreamHeaders := http.Header{
		"Content-Type":          []string{"application/json"},
		"X-Upstream-Request-Id": []string{"req-123"},
		"X-Custom":              []string{"value"},
	}

	result := engine.ProcessResponse("ch1", "model1", upstreamHeaders)

	if result.Get("X-Upstream-Request-Id") != "" {
		t.Error("X-Upstream-Request-Id should be dropped")
	}
	if result.Get("Content-Type") == "" {
		t.Error("Content-Type should pass through")
	}
	if result.Get("X-Custom") == "" {
		t.Error("X-Custom should pass through")
	}
}

func TestEngine_NoPolicy_PassThrough(t *testing.T) {
	cfg := &config.Config{}
	engine := NewEngine(cfg)

	clientHeaders := http.Header{
		"Content-Type": []string{"application/json"},
		"X-Custom":     []string{"value"},
	}

	result := engine.ProcessRequest("ch1", "model1", clientHeaders)

	if result.Get("Content-Type") != "application/json" {
		t.Error("Content-Type should pass through")
	}
	if result.Get("X-Custom") != "value" {
		t.Error("X-Custom should pass through")
	}
}
