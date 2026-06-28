package proxy

import "fmt"

// FailoverError represents an error that may trigger cross-channel failover.
// If the dispatch loop receives a FailoverError, it tries the next candidate channel.
type FailoverError struct {
	StatusCode int    // upstream HTTP status code; 0 means connection failure
	Message    string // human-readable description
}

func (e *FailoverError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("failover error (status %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("failover error (connection): %s", e.Message)
}

// IsFailoverable determines whether a given error/status should trigger failover.
// 400 → not failoverable (client error, return immediately)
// 401 → failoverable (key problem, try next channel after key rotation)
// 429 → failoverable (rate limit)
// 4xx → failoverable (may be key-related)
// 5xx → failoverable (server error)
// connection failure → failoverable
func IsFailoverable(statusCode int) bool {
	if statusCode == 0 {
		return true // connection failure
	}
	if statusCode == 400 {
		return false
	}
	return statusCode >= 400
}

// IsConsecutiveFailCandidate returns true for errors that should increment
// the consecutive fail counter (5xx and connection failures).
func IsConsecutiveFailCandidate(statusCode int, connErr bool) bool {
	if connErr {
		return true
	}
	return statusCode >= 500
}
