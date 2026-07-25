package translate

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestChatStreamConvertersRejectMalformedAndIncompleteStreams(t *testing.T) {
	tests := []struct {
		name  string
		input string
		run   func(string) error
	}{
		{
			name:  "malformed JSON",
			input: "data: {not-json}\n\n",
			run: func(input string) error {
				_, err := PipeChatStreamToClaude(context.Background(), strings.NewReader(input), &bytes.Buffer{}, &ChatRequest{}, nil)
				return err
			},
		},
		{
			name:  "missing terminal event",
			input: "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
			run: func(input string) error {
				_, err := PipeChatStreamToGemini(context.Background(), strings.NewReader(input), &bytes.Buffer{}, &ChatRequest{}, nil)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(tt.input); err == nil {
				t.Fatal("expected stream error")
			}
		})
	}
}

func TestClaudeStreamRequiresMessageStop(t *testing.T) {
	input := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n"
	if _, err := PipeClaudeStreamToChat(context.Background(), strings.NewReader(input), &bytes.Buffer{}, &ChatRequest{}, nil); err == nil {
		t.Fatal("expected incomplete Claude stream error")
	}
}

func TestGeminiSSEStreamsChatDeltas(t *testing.T) {
	input := "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}]}\n\n"
	var output bytes.Buffer
	resp, err := PipeGeminiStreamToChat(context.Background(), strings.NewReader(input), &output, &ChatRequest{Model: "test"}, nil)
	if err != nil {
		t.Fatalf("PipeGeminiStreamToChat: %v", err)
	}
	if !strings.Contains(output.String(), "hello") || !strings.Contains(output.String(), "data: [DONE]") {
		t.Fatalf("unexpected streamed output: %s", output.String())
	}
	if resp.Choices[0].Message.Content == nil || *resp.Choices[0].Message.Content != "hello" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestResponsesStreamWritesIncrementalChatDelta(t *testing.T) {
	input := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"hello"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"test","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		``,
	}, "\n")
	var output bytes.Buffer
	if _, err := PipeResponsesStreamToChat(context.Background(), strings.NewReader(input), &output, &ChatRequest{Model: "test"}, TranslateOpts{}); err != nil {
		t.Fatalf("PipeResponsesStreamToChat: %v", err)
	}
	if !strings.Contains(output.String(), "hello") || !strings.Contains(output.String(), "data: [DONE]") {
		t.Fatalf("unexpected streamed output: %s", output.String())
	}
}
