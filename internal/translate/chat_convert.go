package translate

// ChatToResponses converts a ChatRequest to a ResponsesRequest.
func ChatToResponses(req *ChatRequest) *ResponsesRequest {
	resp := &ResponsesRequest{
		Model:           req.Model,
		Stream:          req.Stream,
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		MaxOutputTokens: req.MaxCompletionTokens,
	}

	// Convert messages to input
	if len(req.Messages) > 0 {
		// Check if first message is system
		var inputItems []interface{}
		for _, msg := range req.Messages {
			if msg.Role == "system" || msg.Role == "developer" {
				// Use instructions field for system messages
				if content, ok := msg.Content.(string); ok {
					resp.Instructions = content
				}
				continue
			}

			// Convert message to input item
			item := map[string]interface{}{
				"type": "message",
				"role": msg.Role,
			}

			// Handle content
			if content, ok := msg.Content.(string); ok {
				item["content"] = []map[string]interface{}{
					{
						"type": "input_text",
						"text": content,
					},
				}
			} else {
				item["content"] = msg.Content
			}

			// Handle tool calls in assistant messages
			if len(msg.ToolCalls) > 0 {
				item["type"] = "function_call"
				for _, tc := range msg.ToolCalls {
					callItem := map[string]interface{}{
						"type":      "function_call",
						"id":        tc.ID,
						"call_id":   tc.ID,
						"name":      tc.Function.Name,
						"arguments": tc.Function.Arguments,
					}
					inputItems = append(inputItems, callItem)
				}
				continue
			}

			// Handle tool results
			if msg.ToolCallID != "" {
				item["type"] = "function_call_output"
				item["call_id"] = msg.ToolCallID
				item["output"] = msg.Content
				delete(item, "role")
				delete(item, "content")
			}

			inputItems = append(inputItems, item)
		}

		if len(inputItems) == 1 {
			// Single item, check if it's a simple message
			if m, ok := inputItems[0].(map[string]interface{}); ok {
				if m["type"] == "message" {
					if content, ok := m["content"].([]map[string]interface{}); ok && len(content) == 1 {
						if content[0]["type"] == "input_text" {
							resp.Input = content[0]["text"]
							return resp
						}
					}
				}
			}
			resp.Input = inputItems[0]
		} else {
			resp.Input = inputItems
		}
	}

	// Convert tools
	if len(req.Tools) > 0 {
		var tools []ResponsesTool
		for _, t := range req.Tools {
			if t.Type == "function" {
				tools = append(tools, ResponsesTool{
					Type:        "function",
					Name:        t.Function.Name,
					Description: t.Function.Description,
					Parameters:  t.Function.Parameters,
				})
			}
		}
		resp.Tools = tools
	}

	// Convert tool choice
	if req.ToolChoice != nil {
		resp.ToolChoice = req.ToolChoice
	}

	// Parallel tool calls
	if req.ParallelToolCalls != nil {
		resp.ParallelToolCalls = req.ParallelToolCalls
	}

	return resp
}

// ChatToClaude converts a ChatRequest to a ClaudeRequest.
func ChatToClaude(req *ChatRequest) *ClaudeRequest {
	result, _ := ChatToClaudeRequest(req)
	return result
}

// ChatToGemini converts a ChatRequest to a GeminiRequest.
func ChatToGemini(req *ChatRequest) *GeminiRequest {
	result, _ := ChatToGeminiRequest(req)
	return result
}
