package channel

import (
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/log"
)

type KeyEntry struct {
	Value string
	Name  string
	State *KeyState
}

type KeyPool struct {
	keys       []*KeyEntry
	strategy   string
	channelID  string
	counter    uint64
	logger     *log.Logger
	mu         sync.RWMutex
}

func NewKeyPool(keyCfgs []config.KeyConfig, strategy, channelID string, logger *log.Logger, consecThreshold, pauseMultiplierSec, pauseMaxSec int) *KeyPool {
	keys := make([]*KeyEntry, len(keyCfgs))
	for i, kc := range keyCfgs {
		name := kc.Name
		if name == "" {
			name = kc.Value[:min(8, len(kc.Value))] + "***"
		}
		keys[i] = &KeyEntry{
			Value: kc.Value,
			Name:  name,
			State: NewKeyState(consecThreshold, pauseMultiplierSec, pauseMaxSec),
		}
	}
	return &KeyPool{
		keys:      keys,
		strategy:  strategy,
		channelID: channelID,
		logger:    logger,
	}
}

func (kp *KeyPool) Next() *KeyEntry {
	kp.mu.RLock()
	defer kp.mu.RUnlock()

	var available []*KeyEntry
	for _, k := range kp.keys {
		if k.State.IsAvailable() {
			available = append(available, k)
		}
	}

	if len(available) == 0 {
		allSkipped := true
		for _, k := range kp.keys {
			if !k.State.PermanentlySkipped {
				allSkipped = false
				break
			}
		}
		if allSkipped {
			return nil
		}
		kp.mu.RUnlock()
		kp.mu.Lock()
		for _, k := range kp.keys {
			if !k.State.PermanentlySkipped {
				k.State.ResetPause()
			}
		}
		kp.mu.Unlock()
		kp.mu.RLock()
		for _, k := range kp.keys {
			if k.State.IsAvailable() {
				available = append(available, k)
			}
		}
		if len(available) == 0 {
			return nil
		}
		kp.mu.RUnlock()
		kp.mu.RLock()
	}

	switch kp.strategy {
	case "random":
		return available[rand.Intn(len(available))]
	case "least-errors":
		best := available[0]
		for _, k := range available[1:] {
			if k.State.ErrorCount < best.State.ErrorCount {
				best = k
			}
		}
		return best
	case "least-latency":
		best := available[0]
		for _, k := range available[1:] {
			if k.State.AvgLatencyMs() < best.State.AvgLatencyMs() {
				best = k
			}
		}
		return best
	case "least-rate-limited":
		return kp.leastRateLimited(available)
	default:
		idx := atomic.AddUint64(&kp.counter, 1)
		return available[idx%uint64(len(available))]
	}
}

func (kp *KeyPool) NextExcluding(exclude map[string]bool) *KeyEntry {
	kp.mu.RLock()
	defer kp.mu.RUnlock()

	var available []*KeyEntry
	for _, k := range kp.keys {
		if k.State.IsAvailable() && !exclude[k.Value] {
			available = append(available, k)
		}
	}

	if len(available) == 0 {
		return nil
	}

	switch kp.strategy {
	case "least-rate-limited":
		return kp.leastRateLimited(available)
	case "least-errors":
		best := available[0]
		for _, k := range available[1:] {
			if k.State.ErrorCount < best.State.ErrorCount {
				best = k
			}
		}
		return best
	default:
		return available[rand.Intn(len(available))]
	}
}

func (kp *KeyPool) leastRateLimited(available []*KeyEntry) *KeyEntry {
	if len(available) == 0 {
		return nil
	}
	sorted := make([]*KeyEntry, len(available))
	copy(sorted, available)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].State.RateLimitScore() < sorted[j].State.RateLimitScore()
	})
	return sorted[0]
}

func (kp *KeyPool) GetN(n int) []*KeyEntry {
	kp.mu.RLock()
	defer kp.mu.RUnlock()

	var available []*KeyEntry
	for _, k := range kp.keys {
		if k.State.IsAvailable() {
			available = append(available, k)
		}
	}

	if len(available) <= n {
		return available
	}

	switch kp.strategy {
	case "random":
		rand.Shuffle(len(available), func(i, j int) {
			available[i], available[j] = available[j], available[i]
		})
	case "least-rate-limited":
		sort.Slice(available, func(i, j int) bool {
			return available[i].State.RateLimitScore() < available[j].State.RateLimitScore()
		})
	default:
		for i := 0; i < len(available); i++ {
			for j := i + 1; j < len(available); j++ {
				if available[j].State.RequestCount < available[i].State.RequestCount {
					available[i], available[j] = available[j], available[i]
				}
			}
		}
	}

	return available[:n]
}

func (kp *KeyPool) ReportSuccess(key string) {
	kp.mu.RLock()
	defer kp.mu.RUnlock()
	for _, k := range kp.keys {
		if k.Value == key {
			k.State.OnSuccess()
			return
		}
	}
}

func (kp *KeyPool) RecordLatency(key string, ms int64) {
	kp.mu.RLock()
	defer kp.mu.RUnlock()
	for _, k := range kp.keys {
		if k.Value == key {
			k.State.RecordLatency(ms)
			return
		}
	}
}

func (kp *KeyPool) ReportError(key string, statusCode int) {
	kp.mu.RLock()
	defer kp.mu.RUnlock()
	for _, k := range kp.keys {
		if k.Value == key {
			if statusCode == 401 {
				k.State.On401()
				kp.logger.Warn("key permanently skipped (401)",
					"channel", kp.channelID,
					"key_name", k.Name,
					"key_value", k.Value,
					"reason", "401 Unauthorized",
				)
			} else if statusCode == 429 {
				k.State.On429()
				kp.logger.Warn("key rate limited (429)",
					"channel", kp.channelID,
					"key_name", k.Name,
					"rate_limit_count", k.State.RateLimitScore(),
				)
			} else {
				k.State.OnError()
				if k.State.Paused {
					kp.logger.LogKeyPaused(kp.channelID, k.Name, k.State.ConsecErrors, k.State.PauseUntil)
				}
			}
			return
		}
	}
}

func (kp *KeyPool) GetStats() []KeyStats {
	kp.mu.RLock()
	defer kp.mu.RUnlock()
	var stats []KeyStats
	for _, k := range kp.keys {
		stats = append(stats, KeyStats{
			Name:               k.Name,
			RequestCount:       k.State.RequestCount,
			ErrorCount:         k.State.ErrorCount,
			AvgLatencyMs:       k.State.AvgLatencyMs(),
			LastSuccessTime:    k.State.LastSuccessTime,
			LastErrorTime:      k.State.LastErrorTime,
			LastError:          k.State.LastError,
			Paused:             k.State.Paused,
			PauseUntil:         k.State.PauseUntil,
			PermanentlySkipped: k.State.PermanentlySkipped,
			RateLimitCount:     k.State.RateLimitCount,
		})
	}
	return stats
}

func (kp *KeyPool) PauseKey(keyName string) {
	kp.mu.Lock()
	defer kp.mu.Unlock()
	for _, k := range kp.keys {
		if k.Name == keyName {
			k.State.Paused = true
			k.State.PauseUntil = time.Now().Add(24 * time.Hour)
			return
		}
	}
}

func (kp *KeyPool) ResumeKey(keyName string) {
	kp.mu.Lock()
	defer kp.mu.Unlock()
	for _, k := range kp.keys {
		if k.Name == keyName {
			k.State.ResetPause()
			k.State.PermanentlySkipped = false
			kp.logger.LogKeyResumed(kp.channelID, k.Name)
			return
		}
	}
}

func (kp *KeyPool) SkipKey(keyName string) {
	kp.mu.Lock()
	defer kp.mu.Unlock()
	for _, k := range kp.keys {
		if k.Name == keyName {
			k.State.On401()
			return
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

