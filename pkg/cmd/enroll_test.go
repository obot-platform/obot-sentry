package cmd

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/obot-platform/obot/apiclient/types"
	"github.com/spf13/cobra"

	"github.com/obot-platform/obot-sentry/pkg/eventlog"
	"github.com/obot-platform/obot-sentry/pkg/identity"
	"github.com/obot-platform/obot-sentry/pkg/state"
)

// An installer's enrollment check fails only on a server rejection; anything
// else, including a missing configuration, lets the install finish.
func TestInstallCheckResult(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantExitCode int
		wantStdout   string
		wantStderr   string
	}{
		{
			name:         "enrolled",
			err:          nil,
			wantExitCode: 0,
			wantStdout:   "enrolled with https://obot.example.com as device device-1",
		},
		{
			name:         "rejected key fails the install",
			err:          types.NewErrHTTP(http.StatusUnauthorized, "unauthorized"),
			wantExitCode: exitCodeRejected,
		},
		{
			name:         "device limit fails the install",
			err:          types.NewErrHTTP(http.StatusForbidden, "device limit"),
			wantExitCode: exitCodeRejected,
		},
		{
			name:         "unreachable server lets the install finish",
			err:          errors.New("dial tcp: connection refused"),
			wantExitCode: 0,
			wantStderr:   "the scheduled scan will retry",
		},
		{
			name:         "server error lets the install finish",
			err:          types.NewErrHTTP(http.StatusServiceUnavailable, "unavailable"),
			wantExitCode: 0,
			wantStderr:   "the scheduled scan will retry",
		},
		{
			name:         "missing configuration lets the install finish",
			err:          NewConfigError(errors.New("no enrollment key configured")),
			wantExitCode: 0,
			wantStderr:   "no enrollment key configured",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)

			err := installCheckResult(cmd, "https://obot.example.com", &identity.Identity{DeviceID: "device-1"}, tt.err)

			gotExitCode := 0
			if err != nil {
				exitErr, ok := errors.AsType[*ExitCodeError](err)
				if !ok {
					t.Fatalf("expected an *ExitCodeError, got %T: %v", err, err)
				}
				gotExitCode = exitErr.Code
			}
			if gotExitCode != tt.wantExitCode {
				t.Fatalf("exit code = %d, want %d (err %v)", gotExitCode, tt.wantExitCode, err)
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Fatalf("stdout %q does not contain %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("stderr %q does not contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

// A server rejection exits 3, while errors that already carry an exit code keep it.
func TestRejectedExitCode(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantExitCode int
	}{
		{
			name:         "nil",
			err:          nil,
			wantExitCode: 0,
		},
		{
			name:         "rejected",
			err:          types.NewErrHTTP(http.StatusUnauthorized, "unauthorized"),
			wantExitCode: exitCodeRejected,
		},
		{
			name:         "configuration error keeps its code",
			err:          NewConfigError(errors.New("no server URL configured")),
			wantExitCode: exitCodeConfig,
		},
		{
			name:         "other failure",
			err:          errors.New("connection refused"),
			wantExitCode: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rejectedExitCode(tt.err)
			gotExitCode := 0
			if err != nil {
				gotExitCode = 1
				if exitErr, ok := errors.AsType[*ExitCodeError](err); ok {
					gotExitCode = exitErr.Code
				}
			}
			if gotExitCode != tt.wantExitCode {
				t.Fatalf("exit code = %d, want %d", gotExitCode, tt.wantExitCode)
			}
		})
	}
}

// Scheduled scans log a failure when it first happens or changes, and the
// recovery after it, but not the same failure on every run.
func TestScanEventID(t *testing.T) {
	failure := errors.New("enrolling device: rejected")
	tests := []struct {
		name     string
		previous state.ScanState
		err      error
		want     uint32
	}{
		{
			name:     "first failure",
			previous: state.ScanState{LastStatus: "ok"},
			err:      failure,
			want:     eventlog.ScanFailed,
		},
		{
			name:     "first scan fails",
			previous: state.ScanState{},
			err:      failure,
			want:     eventlog.ScanFailed,
		},
		{
			name:     "same failure again",
			previous: state.ScanState{LastStatus: "error", LastError: failure.Error()},
			err:      failure,
			want:     0,
		},
		{
			name:     "different failure",
			previous: state.ScanState{LastStatus: "error", LastError: "dial tcp: connection refused"},
			err:      failure,
			want:     eventlog.ScanFailed,
		},
		{
			name:     "recovery",
			previous: state.ScanState{LastStatus: "error", LastError: failure.Error()},
			err:      nil,
			want:     eventlog.ScanRecovered,
		},
		{
			name:     "still healthy",
			previous: state.ScanState{LastStatus: "ok"},
			err:      nil,
			want:     0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scanEventID(tt.previous, tt.err); got != tt.want {
				t.Fatalf("scanEventID() = %d, want %d", got, tt.want)
			}
		})
	}
}
