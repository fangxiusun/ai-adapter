package debuglog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// DeepDebugLogger creates per-request log files when deep debug mode is enabled.
type DeepDebugLogger struct {
	enabled bool
	dir     string
	mu      sync.Once
}

// New creates a DeepDebugLogger. If enabled is false, all operations are no-ops.
func New(enabled bool) *DeepDebugLogger {
	dir := filepath.Join(".", "debug_logs")
	return &DeepDebugLogger{enabled: enabled, dir: dir}
}

// IsEnabled returns whether deep debug mode is active.
func (l *DeepDebugLogger) IsEnabled() bool {
	return l.enabled
}

// RequestLog represents a single request's set of debug log files.
type RequestLog struct {
	logger              *DeepDebugLogger
	logID               string
	dir                 string
	mu                  sync.Mutex
	upstreamReqBodyOnce sync.Once
}

// BeginRequest creates a new per-request log directory and files. Returns nil if deep debug is disabled.
func (l *DeepDebugLogger) BeginRequest(reqID string, method, path string) *RequestLog {
	if !l.enabled {
		return nil
	}

	// Ensure base directory exists
	l.mu.Do(func() {
		os.MkdirAll(l.dir, 0755)
	})

	// Generate LOGID: YYYYMMDDHHMMSSmmm-UUID
	now := time.Now()
	uuidStr := uuid.New().String()
	logID := fmt.Sprintf("%s-%s", now.Format("20060102150405.000"), uuidStr)
	logID = strings.ReplaceAll(logID, ".", "")

	// Create per-request directory
	reqDir := filepath.Join(l.dir, logID)
	if err := os.MkdirAll(reqDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "[deep-debug] failed to create request dir %s: %v\n", reqDir, err)
		return nil
	}

	rl := &RequestLog{logger: l, logID: logID, dir: reqDir}

	// Write initial metadata to each header file
	ts := now.Format("2006-01-02 15:04:05.000")
	meta := fmt.Sprintf("LOGID:     %s\nRequestID: %s\nTimestamp: %s\nMethod:    %s\nPath:      %s\n", logID, reqID, ts, method, path)

	rl.writeFile("client.req.header.log", meta)
	rl.writeFile("upstream.req.header.log", meta)
	rl.writeFile("upstream.res.header.log", meta)
	rl.writeFile("client.res.header.log", meta)

	return rl
}

// LogClientRequestHeader logs the client request headers and URL.
func (rl *RequestLog) LogClientRequestHeader(r *http.Request) {
	if rl == nil {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format("2006-01-02 15:04:05.000")))
	sb.WriteString(fmt.Sprintf("Method:    %s\n", r.Method))
	sb.WriteString(fmt.Sprintf("URL:       %s\n", r.URL.String()))
	sb.WriteString(fmt.Sprintf("Host:      %s\n", r.Host))
	sb.WriteString("\nHeaders:\n")
	sb.WriteString(formatHeaders(r.Header))
	rl.writeFile("client.req.header.log", sb.String())
}

// LogClientRequestBody logs the client request body.
func (rl *RequestLog) LogClientRequestBody(body []byte) {
	if rl == nil {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.writeFile("client.req.body.log", formatBody(body))
}

// LogUpstreamRequestHeader logs the upstream request headers and URL.
func (rl *RequestLog) LogUpstreamRequestHeader(method, url string, headers http.Header) {
	if rl == nil {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format("2006-01-02 15:04:05.000")))
	sb.WriteString(fmt.Sprintf("Method:    %s\n", method))
	sb.WriteString(fmt.Sprintf("URL:       %s\n", url))
	sb.WriteString("\nHeaders:\n")
	sb.WriteString(formatHeadersMasked(headers))
	rl.writeFile("upstream.req.header.log", sb.String())
}

// LogUpstreamRequestBody logs the upstream request body (only written once per request).
func (rl *RequestLog) LogUpstreamRequestBody(body []byte) {
	if rl == nil {
		return
	}
	rl.upstreamReqBodyOnce.Do(func() {
		rl.mu.Lock()
		defer rl.mu.Unlock()
		rl.writeFileOverwrite("upstream.req.body.log", formatBody(body))
	})
}

// LogUpstreamResponseHeader logs the upstream response headers.
func (rl *RequestLog) LogUpstreamResponseHeader(statusCode int, headers http.Header) {
	if rl == nil {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Timestamp:  %s\n", time.Now().Format("2006-01-02 15:04:05.000")))
	sb.WriteString(fmt.Sprintf("StatusCode: %d %s\n", statusCode, statusText(statusCode)))
	sb.WriteString("\nHeaders:\n")
	sb.WriteString(formatHeaders(headers))
	rl.writeFile("upstream.res.header.log", sb.String())
}

// LogUpstreamResponseBody logs the upstream response body.
func (rl *RequestLog) LogUpstreamResponseBody(body []byte) {
	if rl == nil {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.writeFile("upstream.res.body.log", formatBody(body))
}

// LogUpstreamStreamResponse logs an upstream SSE stream response.
func (rl *RequestLog) LogUpstreamStreamResponse(statusCode int, rawStream []byte) {
	if rl == nil {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Timestamp:  %s\n", time.Now().Format("2006-01-02 15:04:05.000")))
	sb.WriteString(fmt.Sprintf("StatusCode: %d %s\n", statusCode, statusText(statusCode)))
	sb.WriteString(fmt.Sprintf("Length:     %d bytes\n", len(rawStream)))
	sb.WriteString("\n--- Raw Stream Data ---\n")
	sb.WriteString(string(rawStream))
	sb.WriteString("\n--- End Stream Data ---\n")
	rl.writeFile("upstream.res.body.log", sb.String())
}

// LogClientResponseHeader logs the response headers sent to the client.
func (rl *RequestLog) LogClientResponseHeader(statusCode int, headers http.Header) {
	if rl == nil {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Timestamp:  %s\n", time.Now().Format("2006-01-02 15:04:05.000")))
	sb.WriteString(fmt.Sprintf("StatusCode: %d %s\n", statusCode, statusText(statusCode)))
	sb.WriteString("\nHeaders:\n")
	sb.WriteString(formatHeaders(headers))
	rl.writeFile("client.res.header.log", sb.String())
}

// LogClientResponseBody logs the response body sent to the client.
func (rl *RequestLog) LogClientResponseBody(body []byte) {
	if rl == nil {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.writeFile("client.res.body.log", formatBody(body))
}

// LogID returns the LOGID for this request.
func (rl *RequestLog) LogID() string {
	if rl == nil {
		return ""
	}
	return rl.logID
}

// Close finalizes the log files.
func (rl *RequestLog) Close() {
	// No-op; files are written per-call
}

// ============ Internal helpers ============

// writeFileOverwrite writes content to a file, overwriting existing content.
func (rl *RequestLog) writeFileOverwrite(filename, content string) {
	filePath := filepath.Join(rl.dir, filename)
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[deep-debug] failed to write %s: %v\n", filePath, err)
		return
	}
	defer f.Close()
	f.WriteString(content)
}

// writeFile appends content to a file.
func (rl *RequestLog) writeFile(filename, content string) {
	filePath := filepath.Join(rl.dir, filename)
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[deep-debug] failed to write %s: %v\n", filePath, err)
		return
	}
	defer f.Close()
	f.WriteString(content)
}

// formatHeaders formats http.Header for display.
func formatHeaders(headers http.Header) string {
	if len(headers) == 0 {
		return "  (no headers)\n"
	}
	var sb strings.Builder
	for k, vals := range headers {
		for _, v := range vals {
			sb.WriteString(fmt.Sprintf("  %s: %s\n", k, v))
		}
	}
	return sb.String()
}

// formatHeadersMasked formats http.Header with sensitive values masked.
func formatHeadersMasked(headers http.Header) string {
	if len(headers) == 0 {
		return "  (no headers)\n"
	}
	var sb strings.Builder
	for k, vals := range headers {
		for _, v := range vals {
			if isSensitiveHeaderBool(k) {
				v = maskToken(v)
			}
			sb.WriteString(fmt.Sprintf("  %s: %s\n", k, v))
		}
	}
	return sb.String()
}

// isSensitiveHeaderBool checks if a header contains sensitive data.
func isSensitiveHeaderBool(key string) bool {
	lower := strings.ToLower(key)
	sensitive := []string{"authorization", "x-api-key", "api-key", "token", "x-auth-token", "cookie", "set-cookie"}
	for _, s := range sensitive {
		if lower == s {
			return true
		}
	}
	return false
}

// maskToken masks the middle part of a token, showing first 4 and last 4 chars.
func maskToken(s string) string {
	if len(s) <= 12 {
		return s
	}
	prefix := s[:4]
	suffix := s[len(s)-4:]
	maskLen := len(s) - 8
	if maskLen > 20 {
		maskLen = 20
	}
	return prefix + strings.Repeat("*", maskLen) + suffix
}

// formatBody formats a body for display, pretty-printing JSON if possible.
func formatBody(body []byte) string {
	if len(body) == 0 {
		return "(empty body)\n"
	}
	if pretty, ok := formatJSON(body); ok {
		return pretty + "\n"
	}
	return string(body) + "\n"
}

// formatJSON attempts to parse and pretty-print JSON.
func formatJSON(data []byte) (string, bool) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return "", false
	}
	if data[0] != '{' && data[0] != '[' {
		return "", false
	}
	var obj interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return "", false
	}
	pretty, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return "", false
	}
	return string(pretty), true
}

func statusText(code int) string {
	switch code {
	case 200:
		return "OK"
	case 201:
		return "Created"
	case 400:
		return "Bad Request"
	case 401:
		return "Unauthorized"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	case 429:
		return "Too Many Requests"
	case 500:
		return "Internal Server Error"
	case 502:
		return "Bad Gateway"
	case 503:
		return "Service Unavailable"
	case 504:
		return "Gateway Timeout"
	default:
		return ""
	}
}
