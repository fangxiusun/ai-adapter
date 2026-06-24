package channel

import (
	"sync"
	"time"
)

type KeyState struct {
	RequestCount       int64
	ErrorCount         int64
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
	if ks.Paused && time.Now().Before(ks.PauseUntil) {
		return false
	}
	if ks.Paused && time.Now().After(ks.PauseUntil) {
		ks.mu.RUnlock()
		ks.ResetPause()
		ks.mu.RLock()
	}
	return true
}

func (ks *KeyState) OnSuccess() {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.RequestCount++
	ks.ConsecErrors = 0
	ks.LastSuccessTime = time.Now()
}

func (ks *KeyState) On401() {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.PermanentlySkipped = true
	ks.LastError = "401 Unauthorized - permanently skipped"
	ks.LastErrorTime = time.Now()
	ks.ErrorCount++
}

func (ks *KeyState) On429() {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.RequestCount++
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

func (ks *KeyState) OnError() {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.RequestCount++
	ks.ErrorCount++
	ks.ConsecErrors++
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
	Name               string
	RequestCount       int64
	ErrorCount         int64
	AvgLatencyMs       int64
	LastSuccessTime    time.Time
	LastErrorTime      time.Time
	LastError          string
	Paused             bool
	PauseUntil         time.Time
	PermanentlySkipped bool
	RateLimitCount     int
}
