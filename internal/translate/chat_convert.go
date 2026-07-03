package translate

// ChatToResponses converts a ChatRequest to a ResponsesRequest.
func ChatToResponses(req *ChatRequest) *ResponsesRequest {
	resp, _ := ReqToResponses(req, TranslateOpts{ForceParallelTools: true})
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
