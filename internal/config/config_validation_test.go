package config

import (
	"strings"
	"testing"
)

func TestValidateRejectsNegativeRuntimeValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name: "fanout count",
			mutate: func(cfg *Config) {
				cfg.Channels[0].Fanout.Count = -1
			},
			want: "fanout count",
		},
		{
			name: "request timeout",
			mutate: func(cfg *Config) {
				cfg.Channels[0].RequestTimeoutMs = -1
			},
			want: "must not be negative",
		},
		{
			name: "rotation rounds",
			mutate: func(cfg *Config) {
				cfg.Channels[0].Retry.MaxRotationRounds = -1
			},
			want: "retry policy",
		},
		{
			name: "failover timeout",
			mutate: func(cfg *Config) {
				cfg.Failover.TotalTimeoutMs = -1
			},
			want: "failover",
		},
		{
			name: "request body size",
			mutate: func(cfg *Config) {
				cfg.Server.MaxRequestBodySizeMB = -1
			},
			want: "max_request_body_size_mb",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validRuntimeConfig()
			tt.mutate(cfg)
			err := cfg.validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validate() error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestCheckForbiddenRetryFields(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{name: "channel max retries", yaml: "channels:\n  - id: c1\n    max_retries: 2\n", want: "max_retries has been removed"},
		{name: "channel retry delay", yaml: "channels:\n  - id: c1\n    retry_delay_ms: 500\n", want: "retry_delay_ms has been removed"},
		{name: "failover attempt cap", yaml: "failover:\n  max_channel_attempts: 3\n", want: "max_channel_attempts has been removed"},
		{name: "failover consecutive threshold", yaml: "failover:\n  consecutive_fail_threshold: 2\n", want: "consecutive_fail_threshold has been removed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkForbiddenKeys([]byte(tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("checkForbiddenKeys() error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func validRuntimeConfig() *Config {
	return &Config{
		Server: ServerConfig{MaxRequestBodySizeMB: 1},
		Failover: FailoverConfig{
			TotalTimeoutMs: 1000,
		},
		Channels: []ChannelConfig{{
			ID:               "channel-1",
			Enabled:          true,
			Models:           []ModelConfig{{ID: "model-1"}},
			Keys:             []KeyConfig{{Value: "key-1"}},
			ChatURL:          "http://127.0.0.1",
			RequestTimeoutMs: 1000,
			Fanout:           FanoutConfig{Count: 1},
			Retry: RetryConfig{
				RetryDelay429Ms:      1,
				MaxRotationRounds:    1,
				MaxTotalWaitMs:       1000,
				ConsecErrorThreshold: 1,
				PauseMultiplierSec:   1,
				PauseMaxSec:          1,
			},
			KeyStatsSyncSec: 1,
		}},
	}
}
