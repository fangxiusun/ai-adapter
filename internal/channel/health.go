package channel

import (
	"sync"
	"time"
)

// ChannelHealth tracks the health state of a channel for failover decisions.
type ChannelHealth struct {
	mu               sync.RWMutex
	healthy          bool
	consecFailures   int
	recoveryAt       time.Time
	recoveryCooldown time.Duration
	threshold        int
}

// NewChannelHealth creates a new health tracker with the given threshold and cooldown.
func NewChannelHealth(threshold int, cooldownSec int) *ChannelHealth {
	return &ChannelHealth{
		healthy:          true,
		threshold:        threshold,
		recoveryCooldown: time.Duration(cooldownSec) * time.Second,
	}
}

// IsHealthy returns whether the channel is currently available.
// If in recovery mode, allows one probe request through.
func (h *ChannelHealth) IsHealthy() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.healthy {
		return true
	}
	// Check if recovery cooldown has elapsed — allow probe
	if time.Now().After(h.recoveryAt) {
		return true
	}
	return false
}

// ReportSuccess reports a successful request, resetting the health state.
func (h *ChannelHealth) ReportSuccess() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.healthy = true
	h.consecFailures = 0
}

// ReportFailure reports a failed request (5xx or connection error).
// If consecutive failures reach the threshold, the channel is marked unhealthy.
func (h *ChannelHealth) ReportFailure() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.consecFailures++
	if h.consecFailures >= h.threshold {
		h.healthy = false
		h.recoveryAt = time.Now().Add(h.recoveryCooldown)
	}
}

// Reset resets the health state to healthy.
func (h *ChannelHealth) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.healthy = true
	h.consecFailures = 0
}
