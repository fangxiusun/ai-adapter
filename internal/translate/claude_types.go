package translate

// Claude/Anthropic Messages API request/response types.

// ClaudeRequest represents a request to the Anthropic Messages API.
type ClaudeRequest struct {
	Model         string          `json:"model"`
	MaxTokens     int             `json:"max_tokens"`
	System        interface{}     `json:"system,omitempty"`
	Messages      []ClaudeMessage `json:"messages"`
	Stream        bool            `json:"stream,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	TopK          *int            `json:"top_k,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Tools         []ClaudeTool    `json:"tools,omitempty"`
	ToolChoice    interface{}     `json:"tool_choice,omitempty"`
	Metadata      interface{}     `json:"metadata,omitempty"`
}

type ClaudeMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type ClaudeContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// tool_use
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
	// tool_result
	ToolUseID string      `json:"tool_use_id,omitempty"`
	Content   interface{} `json:"content,omitempty"`
	IsError   *bool       `json:"is_error,omitempty"`
	// image
	Source *ClaudeImageSource `json:"source,omitempty"`
}

type ClaudeImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type ClaudeTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type ClaudeResponse struct {
	ID           string               `json:"id"`
	Type         string               `json:"type"`
	Role         string               `json:"role"`
	Content      []ClaudeContentBlock `json:"content"`
	Model        string               `json:"model"`
	StopReason   string               `json:"stop_reason"`
	StopSequence string               `json:"stop_sequence,omitempty"`
	Usage        ClaudeUsage          `json:"usage"`
}

type ClaudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// Claude streaming event types.

type ClaudeStreamEvent struct {
	Type         string             `json:"type"`
	Message      *ClaudeResponse    `json:"message,omitempty"`
	Index        int                `json:"index,omitempty"`
	ContentBlock *ClaudeContentBlock `json:"content_block,omitempty"`
	Delta        *ClaudeDelta       `json:"delta,omitempty"`
	Usage        *ClaudeUsage       `json:"usage,omitempty"`
	Error        *ClaudeStreamError `json:"error,omitempty"`
}

type ClaudeDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

type ClaudeStreamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}