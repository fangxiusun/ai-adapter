package debuglog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamCaptureWritesUpstreamAndClientBodies(t *testing.T) {
	logger := &DeepDebugLogger{enabled: true, dir: t.TempDir()}
	requestLog := logger.BeginRequest("req-test", "POST", "/v1/chat/completions")
	if requestLog == nil {
		t.Fatal("BeginRequest returned nil")
	}

	upstream := requestLog.NewUpstreamStreamCapture(200)
	client := requestLog.NewClientStreamCapture(200)
	raw := []byte("data: hello\n\ndata: [DONE]\n\n")
	if _, err := upstream.Write(raw); err != nil {
		t.Fatalf("write upstream capture: %v", err)
	}
	if _, err := client.Write(raw); err != nil {
		t.Fatalf("write client capture: %v", err)
	}
	if err := upstream.Close(); err != nil {
		t.Fatalf("close upstream capture: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close client capture: %v", err)
	}

	for _, filename := range []string{"upstream.res.body.log", "client.res.body.log"} {
		content, err := os.ReadFile(filepath.Join(requestLog.dir, filename))
		if err != nil {
			t.Fatalf("read %s: %v", filename, err)
		}
		text := string(content)
		if !strings.Contains(text, "hello") || !strings.Contains(text, "data: [DONE]") {
			t.Fatalf("%s did not contain SSE body: %s", filename, text)
		}
		if !strings.Contains(text, "CapturedLength:") || !strings.Contains(text, "Truncated: false") {
			t.Fatalf("%s did not contain capture footer: %s", filename, text)
		}
	}
}

func TestNilStreamCaptureIsNoOp(t *testing.T) {
	var requestLog *RequestLog
	capture := requestLog.NewUpstreamStreamCapture(200)
	if n, err := capture.Write([]byte("data")); err != nil || n != 4 {
		t.Fatalf("nil capture Write = %d, %v", n, err)
	}
	if err := capture.Close(); err != nil {
		t.Fatalf("nil capture Close: %v", err)
	}
}
