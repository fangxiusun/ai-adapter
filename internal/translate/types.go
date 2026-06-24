package translate

// ============================================================================
// Responses API Types (what Codex sends)
// ============================================================================

type ResponsesRequest struct {
	Model             string              `json:"model"`
	Input             interface{}         `json:"input,omitempty"`
	Instructions      string              `json:"instructions,omitempty"`
	Tools             []ResponsesTool     `json:"tools,omitempty"`
	ToolChoice        interface{}         `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool               `json:"parallel_tool_calls,omitempty"`
	Temperature       *float64            `json:"temperature,omitempty"`
	TopP              *float64            `json:"top_p,omitempty"`
	MaxOutputTokens   *int                `json:"max_output_tokens,omitempty"`
	Stream            bool                `json:"stream,omitempty"`
	Reasoning         *ReasoningConfig    `json:"reasoning,omitempty"`
	Metadata          map[string]string   `json:"metadata,omitempty"`
	Text              *TextFormat         `json:"text,omitempty"`
}

type ReasoningConfig struct {
	Effort  *string `json:"effort,omitempty"`
	Summary *string `json:"summary,omitempty"`
}

type TextFormat struct {
	Type string `json:"type,omitempty"`
}

type ResponsesInputItem struct {
	Type             string                 `json:"type"`
	ID               string                 `json:"id,omitempty"`
	Role             string                 `json:"role,omitempty"`
	Content          interface{}            `json:"content,omitempty"`
	CallID           string                 `json:"call_id,omitempty"`
	Name             string                 `json:"name,omitempty"`
	Arguments        string                 `json:"arguments,omitempty"`
	Output           interface{}            `json:"output,omitempty"`
	Summary          []ReasoningSummaryPart `json:"summary,omitempty"`
	EncryptedContent *string                `json:"encrypted_content,omitempty"`
	Status           string                 `json:"status,omitempty"`
}

type ReasoningSummaryPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ResponsesContentPart struct {
	Type     string  `json:"type"`
	Text     string  `json:"text,omitempty"`
	ImageURL string  `json:"image_url,omitempty"`
	Detail   string  `json:"detail,omitempty"`
	Annotations []interface{} `json:"annotations,omitempty"`
}

type ResponsesTool struct {
	Type        string                 `json:"type"`
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
	Strict      *bool                  `json:"strict,omitempty"`
	Tools       []ResponsesTool        `json:"tools,omitempty"`
	ServerLabel string                 `json:"server_label,omitempty"`
	ConnectorID string                 `json:"connector_id,omitempty"`
	ServerURL   string                 `json:"server_url,omitempty"`
}

type ResponsesObject struct {
	ID                string              `json:"id"`
	Object            string              `json:"object"`
	CreatedAt         int64               `json:"created_at"`
	Status            string              `json:"status"`
	Model             string              `json:"model"`
	Output            []OutputItem        `json:"output"`
	Usage             *ResponsesUsage     `json:"usage"`
	ParallelToolCalls bool                `json:"parallel_tool_calls"`
	ToolChoice        interface{}         `json:"tool_choice"`
	Reasoning         *ReasoningResult    `json:"reasoning"`
	Text              *TextFormat         `json:"text"`
	IncompleteDetails *IncompleteDetails  `json:"incomplete_details"`
	Error             *ErrorInfo          `json:"error"`
	Metadata          map[string]string   `json:"metadata"`
}

type OutputItem struct {
	Type          string              `json:"type"`
	ID            string              `json:"id"`
	CallID        string              `json:"call_id,omitempty"`
	Name          string              `json:"name,omitempty"`
	Arguments     string              `json:"arguments,omitempty"`
	Role          string              `json:"role,omitempty"`
	Content       []OutputContentPart `json:"content,omitempty"`
	Summary       []ReasoningSummaryPart `json:"summary,omitempty"`
	EncryptedContent *string          `json:"encrypted_content,omitempty"`
	Status        string              `json:"status,omitempty"`
	Namespace     string              `json:"namespace,omitempty"`
}

type OutputContentPart struct {
	Type        string        `json:"type"`
	Text        string        `json:"text,omitempty"`
	Annotations []interface{} `json:"annotations,omitempty"`
}

type ResponsesUsage struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	TotalTokens       int `json:"total_tokens"`
	InputTokensDetails *TokenDetails `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *TokenDetails `json:"output_tokens_details,omitempty"`
}

type TokenDetails struct {
	CachedTokens    int `json:"cached_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

type ReasoningResult struct {
	Effort  *string `json:"effort"`
	Summary *string `json:"summary"`
}

type IncompleteDetails struct {
	Reason string `json:"reason"`
}

type ErrorInfo struct {
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// ============================================================================
// Chat Completions API Types (what upstream accepts)
// ============================================================================

type ChatRequest struct {
	Model               string          `json:"model"`
	Messages            []ChatMessage   `json:"messages"`
	Tools               []ChatTool      `json:"tools,omitempty"`
	ToolChoice          interface{}     `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
	StreamOptions       *StreamOptions  `json:"stream_options,omitempty"`
	Thinking            *ThinkingConfig `json:"thinking,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type ThinkingConfig struct {
	Type string `json:"type,omitempty"`
}

type ChatMessage struct {
	Role             string          `json:"role"`
	Content          interface{}     `json:"content,omitempty"`
	Name             string          `json:"name,omitempty"`
	ToolCalls        []ChatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	ReasoningContent *string         `json:"reasoning_content,omitempty"`
}

type ChatToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function FunctionCall   `json:"function"`
	Index    int            `json:"index,omitempty"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatTool struct {
	Type     string         `json:"type"`
	Function ChatToolDef    `json:"function,omitempty"`
}

type ChatToolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
	Strict      *bool                  `json:"strict,omitempty"`
}

type ChatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   *ChatUsage   `json:"usage,omitempty"`
}

type ChatChoice struct {
	Index        int              `json:"index"`
	Message      ChatChoiceMsg    `json:"message"`
	FinishReason string           `json:"finish_reason,omitempty"`
	Delta        *ChatStreamDelta `json:"delta,omitempty"`
}

type ChatChoiceMsg struct {
	Role             string          `json:"role"`
	Content          *string         `json:"content"`
	ReasoningContent *string         `json:"reasoning_content,omitempty"`
	ToolCalls        []ChatToolCall  `json:"tool_calls,omitempty"`
}

type ChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	PromptTokensDetails *TokenDetails `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *TokenDetails `json:"completion_tokens_details,omitempty"`
}

type ChatStreamChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []ChatStreamChoice `json:"choices"`
	Usage   *ChatUsage        `json:"usage,omitempty"`
}

type ChatStreamChoice struct {
	Index        int             `json:"index"`
	Delta        ChatStreamDelta `json:"delta"`
	FinishReason string          `json:"finish_reason,omitempty"`
}

type ChatStreamDelta struct {
	Role             string            `json:"role,omitempty"`
	Content          *string           `json:"content,omitempty"`
	ReasoningContent *string           `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCallDelta   `json:"tool_calls,omitempty"`
}

type ToolCallDelta struct {
	Index    int              `json:"index"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function *FunctionDelta   `json:"function,omitempty"`
}

type FunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ============================================================================
// Translation Options
// ============================================================================

type TranslateOpts struct {
	DisableThinking      bool
	ForceHighEffort      bool
	EnableWebSearch      bool
	ForceParallelTools   bool
	ExtractInlineThink   bool
	ImageDropDir         string
}

// ============================================================================
// Stream Types
// ============================================================================

type StreamResult struct {
	Usage          *ResponsesUsage
	Response       *ResponsesObject
	ToolCallCount  int
}

type StreamSSEEvent struct {
	Event string
	Data  map[string]interface{}
}
