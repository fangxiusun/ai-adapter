package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Logger struct {
	level    slog.Level
	inner    *slog.Logger
	file     *os.File
	mu       sync.Mutex
	enabled  bool
	logBody  bool
	logIO    bool
}

func New(levelStr string, filePath string, logBody bool, logIO bool) *Logger {
	level := parseLevel(levelStr)
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if filePath != "" {
		f, err := openLogFile(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot open log file %s: %v, using stdout\n", filePath, err)
			handler = slog.NewTextHandler(os.Stdout, opts)
		} else {
			handler = slog.NewTextHandler(f, opts)
			return &Logger{level: level, inner: slog.New(handler), file: f, enabled: true, logBody: logBody, logIO: logIO}
		}
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return &Logger{level: level, inner: slog.New(handler), enabled: true, logBody: logBody, logIO: logIO}
}

func (l *Logger) SetEnabled(v bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.enabled = v
}

func (l *Logger) SetLevel(levelStr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = parseLevel(levelStr)
}

func (l *Logger) SetLogIO(v bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logIO = v
}

func (l *Logger) Close() {
	if l.file != nil {
		l.file.Close()
	}
}

func (l *Logger) Debug(msg string, args ...any) {
	if !l.enabled || l.level > slog.LevelDebug {
		return
	}
	l.inner.Debug(msg, args...)
}

func (l *Logger) Info(msg string, args ...any) {
	if !l.enabled || l.level > slog.LevelInfo {
		return
	}
	l.inner.Info(msg, args...)
}

func (l *Logger) Warn(msg string, args ...any) {
	if !l.enabled || l.level > slog.LevelWarn {
		return
	}
	l.inner.Warn(msg, args...)
}

func (l *Logger) Error(msg string, args ...any) {
	if !l.enabled {
		return
	}
	l.inner.Error(msg, args...)
}

func (l *Logger) LogRequest(reqID, method, path string, status int, latencyMs int64, key, channel, model string) {
	l.Info("request",
		"request_id", reqID,
		"method", method,
		"path", path,
		"status", status,
		"latency_ms", latencyMs,
		"key", maskKey(key),
		"channel", channel,
		"model", model,
	)
}

func (l *Logger) LogRequestBody(reqID string, body []byte) {
	if !l.logBody {
		return
	}
	l.Debug("request_body", "request_id", reqID, "body_len", len(body))
}

func (l *Logger) LogResponseBody(reqID string, body []byte) {
	if !l.logBody {
		return
	}
	l.Debug("response_body", "request_id", reqID, "body_len", len(body))
}

func (l *Logger) LogClientInput(reqID string, body []byte) {
	if !l.logIO {
		return
	}
	pretty := prettyJSON(body)
	l.Info("client_input",
		"request_id", reqID,
		"body", pretty,
	)
}

func (l *Logger) LogClientOutput(reqID string, body []byte) {
	if !l.logIO {
		return
	}
	pretty := prettyJSON(body)
	l.Info("client_output",
		"request_id", reqID,
		"body", pretty,
	)
}

func (l *Logger) LogSSEEvent(reqID string, event string, data []byte) {
	if !l.logIO {
		return
	}
	pretty := prettyJSON(data)
	l.Info("sse_event",
		"request_id", reqID,
		"event", event,
		"data", pretty,
	)
}

func (l *Logger) LogSSEDelta(reqID string, deltaType string, content string) {
	if !l.logIO {
		return
	}
	if len(content) > 500 {
		content = content[:500] + "...(truncated)"
	}
	l.Info("sse_delta",
		"request_id", reqID,
		"type", deltaType,
		"content", content,
	)
}

func (l *Logger) LogKeyPaused(channel, key string, consecErrors int, pauseUntil time.Time) {
	l.Warn("key_paused",
		"channel", channel,
		"key", maskKey(key),
		"consec_errors", consecErrors,
		"pause_until", pauseUntil.Format(time.RFC3339),
	)
}

func (l *Logger) LogKeyResumed(channel, key string) {
	l.Info("key_resumed", "channel", channel, "key", maskKey(key))
}

func (l *Logger) LogTranslate(direction string, toolsMapped, toolsDropped int) {
	l.Debug("translate",
		"direction", direction,
		"tools_mapped", toolsMapped,
		"tools_dropped", toolsDropped,
	)
}

func (l *Logger) LogFanout(channel string, keyCount int, strategy string) {
	l.Debug("fanout",
		"channel", channel,
		"key_count", keyCount,
		"strategy", strategy,
	)
}

func (l *Logger) LogConfigReload(path string) {
	l.Info("config_reloaded", "path", path)
}

func (l *Logger) LogHealth(channel, status string, latencyMs int64) {
	l.Debug("health_check",
		"channel", channel,
		"status", status,
		"latency_ms", latencyMs,
	)
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func openLogFile(path string) (*os.File, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "***" + key[len(key)-4:]
}

func prettyJSON(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	s := string(data)
	if len(s) > 2000 {
		s = s[:2000] + "...(truncated)"
	}
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}

func (l *Logger) Writer() io.Writer {
	return &logWriter{l: l}
}

type logWriter struct {
	l *Logger
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	w.l.Info(string(p))
	return len(p), nil
}
