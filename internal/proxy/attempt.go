package proxy

import "net/http"

// attemptClass is the single status contract shared by every forwarding path.
// It describes the upstream result before a path-specific response converter
// decides how to write or retry it.
type attemptClass uint8

const (
	attemptSuccess attemptClass = iota
	attemptUnauthorized
	attemptRateLimited
	attemptMappedBadRequest
	attemptClientError
	attemptServerError
	attemptNetworkError
)

func classifyAttempt(status int, err error) attemptClass {
	if err != nil || status == 0 {
		return attemptNetworkError
	}
	switch {
	case status == http.StatusUnauthorized:
		return attemptUnauthorized
	case status == http.StatusTooManyRequests:
		return attemptRateLimited
	case status == http.StatusBadRequest:
		return attemptMappedBadRequest
	case status >= 500:
		return attemptServerError
	case status >= 400:
		return attemptClientError
	default:
		return attemptSuccess
	}
}
