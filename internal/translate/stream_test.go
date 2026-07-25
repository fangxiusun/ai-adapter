package translate

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestPipeResponsesStreamToChatParsesOutputItemsAndUsage(t *testing.T) {
	input := strings.Join([]string{
		`event: response.reasoning_summary_text.delta`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"plan"}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"pong"}`,
		``,
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"","status":"in_progress"}}`,
		``,
		`event: response.function_call_arguments.delta`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"query\":"}`,
		``,
		`event: response.function_call_arguments.done`,
		`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"query\":\"weather\"}"}`,
		``,
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"query\":\"weather\"}","status":"completed"}}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":123,"status":"incomplete","model":"upstream-model","output":[],"usage":{"input_tokens":7,"output_tokens":11,"total_tokens":18},"incomplete_details":{"reason":"max_output_tokens"}}}`,
		``,
	}, "\n")

	resp, err := PipeResponsesStreamToChat(context.Background(), strings.NewReader(input), &bytes.Buffer{}, &ChatRequest{Model: "client-model"}, TranslateOpts{})
	if err != nil {
		t.Fatalf("PipeResponsesStreamToChat returned error: %v", err)
	}
	if resp.ID != "resp_1" {
		t.Fatalf("id = %q, want resp_1", resp.ID)
	}
	if resp.Created != 123 {
		t.Fatalf("created = %d, want 123", resp.Created)
	}
	if resp.Model != "upstream-model" {
		t.Fatalf("model = %q, want upstream-model", resp.Model)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 7 || resp.Usage.CompletionTokens != 11 || resp.Usage.TotalTokens != 18 {
		t.Fatalf("usage = %+v, want 7/11/18", resp.Usage)
	}
	choice := resp.Choices[0]
	if choice.FinishReason != "length" {
		t.Fatalf("finish_reason = %q, want length", choice.FinishReason)
	}
	if choice.Message.ReasoningContent == nil || *choice.Message.ReasoningContent != "plan" {
		t.Fatalf("reasoning = %v, want plan", choice.Message.ReasoningContent)
	}
	if choice.Message.Content == nil || *choice.Message.Content != "pong" {
		t.Fatalf("content = %v, want pong", choice.Message.Content)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(choice.Message.ToolCalls))
	}
	tool := choice.Message.ToolCalls[0]
	if tool.ID != "call_1" || tool.Function.Name != "lookup" || tool.Function.Arguments != `{"query":"weather"}` {
		t.Fatalf("tool call = %+v", tool)
	}
}

func TestPipeResponsesStreamToChatFallsBackToCompletedSnapshot(t *testing.T) {
	input := strings.Join([]string{
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_snapshot","object":"response","created_at":456,"status":"completed","model":"snapshot-model","output":[{"id":"rs_1","type":"reasoning","encrypted_content":"think","summary":[{"type":"summary_text","text":"think"}],"status":"completed"},{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello"}]},{"id":"fc_2","type":"function_call","call_id":"call_2","name":"search","arguments":"{\"q\":\"codex\"}","status":"completed"}],"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}}`,
		``,
	}, "\n")

	var sink bytes.Buffer
	resp, err := PipeResponsesStreamToChat(context.Background(), strings.NewReader(input), &sink, &ChatRequest{Model: "client-model"}, TranslateOpts{})
	if err != nil {
		t.Fatalf("PipeResponsesStreamToChat returned error: %v", err)
	}
	choice := resp.Choices[0]
	if resp.ID != "resp_snapshot" || resp.Created != 456 || resp.Model != "snapshot-model" {
		t.Fatalf("metadata = id:%q created:%d model:%q", resp.ID, resp.Created, resp.Model)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 3 || resp.Usage.CompletionTokens != 4 || resp.Usage.TotalTokens != 7 {
		t.Fatalf("usage = %+v, want 3/4/7", resp.Usage)
	}
	if choice.Message.ReasoningContent == nil || *choice.Message.ReasoningContent != "think" {
		t.Fatalf("reasoning = %v, want think", choice.Message.ReasoningContent)
	}
	if choice.Message.Content == nil || *choice.Message.Content != "hello" {
		t.Fatalf("content = %v, want hello", choice.Message.Content)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(choice.Message.ToolCalls))
	}
	tool := choice.Message.ToolCalls[0]
	if tool.ID != "call_2" || tool.Function.Name != "search" || tool.Function.Arguments != `{"q":"codex"}` {
		t.Fatalf("tool call = %+v", tool)
	}
	streamOutput := sink.String()
	for _, expected := range []string{"hello", "think", "call_2", `\"q\":\"codex\"`, "[DONE]"} {
		if !strings.Contains(streamOutput, expected) {
			t.Fatalf("stream output missing %q: %s", expected, streamOutput)
		}
	}
}
