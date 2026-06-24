package translate

import (
	"encoding/json"
	"strings"
)

var serverSideTools = map[string]bool{
	"code_interpreter":     true,
	"file_search":          true,
	"image_generation":     true,
	"computer_use_preview": true,
	"computer_use":         true,
}

var localShellTool = ChatTool{
	Type: "function",
	Function: ChatToolDef{
		Name:        "shell",
		Description: "Execute a shell command on the local machine. Returns stdout, stderr and exit code.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Argv array, e.g. [\"ls\", \"-la\"].",
				},
				"workdir": map[string]interface{}{
					"type":        "string",
					"description": "Working directory to run the command in (optional).",
				},
				"timeout_ms": map[string]interface{}{
					"type":        "number",
					"description": "Timeout in milliseconds (optional, default 30000).",
				},
			},
			"required": []string{"command"},
		},
	},
}

func ToolToChat(t ResponsesTool, opts TranslateOpts) []ChatTool {
	switch t.Type {
	case "function":
		return toolFunctionToChat(t)
	case "local_shell":
		return []ChatTool{localShellTool}
	case "web_search", "web_search_preview":
		return toolWebSearchToChat(t, opts)
	case "custom":
		return toolCustomToChat(t)
	case "namespace":
		return toolNamespaceToChat(t, opts)
	case "mcp":
		return nil
	case "tool_search":
		return toolSearchToChat(t)
	default:
		if serverSideTools[t.Type] {
			return nil
		}
		return nil
	}
}

func toolFunctionToChat(t ResponsesTool) []ChatTool {
	if t.Name == "" {
		return nil
	}
	fn := ChatToolDef{
		Name:        t.Name,
		Description: t.Description,
		Parameters:  t.Parameters,
	}
	if t.Strict != nil {
		fn.Strict = t.Strict
	}
	return []ChatTool{{Type: "function", Function: fn}}
}

func toolWebSearchToChat(t ResponsesTool, opts TranslateOpts) []ChatTool {
	if !opts.EnableWebSearch {
		return nil
	}
	return []ChatTool{{Type: "web_search"}}
}

func toolCustomToChat(t ResponsesTool) []ChatTool {
	if t.Name == "" {
		return nil
	}
	return []ChatTool{
		{
			Type: "function",
			Function: ChatToolDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"input": map[string]interface{}{
							"type":        "string",
							"description": "Input text for the tool.",
						},
					},
					"additionalProperties": true,
				},
			},
		},
	}
}

func toolNamespaceToChat(t ResponsesTool, opts TranslateOpts) []ChatTool {
	if len(t.Tools) == 0 {
		return nil
	}
	var result []ChatTool
	for _, inner := range t.Tools {
		converted := ToolToChat(inner, opts)
		result = append(result, converted...)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func toolSearchToChat(t ResponsesTool) []ChatTool {
	fn := ChatToolDef{Name: "tool_search"}
	if t.Description != "" {
		fn.Description = t.Description
	}
	if t.Parameters != nil {
		fn.Parameters = t.Parameters
	}
	return []ChatTool{{Type: "function", Function: fn}}
}

func ToolChoiceToChat(tc interface{}) interface{} {
	if tc == nil {
		return nil
	}
	switch v := tc.(type) {
	case string:
		return v
	case map[string]interface{}:
		if v["type"] == "function" {
			name := ""
			if fn, ok := v["function"].(map[string]interface{}); ok {
				name, _ = fn["name"].(string)
			}
			if name == "" {
				name, _ = v["name"].(string)
			}
			if name != "" {
				return map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name": name,
					},
				}
			}
		}
	}
	return nil
}

func ToolToResponses(t ChatTool) ResponsesTool {
	return ResponsesTool{
		Type:        "function",
		Name:        t.Function.Name,
		Description: t.Function.Description,
		Parameters:  t.Function.Parameters,
		Strict:      t.Function.Strict,
	}
}

func DedupeChatTools(tools []ChatTool) []ChatTool {
	seen := make(map[string]bool)
	var result []ChatTool
	for _, t := range tools {
		var key string
		if t.Type == "function" {
			key = "fn:" + t.Function.Name
		} else {
			key = "builtin:" + t.Type
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, t)
	}
	return result
}

func CollectFirstPartyConnectorLabels(tools []ResponsesTool) []string {
	var labels []string
	for _, t := range tools {
		if t.Type != "mcp" || t.ConnectorID == "" {
			continue
		}
		label := t.ServerLabel
		if label == "" {
			label = t.ConnectorID
		}
		labels = append(labels, label)
	}
	return labels
}

func BuildConnectorAdvisoryNote(labels []string) string {
	list := ""
	for i, l := range labels {
		if i > 0 {
			list += ", "
		}
		list += `"` + l + `"`
	}
	return "Note from the proxy: the following connector plugins are enabled — " + list +
		" — but these are NOT available through this proxy. " +
		"The upstream provider does not implement OpenAI's MCP runtime. " +
		"Use shell commands as alternatives where possible."
}

func SanitizeFunctionCallArguments(raw string) string {
	if raw == "" {
		return "{}"
	}
	var js interface{}
	if err := json.Unmarshal([]byte(raw), &js); err != nil {
		return "{}"
	}
	return raw
}

func SalvageToolCallArguments(raw string) string {
	if raw == "" {
		return ""
	}
	var js interface{}
	if err := json.Unmarshal([]byte(raw), &js); err != nil {
		return "{}"
	}
	return raw
}

func ModelSupportsImages(model string) bool {
	lower := strings.ToLower(model)
	if strings.Contains(lower, "omni") {
		return true
	}
	if lower == "mimo-v2.5" {
		return true
	}
	return false
}
