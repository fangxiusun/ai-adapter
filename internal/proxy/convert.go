package proxy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/translate"
)

// convertChatToSource converts a ChatRequest to the source interface format.
func convertChatToSource(source config.InterfaceType, chatReq *translate.ChatRequest) (interface{}, error) {
	switch source {
	case config.InterfaceChat:
		return chatReq, nil
	case config.InterfaceResponses:
		return translate.ReqToResponses(chatReq, translate.TranslateOpts{ForceParallelTools: true})
	case config.InterfaceMessages:
		return translate.ChatToClaudeRequest(chatReq)
	case config.InterfaceGenerateContent:
		return translate.ChatToGeminiRequest(chatReq)
	default:
		return nil, fmt.Errorf("unsupported source interface: %s", source)
	}
}

// convertSourceToChat converts a source response body to a ChatResponse.
func convertSourceToChat(source config.InterfaceType, body []byte, chatReq *translate.ChatRequest) (*translate.ChatResponse, error) {
	switch source {
	case config.InterfaceChat:
		var resp translate.ChatResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("unmarshal chat response: %w", err)
		}
		return &resp, nil
	case config.InterfaceResponses:
		var resp translate.ResponsesObject
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("unmarshal responses response: %w", err)
		}
		return translate.RespToChat(&resp, chatReq, translate.TranslateOpts{}), nil
	case config.InterfaceMessages:
		var resp translate.ClaudeResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("unmarshal claude response: %w", err)
		}
		return translate.ClaudeToChatResponse(&resp), nil
	case config.InterfaceGenerateContent:
		var resp translate.GeminiResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("unmarshal gemini response: %w", err)
		}
		return translate.GeminiToChatResponse(&resp), nil
	default:
		return nil, fmt.Errorf("unsupported source interface: %s", source)
	}
}

// convertChatToTarget converts a ChatResponse to the target interface format.
func convertChatToTarget(target config.InterfaceType, chatResp *translate.ChatResponse, req interface{}) (interface{}, error) {
	switch target {
	case config.InterfaceChat:
		return chatResp, nil
	case config.InterfaceResponses:
		respReq, _ := req.(*translate.ResponsesRequest)
		if respReq == nil {
			respReq = &translate.ResponsesRequest{}
		}
		return translate.RespToResponses(chatResp, respReq, translate.TranslateOpts{}), nil
	case config.InterfaceMessages:
		return translate.ChatToClaudeResponse(chatResp), nil
	case config.InterfaceGenerateContent:
		return translate.ChatToGeminiResponse(chatResp), nil
	default:
		return nil, fmt.Errorf("unsupported target interface: %s", target)
	}
}

// extractGeminiModel extracts the model name from a Gemini URL path.
func extractGeminiModel(path string) string {
	prefix := "/v1beta/models/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.SplitN(rest, ":", 2)
	return parts[0]
}


// upstreamPathForInterface returns the URL path for calling the upstream interface.
func upstreamPathForInterface(iface config.InterfaceType, model string, stream bool) string {
	switch iface {
	case config.InterfaceChat:
		return "/v1/chat/completions"
	case config.InterfaceResponses:
		return "/v1/responses"
	case config.InterfaceMessages:
		return "/v1/messages"
	case config.InterfaceGenerateContent:
		if stream {
			return "/v1beta/models/" + model + ":streamGenerateContent?alt=sse"
		}
		return "/v1beta/models/" + model + ":generateContent"
	default:
		return ""
	}
}
