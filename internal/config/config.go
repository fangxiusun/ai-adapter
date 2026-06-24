package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Logging  LoggingConfig  `yaml:"logging"`
	Database DatabaseConfig `yaml:"database"`
	Channels []ChannelConfig `yaml:"channels"`
}

type ServerConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	APIToken string `yaml:"api_token"`
}

type LoggingConfig struct {
	Level         string `yaml:"level"`
	File          string `yaml:"file"`
	MaxSizeMB     int    `yaml:"max_size_mb"`
	MaxBackups    int    `yaml:"max_backups"`
	LogRequestBody bool  `yaml:"log_request_body"`
	LogIO         bool   `yaml:"log_io"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type ChannelConfig struct {
	ID               string         `yaml:"id"`
	Name             string         `yaml:"name"`
	BaseURL          string         `yaml:"base_url"`
	WireAPI          string         `yaml:"wire_api"`
	Enabled          bool           `yaml:"enabled"`
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
}

type RetryConfig struct {
	// 429 重试延迟（ms），key 轮转时的等待时间
	RetryDelay429Ms int `yaml:"retry_delay_429_ms"`
	// 所有 key 都返回 429 时，从头重新轮转的最大次数
	MaxRotationRounds int `yaml:"max_rotation_rounds"`
	// 最大总等待时间（ms），超时返回最后一个响应给客户端
	MaxTotalWaitMs int `yaml:"max_total_wait_ms"`
	// 自动暂停：连续错误达到此次数时暂停 key
	ConsecErrorThreshold int `yaml:"consec_error_threshold"`
	// 429 暂停时间倍数（秒），实际暂停 = (连续错误数 - 阈值 + 1) × 此值
	PauseMultiplierSec int `yaml:"pause_multiplier_sec"`
	// 429 暂停最大时间（秒）
	PauseMaxSec int `yaml:"pause_max_sec"`
}

type ModelConfig struct {
	ID                string   `yaml:"id"`
	DisplayName       string   `yaml:"display_name"`
	ContextWindow     int      `yaml:"context_window"`
	MaxOutputTokens   int      `yaml:"max_output_tokens"`
	SupportsImages    bool     `yaml:"supports_images"`
	SupportsReasoning bool     `yaml:"supports_reasoning"`
	Aliases           []string `yaml:"aliases"`
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
	}
}

func (c *Config) validate() error {
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
		if ch.BaseURL == "" {
			return fmt.Errorf("channel %s: base_url is required", ch.ID)
		}
		if ch.WireAPI != "chat" && ch.WireAPI != "responses" {
			return fmt.Errorf("channel %s: wire_api must be 'chat' or 'responses'", ch.ID)
		}
		if len(ch.Keys) == 0 {
			return fmt.Errorf("channel %s: at least one key is required", ch.ID)
		}
		if len(ch.Models) == 0 {
			return fmt.Errorf("channel %s: at least one model is required", ch.ID)
		}
	}
	return nil
}

func (c *Config) GetTimeout() time.Duration {
	return 60 * time.Second
}
