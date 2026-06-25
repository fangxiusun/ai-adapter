package translate

// Gemini generateContent API request/response types.

type GeminiRequest struct {
	Contents         []GeminiContent    `json:"contents"`
	SystemInstruction *GeminiContent    `json:"system_instruction,omitempty"`
	GenerationConfig  *GeminiGenConfig  `json:"generation_config,omitempty"`
	Tools             []GeminiTool      `json:"tools,omitempty"`
	ToolConfig        interface{}       `json:"tool_config,omitempty"`
	SafetySettings    []interface{}     `json:"safety_settings,omitempty"`
}

type GeminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GeminiPart `json:"parts"`
}

type GeminiPart struct {
	Text             string                 `json:"text,omitempty"`
	FunctionCall     *GeminiFunctionCall    `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"`
	InlineData       *GeminiInlineData      `json:"inlineData,omitempty"`
	Thought          bool                   `json:"thought,omitempty"`
}

type GeminiFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args,omitempty"`
}

type GeminiFunctionResponse struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

type GeminiGenConfig struct {
	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"topP,omitempty"`
	TopK             *int     `json:"topK,omitempty"`
	MaxOutputTokens  *int     `json:"maxOutputTokens,omitempty"`
	StopSequences    []string `json:"stopSequences,omitempty"`
	ResponseMimeType string   `json:"responseMimeType,omitempty"`
}

type GeminiTool struct {
	FunctionDeclarations []GeminiFunctionDecl `json:"functionDeclarations,omitempty"`
}

type GeminiFunctionDecl struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

type GeminiResponse struct {
	Candidates    []GeminiCandidate `json:"candidates,omitempty"`
	UsageMetadata *GeminiUsage      `json:"usageMetadata,omitempty"`
	ModelVersion  string            `json:"modelVersion,omitempty"`
}

type GeminiCandidate struct {
	Content      GeminiContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
	Index        int           `json:"index,omitempty"`
	SafetyRatings []interface{} `json:"safetyRatings,omitempty"`
}

type GeminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type GeminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// Gemini streaming: each line is a complete GeminiResponse JSON object.
type GeminiStreamChunk struct {
	Candidates    []GeminiCandidate `json:"candidates,omitempty"`
	UsageMetadata *GeminiUsage      `json:"usageMetadata,omitempty"`
}