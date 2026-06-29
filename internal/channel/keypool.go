package channel

import (
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/db"
	"github.com/fangxiusun/ai-adapter/internal/log"
)

type KeyEntry struct {
	Value string
	Name  string
	State *KeyState
}

type KeyPool struct {
	keys         []*KeyEntry
	strategy     string
	channelID    string
	counter      uint64
	logger       *log.Logger
	mu           sync.RWMutex
	database     *db.DB
	syncInterval time.Duration
	stopCh       chan struct{}
}

func NewKeyPool(keyCfgs []config.KeyConfig, strategy, channelID string, logger *log.Logger, consecThreshold, pauseMultiplierSec, pauseMaxSec int, database *db.DB, syncInterval time.Duration) *KeyPool {
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
	kp := &KeyPool{
		keys:         keys,
		strategy:     strategy,
		channelID:    channelID,
		logger:       logger,
		database:     database,
		syncInterval: syncInterval,
		stopCh:       make(chan struct{}),
	}
	kp.loadFromDB()
	if syncInterval > 0 && database != nil {
		go kp.syncLoop()
	}
	return kp
}

func (kp *KeyPool) Next() *KeyEntry {
	kp.mu.RLock()

	var available []*KeyEntry
	var hasExpiredPaused bool
	for _, k := range kp.keys {
		if k.State.IsAvailable() {
			available = append(available, k)
		} else if k.State.IsPauseExpired() {
			hasExpiredPaused = true
		}
	}
	kp.mu.RUnlock()

	// If no available keys but some have expired pauses, reset them under write lock.
	if len(available) == 0 && hasExpiredPaused {
		kp.mu.Lock()
		allSkipped := true
		for _, k := range kp.keys {
			if !k.State.PermanentlySkipped {
				allSkipped = false
				k.State.ResetPause()
			}
		}
		kp.mu.Unlock()
		if allSkipped {
			return nil
		}
		// Re-scan under read lock.
		kp.mu.RLock()
		for _, k := range kp.keys {
			if k.State.IsAvailable() {
				available = append(available, k)
			}
		}
		kp.mu.RUnlock()
	}

	if len(available) == 0 {
		return nil
	}

	// Final selection under read lock — re-validate availability after the lock gap.
	kp.mu.RLock()
	defer kp.mu.RUnlock()
	filtered := available[:0]
	for _, k := range available {
		if k.State.IsAvailable() && !k.State.PermanentlySkipped {
			filtered = append(filtered, k)
		}
	}
	available = filtered
	if len(available) == 0 {
		return nil
	}

	switch kp.strategy {
	case "random":
		selected := available[rand.Intn(len(available))]
		if selected.State.IsPauseExpired() {
			selected.State.ResetPause()
		}
		return selected
	case "least-errors":
		best := available[0]
		for _, k := range available[1:] {
			if k.State.ErrorCount < best.State.ErrorCount {
				best = k
			}
		}
		if best.State.IsPauseExpired() {
			best.State.ResetPause()
		}
		return best
	case "least-latency":
		best := available[0]
		for _, k := range available[1:] {
			if k.State.AvgLatencyMs() < best.State.AvgLatencyMs() {
				best = k
			}
		}
		if best.State.IsPauseExpired() {
			best.State.ResetPause()
		}
		return best
	case "least-rate-limited":
		selected := kp.leastRateLimited(available)
		if selected != nil && selected.State.IsPauseExpired() {
			selected.State.ResetPause()
		}
		return selected
	default:
		idx := atomic.AddUint64(&kp.counter, 1)
		selected := available[idx%uint64(len(available))]
		if selected.State.IsPauseExpired() {
			selected.State.ResetPause()
		}
		return selected
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

func (kp *KeyPool) loadFromDB() {
	if kp.database == nil {
		return
	}
	rows, err := kp.database.LoadKeyStats(kp.channelID)
	if err != nil {
		kp.logger.Warn("failed to load key stats from db", "channel", kp.channelID, "error", err)
		return
	}
	rowMap := make(map[string]db.KeyStatsRow)
	for _, r := range rows {
		rowMap[r.KeyValue] = r
	}
	kp.mu.Lock()
	defer kp.mu.Unlock()
	for _, k := range kp.keys {
		if r, ok := rowMap[k.Value]; ok {
			k.State.RequestCount = r.RequestCount
			k.State.ErrorCount = r.ErrorCount
			k.State.Error400 = r.Error400
			k.State.Error401 = r.Error401
			k.State.Error403 = r.Error403
			k.State.Error404 = r.Error404
			k.State.Error429 = r.Error429
			k.State.Error4xx = r.Error4xx
			k.State.Error5xx = r.Error5xx
			k.State.ErrorNetwork = r.ErrorNetwork
			k.State.ErrorStream = r.ErrorStream
			k.State.TotalLatencyMs = r.TotalLatencyMs
			k.State.LastError = r.LastError
			if r.LastErrorTime > 0 {
				k.State.LastErrorTime = time.UnixMilli(r.LastErrorTime)
			}
			if r.LastSuccessTime > 0 {
				k.State.LastSuccessTime = time.UnixMilli(r.LastSuccessTime)
			}
			k.State.Paused = r.Paused
			if r.PauseUntil > 0 {
				k.State.PauseUntil = time.UnixMilli(r.PauseUntil)
			}
		}
	}
}

func (kp *KeyPool) syncLoop() {
	ticker := time.NewTicker(kp.syncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-kp.stopCh:
			return
		case <-ticker.C:
			kp.SaveToDB()
		}
	}
}

func (kp *KeyPool) Stop() {
	close(kp.stopCh)
}

func (kp *KeyPool) SaveToDB() {
	if kp.database == nil {
		return
	}
	kp.mu.RLock()
	rows := make([]db.KeyStatsRow, 0, len(kp.keys))
	for _, k := range kp.keys {
		rows = append(rows, db.KeyStatsRow{
			ChannelID:       kp.channelID,
			KeyName:         k.Name,
			KeyValue:        k.Value,
			RequestCount:    k.State.RequestCount,
			ErrorCount:      k.State.ErrorCount,
			Error400:        k.State.Error400,
			Error401:        k.State.Error401,
			Error403:        k.State.Error403,
			Error404:        k.State.Error404,
			Error429:        k.State.Error429,
			Error4xx:        k.State.Error4xx,
			Error5xx:        k.State.Error5xx,
			ErrorNetwork:    k.State.ErrorNetwork,
			ErrorStream:     k.State.ErrorStream,
			TotalLatencyMs:  k.State.TotalLatencyMs,
			LastError:       k.State.LastError,
			LastErrorTime:   k.State.LastErrorTime.UnixMilli(),
			LastSuccessTime: k.State.LastSuccessTime.UnixMilli(),
			Paused:          k.State.Paused,
			PauseUntil:      k.State.PauseUntil.UnixMilli(),
		})
	}
	kp.mu.RUnlock()

	if err := kp.database.SaveKeyStatsBatch(rows); err != nil {
		kp.logger.Warn("failed to save key stats", "channel", kp.channelID, "error", err)
	}
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
			switch {
			case statusCode == 401:
				k.State.On401()
				kp.logger.Warn("key permanently skipped (401)",
					"channel", kp.channelID,
					"key_name", k.Name,
					"key_value", k.Value,
					"reason", "401 Unauthorized",
				)
			case statusCode == 429:
				k.State.On429()
				kp.logger.Warn("key rate limited (429)",
					"channel", kp.channelID,
					"key_name", k.Name,
					"rate_limit_count", k.State.RateLimitScore(),
				)
			case statusCode == 400:
				k.State.On400()
			case statusCode == 403:
				k.State.On403()
			case statusCode == 404:
				k.State.On404()
			case statusCode >= 400 && statusCode < 500:
				k.State.OnError4xx()
			case statusCode >= 500:
				k.State.OnError5xx(statusCode)
			default:
				k.State.OnErrorNetwork()
			}
			if k.State.Paused {
				kp.logger.LogKeyPaused(kp.channelID, k.Name, k.State.ConsecErrors, k.State.PauseUntil)
			}
			return
		}
	}
}

func (kp *KeyPool) ReportStreamError(key string) {
	kp.mu.RLock()
	defer kp.mu.RUnlock()
	for _, k := range kp.keys {
		if k.Value == key {
			k.State.OnErrorStream()
			if k.State.Paused {
				kp.logger.LogKeyPaused(kp.channelID, k.Name, k.State.ConsecErrors, k.State.PauseUntil)
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
			Value:              k.Value,
			RequestCount:       k.State.RequestCount,
			ErrorCount:         k.State.ErrorCount,
			Error400:           k.State.Error400,
			Error401:           k.State.Error401,
			Error403:           k.State.Error403,
			Error404:           k.State.Error404,
			Error429:           k.State.Error429,
			Error4xx:           k.State.Error4xx,
			Error5xx:           k.State.Error5xx,
			ErrorNetwork:       k.State.ErrorNetwork,
			ErrorStream:        k.State.ErrorStream,
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

// GetValidKeys returns key configs for keys that have never received a 401 error.
func (kp *KeyPool) GetValidKeys() []config.KeyConfig {
	kp.mu.RLock()
	defer kp.mu.RUnlock()
	var valid []config.KeyConfig
	for _, k := range kp.keys {
		if k.State.Error401 == 0 && !k.State.PermanentlySkipped {
			valid = append(valid, config.KeyConfig{Value: k.Value, Name: k.Name})
		}
	}
	return valid
}

func (kp *KeyPool) PauseKey(keyValue string) {
	kp.mu.Lock()
	defer kp.mu.Unlock()
	for _, k := range kp.keys {
		if k.Value == keyValue {
			k.State.Paused = true
			k.State.PauseUntil = time.Now().Add(24 * time.Hour)
			return
		}
	}
}

func (kp *KeyPool) ResumeKey(keyValue string) {
	kp.mu.Lock()
	defer kp.mu.Unlock()
	for _, k := range kp.keys {
		if k.Value == keyValue {
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


// GetN returns up to n available keys for fanout.
func (kp *KeyPool) GetN(n int) []*KeyEntry {
	kp.mu.RLock()
	defer kp.mu.RUnlock()
	var available []*KeyEntry
	for _, k := range kp.keys {
		if k.State.IsAvailable() && !k.State.PermanentlySkipped {
			available = append(available, k)
		}
	}
	if len(available) <= n {
		return available
	}
	// Shuffle to avoid deterministic selection, then take first n.
	rand.Shuffle(len(available), func(i, j int) {
		available[i], available[j] = available[j], available[i]
	})
	return available[:n]
}

// Size returns the total number of keys in the pool.
func (kp *KeyPool) Size() int {
	kp.mu.RLock()
	defer kp.mu.RUnlock()
	return len(kp.keys)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}


// ListKeys returns all keys in the pool.
func (kp *KeyPool) ListKeys() []config.KeyConfig {
	kp.mu.RLock()
	defer kp.mu.RUnlock()
	result := make([]config.KeyConfig, 0, len(kp.keys))
	for _, k := range kp.keys {
		result = append(result, config.KeyConfig{
			Name:  k.Name,
			Value: k.Value,
		})
	}
	return result
}

// RemoveKey removes a key by value.
func (kp *KeyPool) RemoveKey(keyValue string) error {
	kp.mu.Lock()
	defer kp.mu.Unlock()
	for i, k := range kp.keys {
		if k.Value == keyValue {
			kp.keys = append(kp.keys[:i], kp.keys[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("key not found")
}

// AddKey adds a new key to the pool.
func (kp *KeyPool) AddKey(value, name string) error {
	kp.mu.Lock()
	defer kp.mu.Unlock()
	for _, k := range kp.keys {
		if k.Value == value {
			return fmt.Errorf("key already exists")
		}
	}
	entry := &KeyEntry{
		Name:  name,
		Value: value,
		State: NewKeyState(3, 30, 600),
	}
	kp.keys = append(kp.keys, entry)
	return nil
}
