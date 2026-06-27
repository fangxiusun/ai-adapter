package config

import (
	"fmt"
	"regexp"
	"strings"
)

// ==================== Header Policy Types ====================

// HeaderPhase defines which direction a header rule applies to.
type HeaderPhase string

const (
	PhaseRequest  HeaderPhase = "request"
	PhaseResponse HeaderPhase = "response"
	PhaseBoth     HeaderPhase = "both"
)

// HeaderAction defines what to do with a matched header.
type HeaderAction string

const (
	ActionDrop    HeaderAction = "drop"
	ActionPass    HeaderAction = "pass"
	ActionSet     HeaderAction = "set"
	ActionRename  HeaderAction = "rename"
	ActionAppend  HeaderAction = "append"
	ActionPrepend HeaderAction = "prepend"
	ActionCopy    HeaderAction = "copy"
)

// HeaderMatchType defines how header keys are matched.
type HeaderMatchType string

const (
	MatchExact    HeaderMatchType = "exact"
	MatchWildcard HeaderMatchType = "wildcard"
	MatchRegex    HeaderMatchType = "regex"
)

// HeaderRule defines a single header policy rule.
type HeaderRule struct {
	Name      string          `yaml:"name"`
	Phase     HeaderPhase     `yaml:"phase"`
	MatchType HeaderMatchType `yaml:"match_type"`
	Pattern   string          `yaml:"pattern"`
	Action    HeaderAction    `yaml:"action"`
	Value     string          `yaml:"value,omitempty"`
	Target    string          `yaml:"target,omitempty"`
}

// HeaderPolicyConfig defines a set of header rules with a default action.
// Supports both simplified and full syntax.
type HeaderPolicyConfig struct {
	Enabled       bool            `yaml:"enabled"`
	DefaultAction HeaderAction    `yaml:"default_action"`
	Rules         []HeaderRule    `yaml:"rules"`

	// Simplified syntax - maps action to patterns
	Drop    []string            `yaml:"drop,omitempty"`
	Pass    []string            `yaml:"pass,omitempty"`
	Set     map[string]string   `yaml:"set,omitempty"`
	Rename  map[string]string   `yaml:"rename,omitempty"`
	Append  map[string]string   `yaml:"append,omitempty"`
	Prepend map[string]string   `yaml:"prepend,omitempty"`
	Copy    map[string]string   `yaml:"copy,omitempty"`
}

// HeadersConfig is a simplified top-level header configuration.
type HeadersConfig struct {
	Request  *HeaderPolicyConfig `yaml:"request,omitempty"`
	Response *HeaderPolicyConfig `yaml:"response,omitempty"`
}

// DetectMatchType detects the match type from a pattern using prefix notation.
// ~ prefix -> regex
// Contains * -> wildcard
// Otherwise -> exact
func DetectMatchType(pattern string) (HeaderMatchType, string) {
	if strings.HasPrefix(pattern, "~") {
		return MatchRegex, pattern[1:]
	}
	if strings.Contains(pattern, "*") {
		return MatchWildcard, pattern
	}
	return MatchExact, pattern
}

// ToRules converts simplified syntax to full rules.
func (p *HeaderPolicyConfig) ToRules(phase HeaderPhase) []HeaderRule {
	var rules []HeaderRule

	// Process drop patterns
	for _, pattern := range p.Drop {
		matchType, cleanPattern := DetectMatchType(pattern)
		rules = append(rules, HeaderRule{
			Name:      fmt.Sprintf("drop-%s", cleanPattern),
			Phase:     phase,
			MatchType: matchType,
			Pattern:   cleanPattern,
			Action:    ActionDrop,
		})
	}

	// Process pass patterns
	for _, pattern := range p.Pass {
		matchType, cleanPattern := DetectMatchType(pattern)
		rules = append(rules, HeaderRule{
			Name:      fmt.Sprintf("pass-%s", cleanPattern),
			Phase:     phase,
			MatchType: matchType,
			Pattern:   cleanPattern,
			Action:    ActionPass,
		})
	}

	// Process set map
	for key, value := range p.Set {
		matchType, cleanPattern := DetectMatchType(key)
		rules = append(rules, HeaderRule{
			Name:      fmt.Sprintf("set-%s", cleanPattern),
			Phase:     phase,
			MatchType: matchType,
			Pattern:   cleanPattern,
			Action:    ActionSet,
			Value:     value,
		})
	}

	// Process rename map
	for key, target := range p.Rename {
		matchType, cleanPattern := DetectMatchType(key)
		rules = append(rules, HeaderRule{
			Name:      fmt.Sprintf("rename-%s", cleanPattern),
			Phase:     phase,
			MatchType: matchType,
			Pattern:   cleanPattern,
			Action:    ActionRename,
			Target:    target,
		})
	}

	// Process append map
	for key, value := range p.Append {
		matchType, cleanPattern := DetectMatchType(key)
		rules = append(rules, HeaderRule{
			Name:      fmt.Sprintf("append-%s", cleanPattern),
			Phase:     phase,
			MatchType: matchType,
			Pattern:   cleanPattern,
			Action:    ActionAppend,
			Value:     value,
		})
	}

	// Process prepend map
	for key, value := range p.Prepend {
		matchType, cleanPattern := DetectMatchType(key)
		rules = append(rules, HeaderRule{
			Name:      fmt.Sprintf("prepend-%s", cleanPattern),
			Phase:     phase,
			MatchType: matchType,
			Pattern:   cleanPattern,
			Action:    ActionPrepend,
			Value:     value,
		})
	}

	// Process copy map
	for key, target := range p.Copy {
		matchType, cleanPattern := DetectMatchType(key)
		rules = append(rules, HeaderRule{
			Name:      fmt.Sprintf("copy-%s", cleanPattern),
			Phase:     phase,
			MatchType: matchType,
			Pattern:   cleanPattern,
			Action:    ActionCopy,
			Target:    target,
		})
	}

	// Append explicit rules
	rules = append(rules, p.Rules...)

	return rules
}

// ==================== Validation ====================

// validateHeaderPolicy validates a single HeaderPolicyConfig.
func validateHeaderPolicy(p *HeaderPolicyConfig, context string) error {
	if p == nil || !p.Enabled {
		return nil
	}

	// Validate default_action
	if p.DefaultAction != "" {
		switch p.DefaultAction {
		case ActionPass, ActionDrop:
			// valid
		default:
			return fmt.Errorf("%s: default_action must be 'pass' or 'drop', got: %s", context, p.DefaultAction)
		}
	}

	// Validate explicit rules
	for i, rule := range p.Rules {
		ruleCtx := fmt.Sprintf("%s rule[%d] %q", context, i, rule.Name)

		// Validate phase
		switch rule.Phase {
		case PhaseRequest, PhaseResponse, PhaseBoth:
			// valid
		default:
			return fmt.Errorf("%s: phase must be request/response/both, got: %s", ruleCtx, rule.Phase)
		}

		// Validate match_type
		switch rule.MatchType {
		case MatchExact, MatchWildcard, MatchRegex:
			// valid
		default:
			return fmt.Errorf("%s: match_type must be exact/wildcard/regex, got: %s", ruleCtx, rule.MatchType)
		}

		// Validate pattern
		if rule.Pattern == "" {
			return fmt.Errorf("%s: pattern is required", ruleCtx)
		}

		// Validate regex pattern compiles
		if rule.MatchType == MatchRegex {
			if _, err := regexp.Compile(rule.Pattern); err != nil {
				return fmt.Errorf("%s: invalid regex pattern %q: %w", ruleCtx, rule.Pattern, err)
			}
		}

		// Validate action
		switch rule.Action {
		case ActionDrop, ActionPass, ActionSet, ActionRename, ActionAppend, ActionPrepend, ActionCopy:
			// valid
		default:
			return fmt.Errorf("%s: action must be drop/pass/set/rename/append/prepend/copy, got: %s", ruleCtx, rule.Action)
		}

		// Validate action-specific fields
		switch rule.Action {
		case ActionSet, ActionAppend, ActionPrepend:
			if rule.Value == "" {
				return fmt.Errorf("%s: value is required for action %s", ruleCtx, rule.Action)
			}
		case ActionRename, ActionCopy:
			if rule.Target == "" {
				return fmt.Errorf("%s: target is required for action %s", ruleCtx, rule.Action)
			}
		}
	}

	// Validate simplified syntax patterns
	validateSimplePatterns := func(patterns []string, action string) error {
		for _, pattern := range patterns {
			matchType, cleanPattern := DetectMatchType(pattern)
			if cleanPattern == "" {
				return fmt.Errorf("%s %s: empty pattern", context, action)
			}
			if matchType == MatchRegex {
				if _, err := regexp.Compile(cleanPattern); err != nil {
					return fmt.Errorf("%s %s: invalid regex pattern %q: %w", context, action, cleanPattern, err)
				}
			}
		}
		return nil
	}

	if err := validateSimplePatterns(p.Drop, "drop"); err != nil {
		return err
	}
	if err := validateSimplePatterns(p.Pass, "pass"); err != nil {
		return err
	}

	// Validate set/rename/append/prepend/copy maps
	validateMapPatterns := func(m map[string]string, action string, needValue bool) error {
		for key, value := range m {
			matchType, cleanPattern := DetectMatchType(key)
			if cleanPattern == "" {
				return fmt.Errorf("%s %s: empty pattern", context, action)
			}
			if matchType == MatchRegex {
				if _, err := regexp.Compile(cleanPattern); err != nil {
					return fmt.Errorf("%s %s: invalid regex pattern %q: %w", context, action, cleanPattern, err)
				}
			}
			if needValue && value == "" {
				return fmt.Errorf("%s %s: value/target is required for %q", context, action, key)
			}
		}
		return nil
	}

	if err := validateMapPatterns(p.Set, "set", true); err != nil {
		return err
	}
	if err := validateMapPatterns(p.Rename, "rename", true); err != nil {
		return err
	}
	if err := validateMapPatterns(p.Append, "append", true); err != nil {
		return err
	}
	if err := validateMapPatterns(p.Prepend, "prepend", true); err != nil {
		return err
	}
	if err := validateMapPatterns(p.Copy, "copy", true); err != nil {
		return err
	}

	return nil
}

// validateAllHeaderPolicies validates all header policies in the config.
func (c *Config) validateAllHeaderPolicies() error {
	// Validate global headers (old format)
	if err := validateHeaderPolicy(c.GlobalRequestHeaders, "global_request_headers"); err != nil {
		return err
	}
	if err := validateHeaderPolicy(c.GlobalResponseHeaders, "global_response_headers"); err != nil {
		return err
	}

	// Validate global headers (new simplified format)
	if c.Headers != nil {
		if err := validateHeaderPolicy(c.Headers.Request, "headers.request"); err != nil {
			return err
		}
		if err := validateHeaderPolicy(c.Headers.Response, "headers.response"); err != nil {
			return err
		}
	}

	for _, ch := range c.Channels {
		chCtx := fmt.Sprintf("channel %s", ch.ID)
		if err := validateHeaderPolicy(ch.RequestHeaders, chCtx+".request_headers"); err != nil {
			return err
		}
		if err := validateHeaderPolicy(ch.ResponseHeaders, chCtx+".response_headers"); err != nil {
			return err
		}
		for _, m := range ch.Models {
			modelCtx := fmt.Sprintf("%s model %s", chCtx, m.ID)
			if err := validateHeaderPolicy(m.RequestHeaders, modelCtx+".request_headers"); err != nil {
				return err
			}
			if err := validateHeaderPolicy(m.ResponseHeaders, modelCtx+".response_headers"); err != nil {
				return err
			}
		}
	}
	return nil
}
