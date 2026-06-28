package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/fangxiusun/ai-adapter/internal/log"
)

type capturingResponseWriter struct {
	http.ResponseWriter
	reqID  string
	logger *log.Logger
	body   bytes.Buffer
}

func (w *capturingResponseWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	w.logger.LogClientOutput(w.reqID, b)
	return w.ResponseWriter.Write(b)
}

func (w *capturingResponseWriter) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status)
}

type sseCapturingWriter struct {
	http.ResponseWriter
	reqID      string
	logger     *log.Logger
	buf        bytes.Buffer
	lastEvent string
}

func (w *sseCapturingWriter) Write(b []byte) (int, error) {
	w.buf.Write(b)
	lines := strings.Split(string(b), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "event: ") {
			w.lastEvent = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				w.logger.LogSSEEvent(w.reqID, "DONE", []byte(`"done"`))
			} else {
				event := "message"
				if w.lastEvent != "" {
					event = w.lastEvent
				}
				w.logger.LogSSEEvent(w.reqID, event, []byte(data))
			}
			w.lastEvent = ""
		}
	}
	return w.ResponseWriter.Write(b)
}

func (w *sseCapturingWriter) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status)
}

func newCapturingWriter(w http.ResponseWriter, reqID string, logger *log.Logger) *capturingResponseWriter {
	return &capturingResponseWriter{
		ResponseWriter: w,
		reqID:          reqID,
		logger:         logger,
	}
}

func newSSECapturingWriter(w http.ResponseWriter, reqID string, logger *log.Logger) *sseCapturingWriter {
	return &sseCapturingWriter{
		ResponseWriter: w,
		reqID:          reqID,
		logger:         logger,
	}
}

func parseSSEContent(data string) (event string, content string) {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return "unknown", data
	}

	if t, ok := raw["type"].(string); ok {
		event = t
	}

	if delta, ok := raw["delta"].(string); ok && delta != "" {
		content = delta
	} else if text, ok := raw["text"].(string); ok && text != "" {
		content = text
	} else if message, ok := raw["message"].(string); ok && message != "" {
		content = message
	}

	return event, content
}

