package stats

import (
	"sync"
	"time"
)

// MinuteStats holds aggregated stats for one minute.
type MinuteStats struct {
	Timestamp    time.Time  `json:"timestamp"`
	Total        int        `json:"total"`
	Success      int        `json:"success"`
	Errors       int        `json:"errors"`
	StatusCounts map[int]int `json:"status_counts"`
	TotalLatencyMs int64 `json:"total_latency_ms"`
}

// HistoryEntry is returned by GetHistory.
type HistoryEntry struct {
	Timestamp string `json:"timestamp"`
	Total     int    `json:"total"`
	Success   int    `json:"success"`
	Errors    int    `json:"errors"`
}

// ErrorDistEntry is returned by GetErrorDistribution.
type ErrorDistEntry struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// Stats maintains in-memory per-minute stats for the last hour.
type Stats struct {
	mu      sync.RWMutex
	slots   [60]*MinuteStats
	cursor  int
	current *MinuteStats
}

// NewStats creates a new Stats instance.
func NewStats() *Stats {
	s := &Stats{}
	now := time.Now().Truncate(time.Minute)
	for i := range s.slots {
		s.slots[i] = &MinuteStats{
			Timestamp:    now.Add(time.Duration(i-59) * time.Minute),
			StatusCounts: make(map[int]int),
		}
	}
	s.cursor = 59
	s.current = s.slots[59]
	return s
}

// Record adds a request result to the current minute.
func (s *Stats) Record(status int, latencyMs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Truncate(time.Minute)
	if now.After(s.current.Timestamp) {
		// Advance to new minute(s)
		for s.current.Timestamp.Before(now) {
			s.cursor = (s.cursor + 1) % 60
			s.slots[s.cursor] = &MinuteStats{
				Timestamp:    s.current.Timestamp.Add(time.Minute),
				StatusCounts: make(map[int]int),
			}
			s.current = s.slots[s.cursor]
		}
	}

	s.current.Total++
	s.current.StatusCounts[status]++
	s.current.TotalLatencyMs += latencyMs
	if status >= 200 && status < 400 {
		s.current.Success++
	} else {
		s.current.Errors++
	}
}

// GetHistory returns the last N minutes of stats.
func (s *Stats) GetHistory(minutes int) []HistoryEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if minutes > 60 {
		minutes = 60
	}
	if minutes < 1 {
		minutes = 1
	}

	result := make([]HistoryEntry, 0, minutes)
	for i := minutes - 1; i >= 0; i-- {
		idx := (s.cursor - i + 60) % 60
		slot := s.slots[idx]
		result = append(result, HistoryEntry{
			Timestamp: slot.Timestamp.Format("15:04"),
			Total:     slot.Total,
			Success:   slot.Success,
			Errors:    slot.Errors,
		})
	}
	return result
}

// GetErrorDistribution returns error distribution for the last N minutes.
func (s *Stats) GetErrorDistribution(minutes int) []ErrorDistEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if minutes > 60 {
		minutes = 60
	}

	dist := make(map[string]int)
	for i := minutes - 1; i >= 0; i-- {
		idx := (s.cursor - i + 60) % 60
		slot := s.slots[idx]
		for status, count := range slot.StatusCounts {
			switch {
			case status >= 200 && status < 300:
				dist["2xx"] += count
			case status == 400:
				dist["400"] += count
			case status == 401:
				dist["401"] += count
			case status == 429:
				dist["429"] += count
			case status >= 400 && status < 500:
				dist["4xx"] += count
			case status >= 500:
				dist["5xx"] += count
			}
		}
	}

	result := make([]ErrorDistEntry, 0, len(dist))
	for status, count := range dist {
		result = append(result, ErrorDistEntry{Status: status, Count: count})
	}
	return result
}

// GetCurrentQPS returns requests in the current minute divided by 60.
func (s *Stats) GetCurrentQPS() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return float64(s.current.Total) / 60.0
}

// GetErrorRate returns the error rate for the current minute.
func (s *Stats) GetErrorRate() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current.Total == 0 {
		return 0
	}
	return float64(s.current.Errors) / float64(s.current.Total)
}

// GetAvgLatencyMs returns the average latency in ms for the current minute.
func (s *Stats) GetAvgLatencyMs() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current.Total == 0 {
		return 0
	}
	return s.current.TotalLatencyMs / int64(s.current.Total)
}
