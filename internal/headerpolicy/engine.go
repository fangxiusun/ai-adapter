package headerpolicy

import (
	"net/http"
	"strings"

	"github.com/fangxiusun/ai-adapter/internal/config"
)

// ==================== Engine ====================

// Engine processes HTTP headers according to configured policies.
type Engine struct {
	globalReq  *config.HeaderPolicyConfig
	globalRes  *config.HeaderPolicyConfig
	channels   map[string]*channelPolicy
}

// channelPolicy holds resolved policies for a channel and its models.
type channelPolicy struct {
	req    *config.HeaderPolicyConfig
	res    *config.HeaderPolicyConfig
	models map[string]*modelPolicy
}

// modelPolicy holds resolved policies for a specific model.
type modelPolicy struct {
	req *config.HeaderPolicyConfig
	res *config.HeaderPolicyConfig
}

// NewEngine creates a new header policy engine from config.
func NewEngine(cfg *config.Config) *Engine {
	e := &Engine{
		channels: make(map[string]*channelPolicy),
	}

	// Support both new simplified format and legacy format
	if cfg.Headers != nil {
		e.globalReq = cfg.Headers.Request
		e.globalRes = cfg.Headers.Response
	} else {
		e.globalReq = cfg.GlobalRequestHeaders
		e.globalRes = cfg.GlobalResponseHeaders
	}

	for _, ch := range cfg.Channels {
		cp := &channelPolicy{
			req:    ch.RequestHeaders,
			res:    ch.ResponseHeaders,
			models: make(map[string]*modelPolicy),
		}
		for _, m := range ch.Models {
			cp.models[m.ID] = &modelPolicy{
				req: m.RequestHeaders,
				res: m.ResponseHeaders,
			}
			for _, alias := range m.Aliases {
				cp.models[alias] = cp.models[m.ID]
			}
		}
		e.channels[ch.ID] = cp
	}

	return e
}

// ProcessRequest processes client->upstream request headers.
func (e *Engine) ProcessRequest(channelID, model string, clientHeaders http.Header) http.Header {
	result := cloneHeaders(clientHeaders)
	applySafetyRules(result, "request")
	rules := e.collectRequestRules(channelID, model)
	defaultAction := e.getRequestDefaultAction(channelID, model)
	applyRules(result, rules, defaultAction)
	return result
}

// ProcessResponse processes upstream->client response headers.
func (e *Engine) ProcessResponse(channelID, model string, upstreamHeaders http.Header) http.Header {
	result := cloneHeaders(upstreamHeaders)
	applySafetyRules(result, "response")
	rules := e.collectResponseRules(channelID, model)
	defaultAction := e.getResponseDefaultAction(channelID, model)
	applyRules(result, rules, defaultAction)
	return result
}

// getRequestDefaultAction returns the default action for request phase.
func (e *Engine) getRequestDefaultAction(channelID, model string) config.HeaderAction {
	if cp, ok := e.channels[channelID]; ok {
		if mp, ok := cp.models[model]; ok && mp.req != nil && mp.req.Enabled && mp.req.DefaultAction != "" {
			return mp.req.DefaultAction
		}
	}
	if cp, ok := e.channels[channelID]; ok && cp.req != nil && cp.req.Enabled && cp.req.DefaultAction != "" {
		return cp.req.DefaultAction
	}
	if e.globalReq != nil && e.globalReq.Enabled && e.globalReq.DefaultAction != "" {
		return e.globalReq.DefaultAction
	}
	return config.ActionPass
}

// getResponseDefaultAction returns the default action for response phase.
func (e *Engine) getResponseDefaultAction(channelID, model string) config.HeaderAction {
	if cp, ok := e.channels[channelID]; ok {
		if mp, ok := cp.models[model]; ok && mp.res != nil && mp.res.Enabled && mp.res.DefaultAction != "" {
			return mp.res.DefaultAction
		}
	}
	if cp, ok := e.channels[channelID]; ok && cp.res != nil && cp.res.Enabled && cp.res.DefaultAction != "" {
		return cp.res.DefaultAction
	}
	if e.globalRes != nil && e.globalRes.Enabled && e.globalRes.DefaultAction != "" {
		return e.globalRes.DefaultAction
	}
	return config.ActionPass
}

// ==================== Safety Rules ====================

var safetyDropHeaders = []string{
	"authorization",
	"cookie",
	"x-api-key",
}

var safetyDropWildcardPatterns = []string{
	"x-internal-*",
}

func applySafetyRules(headers http.Header, phase string) {
	if phase == "request" {
		for _, h := range safetyDropHeaders {
			delete(headers, h)
			delete(headers, http.CanonicalHeaderKey(h))
		}
		for key := range headers {
			lowerKey := strings.ToLower(key)
			for _, pattern := range safetyDropWildcardPatterns {
				if MatchWildcard(pattern, lowerKey) {
					delete(headers, key)
					break
				}
			}
		}
	}
}

// ==================== Rule Collection ====================

type collectedRule struct {
	config.HeaderRule
	priority int // model=3, channel=2, global=1
}

func (e *Engine) collectRequestRules(channelID, model string) []collectedRule {
	var rules []collectedRule

	// Global rules (priority 1)
	if e.globalReq != nil && e.globalReq.Enabled {
		// Support both simplified and explicit rules
		allRules := e.globalReq.ToRules(config.PhaseRequest)
		for _, r := range allRules {
			if r.Phase == config.PhaseRequest || r.Phase == config.PhaseBoth {
				rules = append(rules, collectedRule{HeaderRule: r, priority: 1})
			}
		}
	}

	// Channel rules (priority 2)
	if cp, ok := e.channels[channelID]; ok && cp.req != nil && cp.req.Enabled {
		allRules := cp.req.ToRules(config.PhaseRequest)
		for _, r := range allRules {
			if r.Phase == config.PhaseRequest || r.Phase == config.PhaseBoth {
				rules = append(rules, collectedRule{HeaderRule: r, priority: 2})
			}
		}
	}

	// Model rules (priority 3)
	if cp, ok := e.channels[channelID]; ok {
		if mp, ok := cp.models[model]; ok && mp.req != nil && mp.req.Enabled {
			allRules := mp.req.ToRules(config.PhaseRequest)
			for _, r := range allRules {
				if r.Phase == config.PhaseRequest || r.Phase == config.PhaseBoth {
					rules = append(rules, collectedRule{HeaderRule: r, priority: 3})
				}
			}
		}
	}

	sortByPriority(rules)
	return rules
}

func (e *Engine) collectResponseRules(channelID, model string) []collectedRule {
	var rules []collectedRule

	if e.globalRes != nil && e.globalRes.Enabled {
		allRules := e.globalRes.ToRules(config.PhaseResponse)
		for _, r := range allRules {
			if r.Phase == config.PhaseResponse || r.Phase == config.PhaseBoth {
				rules = append(rules, collectedRule{HeaderRule: r, priority: 1})
			}
		}
	}

	if cp, ok := e.channels[channelID]; ok && cp.res != nil && cp.res.Enabled {
		allRules := cp.res.ToRules(config.PhaseResponse)
		for _, r := range allRules {
			if r.Phase == config.PhaseResponse || r.Phase == config.PhaseBoth {
				rules = append(rules, collectedRule{HeaderRule: r, priority: 2})
			}
		}
	}

	if cp, ok := e.channels[channelID]; ok {
		if mp, ok := cp.models[model]; ok && mp.res != nil && mp.res.Enabled {
			allRules := mp.res.ToRules(config.PhaseResponse)
			for _, r := range allRules {
				if r.Phase == config.PhaseResponse || r.Phase == config.PhaseBoth {
					rules = append(rules, collectedRule{HeaderRule: r, priority: 3})
				}
			}
		}
	}

	sortByPriority(rules)
	return rules
}

func sortByPriority(rules []collectedRule) {
	for i := 1; i < len(rules); i++ {
		key := rules[i]
		j := i - 1
		for j >= 0 && rules[j].priority < key.priority {
			rules[j+1] = rules[j]
			j--
		}
		rules[j+1] = key
	}
}

// ==================== Rule Application ====================

func applyRules(headers http.Header, rules []collectedRule, defaultAction config.HeaderAction) {
	if len(rules) == 0 && defaultAction == config.ActionPass {
		return
	}

	processed := make(map[string]bool)

	// First pass: apply rules to existing headers
	for _, rule := range rules {
		for key := range headers {
			lowerKey := strings.ToLower(key)
			if processed[lowerKey] {
				continue
			}
			if Match(rule.MatchType, rule.Pattern, lowerKey) {
				applySingleRule(headers, key, rule.HeaderRule)
				processed[lowerKey] = true
			}
		}
	}

	// Second pass: for set actions, add new headers if they don't exist
	for _, rule := range rules {
		if rule.Action == config.ActionSet {
			targetKey := rule.Pattern
			lowerTarget := strings.ToLower(targetKey)
			if !processed[lowerTarget] && headers.Get(targetKey) == "" {
				headers.Set(targetKey, rule.Value)
				processed[lowerTarget] = true
			}
		}
	}

	// Apply default action to unprocessed headers
	if defaultAction == config.ActionDrop {
		for key := range headers {
			lowerKey := strings.ToLower(key)
			if !processed[lowerKey] {
				delete(headers, key)
			}
		}
	}
}

func applySingleRule(headers http.Header, key string, rule config.HeaderRule) {
	switch rule.Action {
	case config.ActionDrop:
		delete(headers, key)
	case config.ActionPass:
		// do nothing
	case config.ActionSet:
		headers.Set(key, rule.Value)
	case config.ActionRename:
		value := headers.Get(key)
		delete(headers, key)
		headers.Set(rule.Target, value)
	case config.ActionAppend:
		existing := headers.Get(key)
		if existing != "" {
			headers.Set(key, existing+", "+rule.Value)
		} else {
			headers.Set(key, rule.Value)
		}
	case config.ActionPrepend:
		existing := headers.Get(key)
		if existing != "" {
			headers.Set(key, rule.Value+", "+existing)
		} else {
			headers.Set(key, rule.Value)
		}
	case config.ActionCopy:
		value := headers.Get(key)
		headers.Set(rule.Target, value)
	}
}

// ==================== Helpers ====================

func cloneHeaders(src http.Header) http.Header {
	dst := make(http.Header, len(src))
	for k, v := range src {
		dst[k] = make([]string, len(v))
		copy(dst[k], v)
	}
	return dst
}
