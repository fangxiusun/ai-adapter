package config

import (
	"fmt"
	"math"
	"strings"
)

// InterfaceType represents one of the four supported upstream interface protocols.
type InterfaceType string

const (
	InterfaceChat            InterfaceType = "chat"
	InterfaceResponses       InterfaceType = "responses"
	InterfaceMessages        InterfaceType = "messages"
	InterfaceGenerateContent InterfaceType = "generate_content"
)

// AllInterfaces is the stable-ordered list of all interface types.
// Priority order: chat > responses > messages > generate_content
var AllInterfaces = []InterfaceType{
	InterfaceChat,
	InterfaceResponses,
	InterfaceMessages,
	InterfaceGenerateContent,
}

// interfacePriority returns the tie-breaking priority (lower = preferred).
var interfacePriority = map[InterfaceType]int{
	InterfaceChat:            0,
	InterfaceResponses:       1,
	InterfaceMessages:        2,
	InterfaceGenerateContent: 3,
}

// ConversionComplexity defines the static cost of converting from source to target.
// Native forwarding (same source==target) is always 0.
// Scores: same-family cross-interface = 10, cross-2-protocol = 20, cross-3-protocol = 30.
var ConversionComplexity = map[InterfaceType]map[InterfaceType]int{
	InterfaceChat: {
		InterfaceChat:            0,
		InterfaceResponses:       10, // same family (OpenAI)
		InterfaceMessages:        20, // cross protocol
		InterfaceGenerateContent: 30, // cross protocol (furthest)
	},
	InterfaceResponses: {
		InterfaceChat:            10,
		InterfaceResponses:       0,
		InterfaceMessages:        20,
		InterfaceGenerateContent: 30,
	},
	InterfaceMessages: {
		InterfaceChat:            20,
		InterfaceResponses:       20,
		InterfaceMessages:        0,
		InterfaceGenerateContent: 30,
	},
	InterfaceGenerateContent: {
		InterfaceChat:            30,
		InterfaceResponses:       30,
		InterfaceMessages:        30,
		InterfaceGenerateContent: 0,
	},
}

// BestSourceForTarget selects the lowest-cost source interface that the channel
// has a native URL for, which can be converted to the target interface.
// Returns the source interface and true if found.
// Native target is always preferred (complexity 0).
// Ties are broken by stable priority: chat > responses > messages > generate_content.
func BestSourceForTarget(target InterfaceType, ch *ChannelConfig) (InterfaceType, bool) {
	if ch.HasNative(target) {
		return target, true
	}

	var best InterfaceType
	bestScore := math.MaxInt
	bestPriority := math.MaxInt

	for _, src := range AllInterfaces {
		if !ch.HasNative(src) {
			continue
		}
		if src == target {
			continue
		}
		tgtMap, ok := ConversionComplexity[src]
		if !ok {
			continue
		}
		score, ok := tgtMap[target]
		if !ok {
			continue
		}
		priority := interfacePriority[src]
		if score < bestScore || (score == bestScore && priority < bestPriority) {
			best = src
			bestScore = score
			bestPriority = priority
		}
	}

	if best == "" {
		return "", false
	}
	return best, true
}

// BuildUpstreamPath returns the URL path suffix for the given interface and model.
// The base URL comes from the channel capability URL field.
func BuildUpstreamPath(iface InterfaceType, model string) string {
	switch iface {
	case InterfaceChat:
		return "/v1/chat/completions"
	case InterfaceResponses:
		return "/v1/responses"
	case InterfaceMessages:
		return "/v1/messages"
	case InterfaceGenerateContent:
		return "/v1beta/models/" + model + ":generateContent"
	default:
		return ""
	}
}

// BuildUpstreamStreamPath returns the streaming URL path suffix.
func BuildUpstreamStreamPath(iface InterfaceType, model string) string {
	switch iface {
	case InterfaceGenerateContent:
		return "/v1beta/models/" + model + ":streamGenerateContent?alt=sse"
	default:
		return BuildUpstreamPath(iface, model)
	}
}

// ParseInterfaceType converts a string to InterfaceType, returning error for unknown values.
func ParseInterfaceType(s string) (InterfaceType, error) {
	switch strings.ToLower(s) {
	case "chat":
		return InterfaceChat, nil
	case "responses":
		return InterfaceResponses, nil
	case "messages":
		return InterfaceMessages, nil
	case "generate_content":
		return InterfaceGenerateContent, nil
	default:
		return "", fmt.Errorf("unknown interface type: %s", s)
	}
}