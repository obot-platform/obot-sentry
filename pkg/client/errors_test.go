package client

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/obot-platform/obot/apiclient/types"
)

// Only a 4xx other than a timeout or rate limit is a rejection; network errors
// and 5xx responses may succeed on retry.
func TestIsRejected(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "network error",
			err:  errors.New("dial tcp 127.0.0.1:1: connect: connection refused"),
			want: false,
		},
		{
			name: "unauthorized",
			err:  types.NewErrHTTP(http.StatusUnauthorized, "unauthorized"),
			want: true,
		},
		{
			name: "wrapped forbidden",
			err:  fmt.Errorf("enrolling device: %w", types.NewErrHTTP(http.StatusForbidden, "device limit")),
			want: true,
		},
		{
			name: "not found",
			err:  types.NewErrHTTP(http.StatusNotFound, "not found"),
			want: true,
		},
		{
			name: "request timeout",
			err:  types.NewErrHTTP(http.StatusRequestTimeout, "timeout"),
			want: false,
		},
		{
			name: "rate limited",
			err:  types.NewErrHTTP(http.StatusTooManyRequests, "slow down"),
			want: false,
		},
		{
			name: "server error",
			err:  types.NewErrHTTP(http.StatusServiceUnavailable, "unavailable"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRejected(tt.err); got != tt.want {
				t.Fatalf("IsRejected(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
