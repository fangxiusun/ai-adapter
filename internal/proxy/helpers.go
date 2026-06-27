package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fangxiusun/ai-adapter/internal/channel"
)

// sendError writes a JSON error response to the client.
func (h *ProxyHandler) sendError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{"type": "error", "code": code, "message": message, "status": status},
	})
}

// recordLog inserts a request log entry into the database.
func (h *ProxyHandler) recordLog(reqID, channelID, clientModel, upstreamModel string, status int, latencyMs int64, key, errorCode, errorMsg string) {
	if h.db != nil {
		h.db.InsertLog(reqID, channelID, clientModel, upstreamModel, status, latencyMs, key, errorCode, errorMsg)
	}
}

func generateRequestID() string {
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}

// stripUTF8BOM removes a leading UTF-8 BOM (EF BB BF) from the byte slice.
// Some HTTP clients (notably PowerShell on Windows) prepend a BOM to request
// bodies, which causes strict JSON parsers in upstream APIs to reject the payload.
func stripUTF8BOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// ==================== Header Policy Helpers ====================

// processRequestHeaders applies header policy to client request headers before sending to upstream.
// Returns processed headers that should be applied to the upstream request.
// Content-Type and Authorization are always set by the system, not affected by policy.
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
