package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig    `yaml:"server"`
	Logging  LoggingConfig   `yaml:"logging"`
	Database DatabaseConfig  `yaml:"database"`
	Proxies  []ProxyConfig   `yaml:"proxies"`
	Failover  FailoverConfig  `yaml:"failover"`
	Channels []ChannelConfig `yaml:"channels"`


	// New simplified format
	Headers *HeadersConfig `yaml:"headers,omitempty"`

	// Legacy format (still supported)
	GlobalRequestHeaders  *HeaderPolicyConfig `yaml:"global_request_headers,omitempty"`
	GlobalResponseHeaders *HeaderPolicyConfig `yaml:"global_response_headers,omitempty"`
}

type ServerConfig struct {
	Host                 string `yaml:"host" json:"host"`
	Port                 int    `yaml:"port" json:"port"`
	APIToken             string `yaml:"api_token" json:"api_token"`
	AdminToken           string `yaml:"admin_token" json:"admin_token"`
	MaxRequestBodySizeMB int    `yaml:"max_request_body_size_mb" json:"max_request_body_size_mb"`
}

type LoggingConfig struct {
	Level          string `yaml:"level" json:"level"`
	File           string `yaml:"file" json:"file"`
	MaxSizeMB      int    `yaml:"max_size_mb" json:"max_size_mb"`
	MaxBackups     int    `yaml:"max_backups" json:"max_backups"`
	LogRequestBody bool   `yaml:"log_request_body" json:"log_request_body"`
	LogIO          bool   `yaml:"log_io" json:"log_io"`
	MaxAgeDays     int    `yaml:"max_age_days" json:"max_age_days"`
	Compress       bool   `yaml:"compress" json:"compress"`
}

type DatabaseConfig struct {
	Path string `yaml:"path" json:"path"`
}

// ProxyConfig defines a named proxy that channels can reference by proxy_id.
type ProxyConfig struct {
	ID   string `yaml:"id"`
	Type string `yaml:"type"` // "http" or "socks5"
	URL  string `yaml:"url"`
}

// ChannelConfig defines a channel with interface capability URLs.
type ChannelConfig struct {
	ID               string         `yaml:"id"`
	Name             string         `yaml:"name"`
	Enabled          bool           `yaml:"enabled"`
	ProxyID          string         `yaml:"proxy_id"`
	Priority          int            `yaml:"priority"`
	Models           []ModelConfig  `yaml:"models"`
	DefaultModel     string         `yaml:"default_model"`
	Keys             []KeyConfig    `yaml:"keys"`
	KeyStrategy      string         `yaml:"key_strategy"`
	MaxRetries       int            `yaml:"max_retries"`
	RetryDelayMs     int            `yaml:"retry_delay_ms"`
	RequestTimeoutMs int            `yaml:"request_timeout_ms"`
	Fanout           FanoutConfig   `yaml:"fanout"`
	Thinking         ThinkingConfig `yaml:"thinking"`
	WebSearch        WebSearchConfig `yaml:"web_search"`
	Retry            RetryConfig    `yaml:"retry"`
	KeyStatsSyncSec  int            `yaml:"key_stats_sync_sec"`

	ChatURL            string `yaml:"chat_url"`
	ResponsesURL       string `yaml:"responses_url"`
	MessagesURL        string `yaml:"messages_url"`
	GenerateContentURL string `yaml:"generate_content_url"`

	// Header policy configuration (optional)
	RequestHeaders  *HeaderPolicyConfig `yaml:"request_headers,omitempty"`
	ResponseHeaders *HeaderPolicyConfig `yaml:"response_headers,omitempty"`
}

type RetryConfig struct {
	RetryDelay429Ms      int `yaml:"retry_delay_429_ms"`
	MaxRotationRounds    int `yaml:"max_rotation_rounds"`
	MaxTotalWaitMs       int `yaml:"max_total_wait_ms"`
	ConsecErrorThreshold int `yaml:"consec_error_threshold"`
	PauseMultiplierSec   int `yaml:"pause_multiplier_sec"`
	PauseMaxSec          int `yaml:"pause_max_sec"`
}

// FailoverConfig controls cross-channel failover behavior.
type FailoverConfig struct {
	Enabled                  bool `yaml:"enabled"`
	MaxChannelAttempts       int  `yaml:"max_channel_attempts"`
	TotalTimeoutMs           int  `yaml:"total_timeout_ms"`
	ConsecutiveFailThreshold int    `yaml:"consecutive_fail_threshold"`
	LoadBalance              string `yaml:"load_balance"`
}


type ModelConfig struct {
	ID                string   `yaml:"id"`
	DisplayName       string   `yaml:"display_name"`
	ContextWindow     int      `yaml:"context_window"`
	MaxOutputTokens   int      `yaml:"max_output_tokens"`
	SupportsImages    bool     `yaml:"supports_images"`
	SupportsReasoning bool     `yaml:"supports_reasoning"`
	Aliases           []string `yaml:"aliases"`

	// Header policy configuration (optional)
	RequestHeaders  *HeaderPolicyConfig `yaml:"request_headers,omitempty"`
	ResponseHeaders *HeaderPolicyConfig `yaml:"response_headers,omitempty"`
}

type KeyConfig struct {
	Value string `yaml:"value"`
	Name  string `yaml:"name"`
}

type FanoutConfig struct {
	Enabled bool `yaml:"enabled"`
	Count   int  `yaml:"count"`
	WaitAll bool `yaml:"wait_all"`
}

type ThinkingConfig struct {
	DefaultEnabled  bool `yaml:"default_enabled"`
	ForceHighEffort bool `yaml:"force_high_effort"`
}

type WebSearchConfig struct {
	Enabled bool `yaml:"enabled"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

func Parse(data []byte) (*Config, error) {
	if err := checkForbiddenKeys(data); err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// checkForbiddenKeys detects deprecated config fields and returns a clear error.
func checkForbiddenKeys(data []byte) error {
	var raw struct {
		Channels []struct {
			ID      string  `yaml:"id"`
			WireAPI *string `yaml:"wire_api"`
		} `yaml:"channels"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	for _, ch := range raw.Channels {
		if ch.WireAPI != nil {
			return fmt.Errorf("channel %s: wire_api has been removed, use interface capability URLs instead (chat_url, responses_url, messages_url, generate_content_url)", ch.ID)
		}
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8080
	}
	if c.Database.Path == "" {
		c.Database.Path = "./data/proxy.db"
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.MaxSizeMB == 0 {
		c.Logging.MaxSizeMB = 100
	}
	if c.Logging.MaxBackups == 0 {
		c.Logging.MaxBackups = 3
	}
	if c.Logging.MaxAgeDays == 0 {
		 c.Logging.MaxAgeDays = 30
	}
	if c.Server.MaxRequestBodySizeMB == 0 {
		c.Server.MaxRequestBodySizeMB = 64
	}
	for i := range c.Channels {
		ch := &c.Channels[i]
		if ch.KeyStrategy == "" {
			ch.KeyStrategy = "round-robin"
		}
		if ch.MaxRetries == 0 {
			ch.MaxRetries = 2
		}
		if ch.RetryDelayMs == 0 {
			ch.RetryDelayMs = 500
		}
		if ch.RequestTimeoutMs == 0 {
			ch.RequestTimeoutMs = 60000
		}
		if ch.Fanout.Count == 0 {
			ch.Fanout.Count = 2
		}
		if ch.Retry.RetryDelay429Ms == 0 {
			ch.Retry.RetryDelay429Ms = 500
		}
		if ch.Retry.MaxRotationRounds == 0 {
			ch.Retry.MaxRotationRounds = 3
		}
		if ch.Retry.MaxTotalWaitMs == 0 {
			ch.Retry.MaxTotalWaitMs = 30000
		}
		if ch.Retry.ConsecErrorThreshold == 0 {
			ch.Retry.ConsecErrorThreshold = 3
		}
		if ch.Retry.PauseMultiplierSec == 0 {
			ch.Retry.PauseMultiplierSec = 30
		}
		if ch.Retry.PauseMaxSec == 0 {
			ch.Retry.PauseMaxSec = 600
		}
		if ch.KeyStatsSyncSec == 0 {
			ch.KeyStatsSyncSec = 60
		}
		if ch.Priority == 0 {
			ch.Priority = 100
		}
	}
	if c.Failover.MaxChannelAttempts == 0 {
		c.Failover.MaxChannelAttempts = 3
	}
	if c.Failover.TotalTimeoutMs == 0 {
		c.Failover.TotalTimeoutMs = 120000
	}
	if c.Failover.ConsecutiveFailThreshold == 0 {
		c.Failover.ConsecutiveFailThreshold = 2
	}
	if c.Failover.LoadBalance == "" {
		c.Failover.LoadBalance = "priority"
	}
}

func (c *Config) validate() error {
	// Validate header policies first
	if err := c.validateAllHeaderPolicies(); err != nil {
		return err
	}

	// Validate proxies
	proxyIDs := make(map[string]bool)
	for _, p := range c.Proxies {
		if p.ID == "" {
			return fmt.Errorf("proxy id is required")
		}
		if proxyIDs[p.ID] {
			return fmt.Errorf("duplicate proxy id: %s", p.ID)
		}
		proxyIDs[p.ID] = true
		if p.Type != "http" && p.Type != "socks5" {
			return fmt.Errorf("proxy %s: type must be http or socks5, got: %s", p.ID, p.Type)
		}
		if p.URL == "" {
			return fmt.Errorf("proxy %s: url is required", p.ID)
		}
	}

	if len(c.Channels) == 0 {
		return fmt.Errorf("at least one channel is required")
	}
	ids := make(map[string]bool)
	for _, ch := range c.Channels {
		if ch.ID == "" {
			return fmt.Errorf("channel id is required")
		}
		if ids[ch.ID] {
			return fmt.Errorf("duplicate channel id: %s", ch.ID)
		}
		ids[ch.ID] = true
		if ch.ProxyID != "" && !proxyIDs[ch.ProxyID] {
			return fmt.Errorf("channel %s: proxy_id %q not found in proxies", ch.ID, ch.ProxyID)
		}
		if len(ch.Keys) == 0 {
			return fmt.Errorf("channel %s: at least one key is required", ch.ID)
		}
		if len(ch.Models) == 0 {
			return fmt.Errorf("channel %s: at least one model is required", ch.ID)
		}
		if !ch.HasAnyCapability() {
			return fmt.Errorf("channel %s: at least one interface capability URL is required (chat_url, responses_url, messages_url, generate_content_url)", ch.ID)
		}
		keyValues := make(map[string]bool)
		for _, k := range ch.Keys {
			if k.Value == "" {
				return fmt.Errorf("channel %s: key value is required", ch.ID)
			}
			if keyValues[k.Value] {
				return fmt.Errorf("channel %s: duplicate key value: %s", ch.ID, maskKeyValue(k.Value))
			}
			keyValues[k.Value] = true
		}
	}
	return nil
}

func maskKeyValue(s string) string {
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "***" + s[len(s)-4:]
}

func (c *Config) GetTimeout() time.Duration {
	return 60 * time.Second
}

// GetProxy returns the proxy config with the given id, or nil if not found.
func (c *Config) GetProxy(id string) *ProxyConfig {
	for i := range c.Proxies {
		if c.Proxies[i].ID == id {
			return &c.Proxies[i]
		}
	}
	return nil
}

// HasAnyCapability returns true if the channel has at least one interface URL configured.
func (ch *ChannelConfig) HasAnyCapability() bool {
	return ch.ChatURL != "" || ch.ResponsesURL != "" || ch.MessagesURL != "" || ch.GenerateContentURL != ""
}

// HasNative returns true if the channel natively supports the given interface.
func (ch *ChannelConfig) HasNative(iface InterfaceType) bool {
	switch iface {
	case InterfaceChat:
		return ch.ChatURL != ""
	case InterfaceResponses:
		return ch.ResponsesURL != ""
	case InterfaceMessages:
		return ch.MessagesURL != ""
	case InterfaceGenerateContent:
		return ch.GenerateContentURL != ""
	}
	return false
}

// NativeBaseURL returns the base URL for the given interface, or empty string.
func (ch *ChannelConfig) NativeBaseURL(iface InterfaceType) string {
	switch iface {
	case InterfaceChat:
		return ch.ChatURL
	case InterfaceResponses:
		return ch.ResponsesURL
	case InterfaceMessages:
		return ch.MessagesURL
	case InterfaceGenerateContent:
		return ch.GenerateContentURL
	}
	return ""
}


