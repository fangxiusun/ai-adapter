package channel

import (
	"sync"
	"time"
)

type KeyState struct {
	RequestCount       int64
	ErrorCount         int64 // sum of all errors, kept for backward compat
	Error400           int64
	Error401           int64
	Error403           int64
	Error404           int64
	Error429           int64
	Error4xx           int64 // other 4xx
	Error5xx           int64
	ErrorNetwork       int64
	ErrorStream        int64
	ConsecErrors       int
	TotalLatencyMs     int64
	LastError          string
	LastErrorTime      time.Time
	LastSuccessTime    time.Time
	Paused             bool
	PauseUntil         time.Time
	PermanentlySkipped bool
	RateLimitCount     int
	RateLimitWindow    []time.Time
	consecThreshold    int
	pauseMultiplierSec int
	pauseMaxSec        int
	mu                 sync.RWMutex
}

func NewKeyState(consecThreshold, pauseMultiplierSec, pauseMaxSec int) *KeyState {
	if consecThreshold <= 0 {
		consecThreshold = 3
	}
	if pauseMultiplierSec <= 0 {
		pauseMultiplierSec = 30
	}
	if pauseMaxSec <= 0 {
		pauseMaxSec = 600
	}
	return &KeyState{
		consecThreshold:    consecThreshold,
		pauseMultiplierSec: pauseMultiplierSec,
		pauseMaxSec:        pauseMaxSec,
	}
}

func (ks *KeyState) IsAvailable() bool {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	if ks.PermanentlySkipped {
		return false
	}
	if ks.Paused {
		if time.Now().Before(ks.PauseUntil) {
			return false
		}
		// Pause expired -- caller should invoke ResetPause() after releasing read lock.
		return true
	}
	return true
}

// IsPauseExpired returns true if the key is paused but the pause window has elapsed.
// The caller should invoke ResetPause() to clear the pause state.
func (ks *KeyState) IsPauseExpired() bool {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return ks.Paused && time.Now().After(ks.PauseUntil)
}

func (ks *KeyState) OnSuccess() {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.RequestCount++
	ks.ConsecErrors = 0
	ks.LastSuccessTime = time.Now()
}

func (ks *KeyState) On400() {
	ks.recordError(&ks.Error400, "400 Bad Request")
}

func (ks *KeyState) On401() {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.PermanentlySkipped = true
	ks.Error401++
	ks.ErrorCount++
	ks.LastError = "401 Unauthorized - permanently skipped"
	ks.LastErrorTime = time.Now()
}

func (ks *KeyState) On403() {
	ks.recordError(&ks.Error403, "403 Forbidden")
}

func (ks *KeyState) On404() {
	ks.recordError(&ks.Error404, "404 Not Found")
}

func (ks *KeyState) On429() {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.RequestCount++
	ks.Error429++
	ks.ErrorCount++
	ks.RateLimitCount++
	ks.LastError = "429 Rate Limited"
	ks.LastErrorTime = time.Now()

	now := time.Now()
	ks.RateLimitWindow = append(ks.RateLimitWindow, now)
	cutoff := now.Add(-5 * time.Minute)
	valid := ks.RateLimitWindow[:0]
	for _, t := range ks.RateLimitWindow {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	ks.RateLimitWindow = valid
	ks.RateLimitCount = len(valid)

	if ks.ConsecErrors >= ks.consecThreshold {
		pauseSec := (ks.ConsecErrors - ks.consecThreshold + 1) * ks.pauseMultiplierSec
		pauseDuration := time.Duration(pauseSec) * time.Second
		maxDuration := time.Duration(ks.pauseMaxSec) * time.Second
		if pauseDuration > maxDuration {
			pauseDuration = maxDuration
		}
		ks.Paused = true
		ks.PauseUntil = now.Add(pauseDuration)
	}
	ks.ConsecErrors++
}

func (ks *KeyState) OnError4xx() {
	ks.recordError(&ks.Error4xx, "4xx Client Error")
}

func (ks *KeyState) OnError5xx(statusCode int) {
	ks.recordError(&ks.Error5xx, "5xx Server Error")
}

func (ks *KeyState) OnErrorNetwork() {
	ks.recordError(&ks.ErrorNetwork, "Network Error")
}

func (ks *KeyState) OnErrorStream() {
	ks.recordError(&ks.ErrorStream, "Stream Parse Error")
}

// recordError is the common error recording logic.
func (ks *KeyState) recordError(counter *int64, msg string) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.RequestCount++
	*counter++
	ks.ErrorCount++
	ks.ConsecErrors++
	ks.LastError = msg
	ks.LastErrorTime = time.Now()

	if ks.ConsecErrors >= ks.consecThreshold {
		pauseSec := (ks.ConsecErrors - ks.consecThreshold + 1) * ks.pauseMultiplierSec
		pauseDuration := time.Duration(pauseSec) * time.Second
		maxDuration := time.Duration(ks.pauseMaxSec) * time.Second
		if pauseDuration > maxDuration {
			pauseDuration = maxDuration
		}
		ks.Paused = true
		ks.PauseUntil = time.Now().Add(pauseDuration)
	}
}

func (ks *KeyState) ResetPause() {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.Paused = false
	ks.PauseUntil = time.Time{}
	ks.ConsecErrors = 0
}

func (ks *KeyState) RecordLatency(ms int64) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.TotalLatencyMs += ms
}

func (ks *KeyState) AvgLatencyMs() int64 {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	if ks.RequestCount == 0 {
		return 0
	}
	return ks.TotalLatencyMs / ks.RequestCount
}

func (ks *KeyState) RateLimitScore() int {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return ks.RateLimitCount
}

type KeyStats struct {
	Name               string    `json:"name"`
	Value              string    `json:"value"`
	RequestCount       int64     `json:"request_count"`
	ErrorCount         int64     `json:"error_count"`
	Error400           int64     `json:"error_400"`
	Error401           int64     `json:"error_401"`
	Error403           int64     `json:"error_403"`
	Error404           int64     `json:"error_404"`
	Error429           int64     `json:"error_429"`
	Error4xx           int64     `json:"error_4xx"`
	Error5xx           int64     `json:"error_5xx"`
	ErrorNetwork       int64     `json:"error_network"`
	ErrorStream        int64     `json:"error_stream"`
	AvgLatencyMs       int64     `json:"avg_latency_ms"`
	LastSuccessTime    time.Time `json:"last_success_time"`
	LastErrorTime      time.Time `json:"last_error_time"`
	LastError          string    `json:"last_error"`
	Paused             bool      `json:"paused"`
	PauseUntil         time.Time `json:"pause_until"`
	PermanentlySkipped bool      `json:"permanently_skipped"`
	RateLimitCount     int       `json:"rate_limit_count"`
}
