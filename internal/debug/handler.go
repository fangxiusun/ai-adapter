package debug

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/translate"
)

// Handler provides debug/dry-run endpoints that return curl commands
// instead of actually making upstream requests.
type Handler struct {
	channels *channel.ChannelManager
	config   *config.Config
}

// NewHandler creates a new debug handler.
func NewHandler(channels *channel.ChannelManager, cfg *config.Config) *Handler {
	return &Handler{channels: channels, config: cfg}
}

// RegisterRoutes registers the debug curl endpoints.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/curl/v1/chat/completions", h.handleCurlChat)
	mux.HandleFunc("/curl/v1/responses", h.handleCurlResponses)
	mux.HandleFunc("/curl/v1/messages", h.handleCurlMessages)
	mux.HandleFunc("/curl/v1beta/models/", h.handleCurlGenerateContent)
}

// handleCurlChat handles /curl/v1/chat/completions
func (h *Handler) handleCurlChat(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.returnTemplate(w, config.InterfaceChat)
		return
	}
	h.handlePost(w, r, config.InterfaceChat)
}

// handleCurlResponses handles /curl/v1/responses
func (h *Handler) handleCurlResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.returnTemplate(w, config.InterfaceResponses)
		return
	}
	h.handlePost(w, r, config.InterfaceResponses)
}

// handleCurlMessages handles /curl/v1/messages
func (h *Handler) handleCurlMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.returnTemplate(w, config.InterfaceMessages)
		return
	}
	h.handlePost(w, r, config.InterfaceMessages)
}

// handleCurlGenerateContent handles /curl/v1beta/models/{model}:generateContent
func (h *Handler) handleCurlGenerateContent(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.returnTemplate(w, config.InterfaceGenerateContent)
		return
	}
	h.handlePost(w, r, config.InterfaceGenerateContent)
}

// returnTemplate returns a curl command template for the given interface.
func (h *Handler) returnTemplate(w http.ResponseWriter, iface config.InterfaceType) {
	template := h.buildCurlTemplate(iface)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(template))
}

// handlePost processes POST requests and returns the upstream curl command.
func (h *Handler) handlePost(w http.ResponseWriter, r *http.Request, target config.InterfaceType) {
	body, err := readBody(r)
	if err != nil {
		writeError(w, 400, "read_body_failed", err.Error())
		return
	}

	// Parse the request to get model
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, 400, "invalid_json", err.Error())
		return
	}

	// Extract model for Gemini from URL path
	model := req.Model
	if target == config.InterfaceGenerateContent {
		model = extractGeminiModel(r.URL.Path)
		if model == "" {
			model = req.Model
		}
	}

	if model == "" {
		writeError(w, 400, "missing_model", "model is required")
		return
	}

	// Find channel for this model
	ch, modelInfo, err := h.channels.SelectChannel(model)
	if err != nil {
		writeError(w, 404, "no_channel", err.Error())
		return
	}

	upstreamModel := model
	if modelInfo != nil && modelInfo.ID != "" {
		upstreamModel = modelInfo.ID
	}

	// Determine source interface
	source, ok := config.BestSourceForTarget(target, &ch.Config)
	if !ok {
		writeError(w, 503, "no_conversion_path",
			fmt.Sprintf("channel %s has no native interface and no conversion path to %s", ch.Config.ID, target))
		return
	}

	// Build upstream curl command
	curlCmd := h.buildUpstreamCurl(ch, source, target, upstreamModel, body, r)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(curlCmd))
}

// buildCurlTemplate builds a curl command template for the given interface.
func (h *Handler) buildCurlTemplate(iface config.InterfaceType) string {
	serverAddr := fmt.Sprintf("%s:%d", h.config.Server.Host, h.config.Server.Port)
	if serverAddr == "0.0.0.0:8080" {
		serverAddr = "localhost:8080"
	}

	apiToken := h.config.Server.APIToken
	if apiToken == "" {
		apiToken = "YOUR_API_TOKEN"
	}

	var template string
	switch iface {
	case config.InterfaceChat:
		template = fmt.Sprintf(`curl -s -X POST http://%s/v1/chat/completions \
  -H "Authorization: Bearer %s" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mimo-v2.5-pro",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "用一句话介绍 Go 语言"}
    ],
    "stream": false,
    "temperature": 0.7,
    "top_p": 1.0,
    "max_completion_tokens": 256,
    "stop": ["\n"],
    "tools": [
      {
        "type": "function",
        "function": {
          "name": "get_weather",
          "description": "Get the current weather",
          "parameters": {
            "type": "object",
            "properties": {
              "location": {"type": "string"}
            },
            "required": ["location"]
          }
        }
      }
    ],
    "tool_choice": "auto",
    "parallel_tool_calls": true,
    "stream_options": {"include_usage": true},
    "reasoning_effort": "medium"
  }'`, serverAddr, apiToken)

	case config.InterfaceResponses:
		template = fmt.Sprintf(`curl -s -X POST http://%s/v1/responses \
  -H "Authorization: Bearer %s" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mimo-v2.5-pro",
    "input": "用一句话介绍 Go 语言",
    "instructions": "You are a helpful assistant.",
    "stream": false,
    "temperature": 0.7,
    "top_p": 1.0,
    "max_output_tokens": 256,
    "tools": [
      {
        "type": "function",
        "name": "get_weather",
        "description": "Get the current weather",
        "parameters": {
          "type": "object",
          "properties": {
            "location": {"type": "string"}
          },
          "required": ["location"]
        }
      }
    ],
    "tool_choice": "auto",
    "parallel_tool_calls": true,
    "reasoning": {
      "effort": "medium",
      "summary": "auto"
    },
    "metadata": {"user_id": "user123"}
  }'`, serverAddr, apiToken)

	case config.InterfaceMessages:
		template = fmt.Sprintf(`curl -s -X POST http://%s/v1/messages \
  -H "Authorization: Bearer %s" \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "mimo-v2.5-pro",
    "max_tokens": 256,
    "system": "You are a helpful assistant.",
    "messages": [
      {"role": "user", "content": "用一句话介绍 Go 语言"}
    ],
    "stream": false,
    "temperature": 0.7,
    "top_p": 1.0,
    "stop_sequences": ["\n"],
    "tools": [
      {
        "name": "get_weather",
        "description": "Get the current weather",
        "input_schema": {
          "type": "object",
          "properties": {
            "location": {"type": "string"}
          },
          "required": ["location"]
        }
      }
    ],
    "tool_choice": {"type": "auto"},
    "metadata": {"user_id": "user123"}
  }'`, serverAddr, apiToken)

	case config.InterfaceGenerateContent:
		template = fmt.Sprintf(`curl -s -X POST http://%s/v1beta/models/mimo-v2.5-pro:generateContent \
  -H "Authorization: Bearer %s" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [
      {
        "role": "user",
        "parts": [{"text": "用一句话介绍 Go 语言"}]
      }
    ],
    "systemInstruction": {
      "parts": [{"text": "You are a helpful assistant."}]
    },
    "generationConfig": {
      "temperature": 0.7,
      "topP": 1.0,
      "maxOutputTokens": 256,
      "stopSequences": ["\n"]
    },
    "tools": [
      {
        "functionDeclarations": [
          {
            "name": "get_weather",
            "description": "Get the current weather",
            "parameters": {
              "type": "object",
              "properties": {
                "location": {"type": "string"}
              },
              "required": ["location"]
            }
          }
        ]
      }
    ],
    "toolConfig": {
      "functionCallingConfig": {
        "mode": "AUTO"
      }
    }
  }'`, serverAddr, apiToken)
	}

	return template
}

// buildUpstreamCurl builds the actual upstream curl command that would be sent.
func (h *Handler) buildUpstreamCurl(ch *channel.Channel, source, target config.InterfaceType, model string, body []byte, r *http.Request) string {
	baseURL := ch.Config.NativeBaseURL(source)
	if baseURL == "" {
		return "# Error: no base URL configured for interface " + string(source)
	}

	stream := false
	if target == config.InterfaceGenerateContent {
		stream = strings.Contains(r.URL.Path, "streamGenerateContent")
	} else {
		var req struct {
			Stream bool `json:"stream"`
		}
		json.Unmarshal(body, &req)
		stream = req.Stream
	}

	// Build path
	var path string
	switch source {
	case config.InterfaceChat:
		path = "/v1/chat/completions"
	case config.InterfaceResponses:
		path = "/v1/responses"
	case config.InterfaceMessages:
		path = "/v1/messages"
	case config.InterfaceGenerateContent:
		if stream {
			path = fmt.Sprintf("/v1beta/models/%s:streamGenerateContent?alt=sse", model)
		} else {
			path = fmt.Sprintf("/v1beta/models/%s:generateContent", model)
		}
	}

	url := baseURL + path

	// Convert request body if needed
	var upstreamBody []byte
	var err error

	if source == target {
		// Native forward, use original body
		upstreamBody = body
	} else {
		// Need conversion
		upstreamBody, err = h.convertRequest(source, target, model, stream, body)
		if err != nil {
			return fmt.Sprintf("# Error converting request: %v", err)
		}
	}

	// Get auth header from request or use placeholder
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		authHeader = "Bearer " + h.getFirstKey(ch)
	}

	// Build curl command
	curlCmd := fmt.Sprintf(`curl -s -X POST %s \
  -H "Authorization: %s" \
  -H "Content-Type: application/json"`, url, authHeader)

	// Add anthropic-version header for Claude
	if source == config.InterfaceMessages {
		anthropicVersion := r.Header.Get("anthropic-version")
		if anthropicVersion == "" {
			anthropicVersion = "2023-06-01"
		}
		curlCmd += fmt.Sprintf(` \
  -H "anthropic-version: %s"`, anthropicVersion)
	}

	// Format JSON body
	var prettyJSON map[string]interface{}
	if json.Unmarshal(upstreamBody, &prettyJSON) == nil {
		prettyBytes, _ := json.MarshalIndent(prettyJSON, "  ", "  ")
		curlCmd += fmt.Sprintf(` \
  -d '%s'`, string(prettyBytes))
	} else {
		curlCmd += fmt.Sprintf(` \
  -d '%s'`, string(upstreamBody))
	}

	return curlCmd
}

// convertRequest converts the request body from target format to source format.
func (h *Handler) convertRequest(source, target config.InterfaceType, model string, stream bool, body []byte) ([]byte, error) {
	// First parse as the target format, then convert to source format
	switch target {
	case config.InterfaceChat:
		var req translate.ChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		req.Model = model
		req.Stream = stream
		return h.chatToSource(source, &req)

	case config.InterfaceResponses:
		var req translate.ResponsesRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		req.Model = model
		req.Stream = stream
		chatReq, err := translate.ReqToChat(&req, translate.TranslateOpts{ForceParallelTools: true})
		if err != nil {
			return nil, err
		}
		return h.chatToSource(source, chatReq)

	case config.InterfaceMessages:
		var req translate.ClaudeRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		req.Model = model
		chatReq, err := translate.ClaudeToChatRequest(&req)
		if err != nil {
			return nil, err
		}
		chatReq.Stream = stream
		return h.chatToSource(source, chatReq)

	case config.InterfaceGenerateContent:
		var req translate.GeminiRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		chatReq, err := translate.GeminiToChatRequest(&req)
		if err != nil {
			return nil, err
		}
		chatReq.Model = model
		chatReq.Stream = stream
		return h.chatToSource(source, chatReq)
	}

	return body, nil
}

// chatToSource converts a ChatRequest to the source format.
func (h *Handler) chatToSource(source config.InterfaceType, req *translate.ChatRequest) ([]byte, error) {
	switch source {
	case config.InterfaceChat:
		return json.Marshal(req)

	case config.InterfaceResponses:
		respReq := translate.ChatToResponses(req)
		return json.Marshal(respReq)

	case config.InterfaceMessages:
		claudeReq := translate.ChatToClaude(req)
		return json.Marshal(claudeReq)

	case config.InterfaceGenerateContent:
		geminiReq := translate.ChatToGemini(req)
		return json.Marshal(geminiReq)
	}

	return nil, fmt.Errorf("unsupported source interface: %s", source)
}

// getFirstKey returns the first available key from the channel.
func (h *Handler) getFirstKey(ch *channel.Channel) string {
	keys := ch.Config.Keys
	if len(keys) > 0 {
		return keys[0].Value
	}
	return "YOUR_API_KEY"
}

// extractGeminiModel extracts the model name from Gemini URL path.
func extractGeminiModel(path string) string {
	// Path format: /curl/v1beta/models/{model}:generateContent or :streamGenerateContent
	prefix := "/curl/v1beta/models/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	// Find the colon
	idx := strings.Index(rest, ":")
	if idx < 0 {
		return rest
	}
	return rest[:idx]
}

// readBody reads the request body.
func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return buf, err
		}
	}
	return buf, nil
}

// writeError writes an error response.
func writeError(w http.ResponseWriter, code int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    errType,
			"message": message,
		},
	})
}
