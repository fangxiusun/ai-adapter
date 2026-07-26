package proxy

import (
	"errors"
	"net/http"
	"testing"
)

func TestClassifyAttemptContract(t *testing.T) {
	tests := []struct {
		name   string
		status int
		err    error
		want   attemptClass
	}{
		{name: "success", status: http.StatusOK, want: attemptSuccess},
		{name: "redirect remains successful transport result", status: http.StatusTemporaryRedirect, want: attemptSuccess},
		{name: "unauthorized", status: http.StatusUnauthorized, want: attemptUnauthorized},
		{name: "rate limited", status: http.StatusTooManyRequests, want: attemptRateLimited},
		{name: "bad request maps to rate limit policy", status: http.StatusBadRequest, want: attemptMappedBadRequest},
		{name: "other client error", status: http.StatusForbidden, want: attemptClientError},
		{name: "server error", status: http.StatusBadGateway, want: attemptServerError},
		{name: "connection error", err: errors.New("connection reset"), want: attemptNetworkError},
		{name: "zero status", want: attemptNetworkError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyAttempt(tt.status, tt.err); got != tt.want {
				t.Fatalf("classifyAttempt(%d, %v) = %v, want %v", tt.status, tt.err, got, tt.want)
			}
		})
	}
}
