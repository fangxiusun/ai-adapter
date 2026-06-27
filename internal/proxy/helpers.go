package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/translate"
)

// sendError writes a JSON error response to the client.
func (h *ProxyHandler) sendError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{"type": "error", "code": code, "message": message, "status": status},
	})
}

// recordLog inserts a request log entry into the database with usage data.
func (h *ProxyHandler) recordLog(reqID, channelID, clientModel, upstreamModel string, status int, latencyMs int64, key, errorCode, errorMsg string, promptTokens, completionTokens, totalTokens int, usageJSON string) {
	if h.db != nil {
		h.db.InsertLog(reqID, channelID, clientModel, upstreamModel, status, latencyMs, key, errorCode, errorMsg, promptTokens, completionTokens, totalTokens, usageJSON)
	}
}

func generateRequestID() string {
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}

// stripUTF8BOM removes a leading UTF-8 BOM (EF BB BF) from the byte slice.
func stripUTF8BOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// ==================== Usage Helpers ====================

// normalizeUsage extracts standardized token counts from a ChatUsage and returns
// (promptTokens, completionTokens, totalTokens, usageJSON).
func normalizeUsage(usage *translate.ChatUsage) (int, int, int, string) {
	if usage == nil {
		return 0, 0, 0, ""
	}
	b, _ := json.Marshal(usage)
	return usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, string(b)
}

// extractUsageFromRawBody extracts usage data from a raw upstream response body
// based on the interface protocol type.
func extractUsageFromRawBody(iface config.InterfaceType, body []byte) (int, int, int, string) {
	if len(body) == 0 {
		return 0, 0, 0, ""
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, 0, 0, ""
	}

	switch iface {
	case config.InterfaceChat:
		return extractChatUsage(raw)
	case config.InterfaceResponses:
		return extractResponsesUsage(raw)
	case config.InterfaceMessages:
		return extractClaudeUsage(raw)
	case config.InterfaceGenerateContent:
		return extractGeminiUsage(raw)
	}
	return 0, 0, 0, ""
}

func extractChatUsage(raw map[string]interface{}) (int, int, int, string) {
	u, ok := raw["usage"].(map[string]interface{})
	if !ok {
		return 0, 0, 0, ""
	}
	pt := toInt(u["prompt_tokens"])
	ct := toInt(u["completion_tokens"])
	tt := toInt(u["total_tokens"])
	b, _ := json.Marshal(u)
	return pt, ct, tt, string(b)
}

func extractResponsesUsage(raw map[string]interface{}) (int, int, int, string) {
	u, ok := raw["usage"].(map[string]interface{})
	if !ok {
		return 0, 0, 0, ""
	}
	pt := toInt(u["input_tokens"])
	ct := toInt(u["output_tokens"])
	tt := toInt(u["total_tokens"])
	b, _ := json.Marshal(u)
	return pt, ct, tt, string(b)
}

func extractClaudeUsage(raw map[string]interface{}) (int, int, int, string) {
	u, ok := raw["usage"].(map[string]interface{})
	if !ok {
		return 0, 0, 0, ""
	}
	pt := toInt(u["input_tokens"])
	ct := toInt(u["output_tokens"])
	tt := pt + ct
	b, _ := json.Marshal(u)
	return pt, ct, tt, string(b)
}

func extractGeminiUsage(raw map[string]interface{}) (int, int, int, string) {
	u, ok := raw["usageMetadata"].(map[string]interface{})
	if !ok {
		return 0, 0, 0, ""
	}
	pt := toInt(u["promptTokenCount"])
	ct := toInt(u["candidatesTokenCount"])
	tt := toInt(u["totalTokenCount"])
	b, _ := json.Marshal(u)
	return pt, ct, tt, string(b)
}

// toInt converts a numeric interface{} to int. Returns 0 for nil or non-numeric.
func toInt(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

// injectStreamOptions ensures stream_options.include_usage is true for Chat requests
// unless the user explicitly set it to false. Modifies body in-place and returns
// the (possibly new) body bytes.
func injectStreamOptions(body []byte) []byte {
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	opts, _ := raw["stream_options"].(map[string]interface{})
	if opts != nil {
		if v, ok := opts["include_usage"]; ok && v == false {
			return body // user explicitly disabled it
		}
	}
	if opts == nil {
		opts = make(map[string]interface{})
	}
	opts["include_usage"] = true
	raw["stream_options"] = opts
	out, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return out
}

// ==================== Header Policy Helpers ====================

// processRequestHeaders applies header policy to client request headers before sending to upstream.
func (h *ProxyHandler) processRequestHeaders(ch *channel.Channel, model string, clientHeaders http.Header) http.Header {
	if h.headerEngine == nil {
		return nil
	}
	return h.headerEngine.ProcessRequest(ch.Config.ID, model, clientHeaders)
}

// processResponseHeaders applies header policy to upstream response headers before sending to client.
func (h *ProxyHandler) processResponseHeaders(ch *channel.Channel, model string, upstreamHeaders http.Header) http.Header {
	if h.headerEngine == nil {
		return nil
	}
	return h.headerEngine.ProcessResponse(ch.Config.ID, model, upstreamHeaders)
}

// applyProcessedHeaders applies processed headers to the target, preserving system headers.
func applyProcessedHeaders(target http.Header, processed http.Header, preserveKeys ...string) {
	if processed == nil {
		return
	}
	preserve := make(map[string]bool)
	for _, k := range preserveKeys {
		preserve[strings.ToLower(k)] = true
	}
	for k, v := range processed {
		if !preserve[strings.ToLower(k)] {
			target[k] = v
		}
	}
}
