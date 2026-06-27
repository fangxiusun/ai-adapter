package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// RequestsTotal counts total requests by channel, model, api, and status code.
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ai_adapter",
		Subsystem: "http",
		Name:      "requests_total",
		Help:      "Total number of requests processed",
	}, []string{"channel", "model", "api", "status"})

	// RequestDurationSeconds tracks request latency by channel and model.
	RequestDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "ai_adapter",
		Subsystem: "http",
		Name:      "request_duration_seconds",
		Help:      "Request latency in seconds",
		Buckets:   []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
	}, []string{"channel", "model", "api"})

	// ActiveRequests tracks currently active requests.
	ActiveRequests = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "ai_adapter",
		Subsystem: "http",
		Name:      "active_requests",
		Help:      "Number of requests currently being processed",
	})

	// PromptTokensTotal counts total prompt tokens by channel and model.
	PromptTokensTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ai_adapter",
		Subsystem: "tokens",
		Name:      "prompt_total",
		Help:      "Total prompt tokens processed",
	}, []string{"channel", "model"})

	// CompletionTokensTotal counts total completion tokens by channel and model.
	CompletionTokensTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ai_adapter",
		Subsystem: "tokens",
		Name:      "completion_total",
		Help:      "Total completion tokens processed",
	}, []string{"channel", "model"})

	// TotalTokensTotal counts total tokens (prompt + completion) by channel and model.
	TotalTokensTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ai_adapter",
		Subsystem: "tokens",
		Name:      "total_total",
		Help:      "Total tokens processed (prompt + completion)",
	}, []string{"channel", "model"})

	// ErrorsTotal counts errors by channel, model, and error code.
	ErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ai_adapter",
		Subsystem: "http",
		Name:      "errors_total",
		Help:      "Total number of errors",
	}, []string{"channel", "model", "error_code"})

	// KeyUsageTotal counts key usage by channel and key name.
	KeyUsageTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ai_adapter",
		Subsystem: "keys",
		Name:      "usage_total",
		Help:      "Total key usage count",
	}, []string{"channel", "key"})

	// KeyErrorsTotal counts key errors by channel, key name, and error type.
	KeyErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ai_adapter",
		Subsystem: "keys",
		Name:      "errors_total",
		Help:      "Total key errors by type",
	}, []string{"channel", "key", "error_type"})

	// UpstreamLatencySeconds tracks upstream response latency.
	UpstreamLatencySeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "ai_adapter",
		Subsystem: "upstream",
		Name:      "latency_seconds",
		Help:      "Upstream response latency in seconds",
		Buckets:   []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
	}, []string{"channel", "model"})

	// KeyRateLimited counts rate-limited (429) responses by channel and key.
	KeyRateLimited = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ai_adapter",
		Subsystem: "keys",
		Name:      "rate_limited_total",
		Help:      "Total rate-limited responses per key",
	}, []string{"channel", "key"})
)
