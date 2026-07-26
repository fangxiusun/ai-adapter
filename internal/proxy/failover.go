package proxy

import (
	"fmt"
	"time"
)

// FailoverError represents an error that may trigger cross-channel failover.
// If the dispatch loop receives a FailoverError, it tries the next candidate channel.
type FailoverError struct {
	StatusCode           int    // upstream HTTP status code; 0 means connection failure
	Message              string // human-readable description
	AffectsChannelHealth bool   // only upstream 5xx and connection failures set this
	Handled              bool   // response was already handled; do not fail over or write again
	RetryNext            bool   // current attempt finished; scheduler should try another pair
	RetryKey             string // key used by the retryable attempt
	RetryCooldownUntil   time.Time
}

func handledError(statusCode int, message string) *FailoverError {
	return &FailoverError{StatusCode: statusCode, Message: message, Handled: true}
}

func (e *FailoverError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("failover error (status %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("failover error (connection): %s", e.Message)
}
