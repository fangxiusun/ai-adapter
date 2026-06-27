package proxy

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestStreamUsageCapture_ChatSSE(t *testing.T) {
	input := "data: {\"id\":\"1\",\"choices\":[]}\n\n" +
		"data: {\"id\":\"2\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"id\":\"3\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":20,\"total_tokens\":30}}\n\n" +
		"data: [DONE]\n\n"

	capture := newStreamUsageCapture(strings.NewReader(input))
	out, _ := io.ReadAll(capture)

	// Data passes through unchanged
	if string(out) != input {
		t.Errorf("data not passed through correctly")
	}

	pt, ct, tt, usageJSON := capture.Usage()
	if pt != 10 {
		t.Errorf("promptTokens = %d, want 10", pt)
	}
	if ct != 20 {
		t.Errorf("completionTokens = %d, want 20", ct)
	}
	if tt != 30 {
		t.Errorf("totalTokens = %d, want 30", tt)
	}
	if usageJSON == "" {
		t.Error("usageJSON should not be empty")
	}
	if !strings.Contains(usageJSON, "prompt_tokens") {
		t.Error("usageJSON should contain prompt_tokens")
	}
}

func TestStreamUsageCapture_ResponsesSSE(t *testing.T) {
	input := "event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":15,\"total_tokens\":20}}}\n\n"

	capture := newStreamUsageCapture(strings.NewReader(input))
	io.ReadAll(capture)

	pt, ct, tt, _ := capture.Usage()
	if pt != 5 || ct != 15 || tt != 20 {
		t.Errorf("got pt=%d ct=%d tt=%d, want 5/15/20", pt, ct, tt)
	}
}

func TestStreamUsageCapture_GeminiSSE(t *testing.T) {
	input := "data: {\"candidates\":[],\"usageMetadata\":{\"promptTokenCount\":8,\"candidatesTokenCount\":12,\"totalTokenCount\":20}}\n\n"

	capture := newStreamUsageCapture(strings.NewReader(input))
	io.ReadAll(capture)

	pt, ct, tt, _ := capture.Usage()
	if pt != 8 || ct != 12 || tt != 20 {
		t.Errorf("got pt=%d ct=%d tt=%d, want 8/12/20", pt, ct, tt)
	}
}

func TestStreamUsageCapture_NoUsage(t *testing.T) {
	input := "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"

	capture := newStreamUsageCapture(strings.NewReader(input))
	io.ReadAll(capture)

	pt, ct, tt, usageJSON := capture.Usage()
	if pt != 0 || ct != 0 || tt != 0 {
		t.Errorf("got pt=%d ct=%d tt=%d, want 0/0/0", pt, ct, tt)
	}
	if usageJSON != "" {
		t.Errorf("usageJSON should be empty, got %s", usageJSON)
	}
}

func TestStreamUsageCapture_PassthroughIntegrity(t *testing.T) {
	input := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\ndata: [DONE]\n\n"

	capture := newStreamUsageCapture(strings.NewReader(input))
	var buf bytes.Buffer
	buf.ReadFrom(capture)

	if buf.String() != input {
		t.Errorf("passthrough data mismatch:\ngot:  %q\nwant: %q", buf.String(), input)
	}
}

func TestStreamUsageCapture_PartialLines(t *testing.T) {
	// Simulate chunked reads that split lines
	chunks := []string{
		"data: {\"id\":\"1\",\"usage\":{\"prom",
		"pt_tokens\":5,\"completion_tokens\":10,\"total_tokens\":15}}\n\n",
		"data: [DONE]\n\n",
	}

	pr, pw := io.Pipe()
	capture := newStreamUsageCapture(pr)

	go func() {
		for _, chunk := range chunks {
			pw.Write([]byte(chunk))
		}
		pw.Close()
	}()

	io.ReadAll(capture)

	pt, ct, tt, _ := capture.Usage()
	if pt != 5 || ct != 10 || tt != 15 {
		t.Errorf("got pt=%d ct=%d tt=%d, want 5/10/15", pt, ct, tt)
	}
}
