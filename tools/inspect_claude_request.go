package main

import (
	"encoding/json"
	"fmt"

	"github.com/fangxiusun/ai-adapter/internal/translate"
)

func main() {
	req := &translate.ChatRequest{
		Model:               "mimo-v2.5-pro",
		MaxCompletionTokens: intPtr(256),
		Messages: []translate.ChatMessage{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "用一句话介绍 Go 语言"},
		},
	}

	claudeReq, err := translate.ChatToClaudeRequest(req)
	if err != nil {
		panic(err)
	}

	claudeReq.Stream = false

	b, _ := json.MarshalIndent(claudeReq, "", "  ")
	fmt.Println(string(b))
}

func intPtr(v int) *int { return &v }
