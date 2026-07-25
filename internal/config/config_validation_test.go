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

func validRuntimeConfig() *Config {
	return &Config{
		Server: ServerConfig{MaxRequestBodySizeMB: 1},
		Failover: FailoverConfig{
			MaxChannelAttempts:       1,
			TotalTimeoutMs:           1000,
			ConsecutiveFailThreshold: 1,
		},
		Channels: []ChannelConfig{{
			ID:               "channel-1",
			Enabled:          true,
			Models:           []ModelConfig{{ID: "model-1"}},
			Keys:             []KeyConfig{{Value: "key-1"}},
			ChatURL:          "http://127.0.0.1",
			MaxRetries:       1,
			RetryDelayMs:     1,
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
