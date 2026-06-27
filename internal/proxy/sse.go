package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// ==================== SSE Writer ====================

type sseWriter struct {
	sink    io.Writer
	flusher func()
}

func newSSEWriter(sink io.Writer, flusher func()) *sseWriter {
	return &sseWriter{sink: sink, flusher: flusher}
}

func (w *sseWriter) writeEvent(event string, data interface{}) {
	if data != nil {
		payload, _ := json.Marshal(data)
		fmt.Fprintf(w.sink, "event: %s\ndata: %s\n\n", event, string(payload))
	} else {
		fmt.Fprintf(w.sink, "event: %s\ndata: {}\n\n", event)
	}
	w.flush()
}

func (w *sseWriter) flush() {
	if w.flusher != nil {
		w.flusher()
	}
}

// ==================== Stream Usage Capture ====================

// streamUsageCapture wraps an io.Reader to intercept SSE data lines and capture
// usage information from the last chunk. All data is passed through unchanged.
type streamUsageCapture struct {
	reader         io.Reader
	promptTokens   int
	completionTokens int
	totalTokens    int
	usageJSON      string
	lineBuf        []byte // buffer for partial line data
}

func newStreamUsageCapture(reader io.Reader) *streamUsageCapture {
	return &streamUsageCapture{reader: reader}
}

// Read implements io.Reader. It passes all data through while scanning for
// usage data in SSE "data:" lines.
func (c *streamUsageCapture) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	if n > 0 {
		// Append to line buffer and scan for complete lines
		c.lineBuf = append(c.lineBuf, p[:n]...)
		c.processLines()
	}
	return n, err
}

// processLines scans the line buffer for complete lines and extracts usage.
func (c *streamUsageCapture) processLines() {
	for {
		idx := bytes.IndexByte(c.lineBuf, '\n')
		if idx < 0 {
			break
		}
		line := c.lineBuf[:idx]
		c.lineBuf = c.lineBuf[idx+1:]

		// Trim \r if present
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}

		lineStr := string(line)
		if len(lineStr) > 6 && lineStr[:6] == "data: " {
			data := lineStr[6:]
			if data == "[DONE]" {
				continue
			}
			c.tryExtractUsage(data)
		}
	}
}

// tryExtractUsage attempts to parse a JSON data line and extract usage fields.
func (c *streamUsageCapture) tryExtractUsage(data string) {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return
	}

	// Check for Chat format: {"usage": {"prompt_tokens": N, ...}}
	if u, ok := raw["usage"].(map[string]interface{}); ok {
		if pt, exists := u["prompt_tokens"]; exists {
			c.promptTokens = toInt(pt)
			c.completionTokens = toInt(u["completion_tokens"])
			c.totalTokens = toInt(u["total_tokens"])
			b, _ := json.Marshal(u)
			c.usageJSON = string(b)
			return
		}
		// Responses format: {"usage": {"input_tokens": N, ...}}
		if pt, exists := u["input_tokens"]; exists {
			c.promptTokens = toInt(pt)
			c.completionTokens = toInt(u["output_tokens"])
			c.totalTokens = toInt(u["total_tokens"])
			b, _ := json.Marshal(u)
			c.usageJSON = string(b)
			return
		}
	}

	// Check for Responses event format: {"type":"response.completed","response":{"usage":{...}}}
	if resp, ok := raw["response"].(map[string]interface{}); ok {
		if u, ok := resp["usage"].(map[string]interface{}); ok {
			c.promptTokens = toInt(u["input_tokens"])
			c.completionTokens = toInt(u["output_tokens"])
			c.totalTokens = toInt(u["total_tokens"])
			b, _ := json.Marshal(u)
			c.usageJSON = string(b)
			return
		}
	}

	// Check for Gemini format: {"usageMetadata": {"promptTokenCount": N, ...}}
	if u, ok := raw["usageMetadata"].(map[string]interface{}); ok {
		c.promptTokens = toInt(u["promptTokenCount"])
		c.completionTokens = toInt(u["candidatesTokenCount"])
		c.totalTokens = toInt(u["totalTokenCount"])
		b, _ := json.Marshal(u)
		c.usageJSON = string(b)
	}
}

// Usage returns the captured usage data.
func (c *streamUsageCapture) Usage() (int, int, int, string) {
	return c.promptTokens, c.completionTokens, c.totalTokens, c.usageJSON
}
