package websocket

import (
	"net/http/httptest"
	"testing"
)

func TestNewOriginChecker(t *testing.T) {
	checkOrigin := newOriginChecker([]string{"https://admin.example.com"})

	tests := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{
			name:   "allow request without origin",
			host:   "localhost:8080",
			origin: "",
			want:   true,
		},
		{
			name:   "allow same host origin",
			host:   "localhost:8080",
			origin: "http://localhost:8080",
			want:   true,
		},
		{
			name:   "allow configured cross origin",
			host:   "localhost:8080",
			origin: "https://admin.example.com",
			want:   true,
		},
		{
			name:   "reject unknown cross origin",
			host:   "localhost:8080",
			origin: "https://evil.example",
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://"+tc.host+"/admin/api/ws", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}

			if got := checkOrigin(req); got != tc.want {
				t.Fatalf("checkOrigin() = %v, want %v", got, tc.want)
			}
		})
	}
}
