package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	obotcmd "github.com/obot-platform/cmd"
	"github.com/obot-platform/obot/apiclient/types"

	"github.com/obot-platform/obot-sentry/pkg/mdmconfig"
	"github.com/obot-platform/obot-sentry/pkg/state"
)

// runStatus runs the status command against cfg and dirs, isolated from the
// host's MDM configuration and environment, and returns its output.
func runStatus(t *testing.T, cfg mdmconfig.Config, dirs statusDirs) (string, error) {
	t.Helper()
	for _, key := range []string{"OBOT_SENTRY_SERVER_URL", "OBOT_SENTRY_ENROLLMENT_KEY", "OBOT_SENTRY_SCAN_INTERVAL_MINUTES"} {
		t.Setenv(key, "")
	}
	status := &Status{
		ConfigFlags: ConfigFlags{loadMDMConfig: func() (mdmconfig.Config, error) { return cfg, nil }},
		resolveDirs: func() (statusDirs, error) { return dirs, nil },
	}
	cmd := obotcmd.Command(status)
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs(nil)
	err := cmd.Execute()
	return stdout.String(), err
}

func tempStatusDirs(t *testing.T) statusDirs {
	t.Helper()
	dir := t.TempDir()
	return statusDirs{data: dir, identity: dir, cache: filepath.Join(dir, "cache")}
}

func requireExitCode(t *testing.T, err error, want int) {
	t.Helper()
	exitErr, ok := errors.AsType[*ExitCodeError](err)
	if !ok {
		t.Fatalf("expected an *ExitCodeError with code %d, got %v", want, err)
	}
	if exitErr.Code != want {
		t.Fatalf("exit code = %d, want %d (err: %v)", exitErr.Code, want, err)
	}
	if !exitErr.Quiet {
		t.Errorf("status already prints the problem, so its error should be quiet")
	}
}

// The placeholder can never enroll, so status reports it as a configuration
// problem without spending a request on it.
func TestStatusPlaceholderKeyIsAConfigErrorWithoutContactingServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s", r.URL.Path)
	}))
	defer srv.Close()

	out, err := runStatus(t, mdmconfig.Config{ServerURL: srv.URL, EnrollmentKey: mdmconfig.EnrollmentKeyPlaceholder}, tempStatusDirs(t))
	requireExitCode(t, err, 2)
	for _, want := range []string{
		"REPLACE_WITH_ENROLLMENT_KEY (the placeholder, not a real key)",
		"Enrollment:",
		"failed:",
		"none recorded for this user",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// A key the server rejects is a configuration problem too, and the report must
// not print the key's secret.
func TestStatusRejectedKeyIsAConfigError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	out, err := runStatus(t, mdmconfig.Config{ServerURL: srv.URL, EnrollmentKey: "ode1-3-17-secret"}, tempStatusDirs(t))
	requireExitCode(t, err, 2)
	if !strings.Contains(out, "the server rejected the enrollment key") {
		t.Errorf("output should say the server rejected the key:\n%s", out)
	}
	if strings.Contains(out, "secret") {
		t.Errorf("output leaks the enrollment key's secret:\n%s", out)
	}
}

// An unreachable server says nothing about the configuration, so it exits 1.
func TestStatusUnreachableServerIsARuntimeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	_, err := runStatus(t, mdmconfig.Config{ServerURL: srv.URL, EnrollmentKey: "ode1-3-17-secret"}, tempStatusDirs(t))
	requireExitCode(t, err, 1)
}

func TestStatusMissingServerURL(t *testing.T) {
	out, err := runStatus(t, mdmconfig.Config{EnrollmentKey: "ode1-3-17-secret"}, tempStatusDirs(t))
	requireExitCode(t, err, 2)
	if !strings.Contains(out, "not checked: no server URL configured") {
		t.Errorf("output should say enrollment wasn't checked:\n%s", out)
	}
}

// Status enrolls an unenrolled device and saves it like a scan would, so a
// second run reads the saved enrollment instead of enrolling again.
func TestStatusEnrollsOnceThenReportsEnrollment(t *testing.T) {
	var enrolls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mdm/enroll" {
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		enrolls.Add(1)
		var req types.DeviceEnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode enroll request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(types.DeviceEnrollResponse{Device: types.Device{DeviceID: req.DeviceID, MDMConfigurationID: 3}})
	}))
	defer srv.Close()

	cfg := mdmconfig.Config{ServerURL: srv.URL, EnrollmentKey: "ode1-3-17-secret"}
	dirs := tempStatusDirs(t)
	for range 2 {
		out, err := runStatus(t, cfg, dirs)
		if err != nil {
			t.Fatalf("status: %v\n%s", err, out)
		}
		if !strings.Contains(out, "enrolled as device ") {
			t.Errorf("output should report the enrollment:\n%s", out)
		}
		if !strings.Contains(out, "ode1-3-17-…") {
			t.Errorf("output should show the redacted key:\n%s", out)
		}
	}
	if got := enrolls.Load(); got != 1 {
		t.Errorf("enroll requests = %d, want 1 (the second status reads the saved enrollment)", got)
	}
}

// Status surfaces the last scan's failure from scan.json, along with where
// that file lives.
func TestStatusReportsLastScan(t *testing.T) {
	dirs := tempStatusDirs(t)
	scannedAt := time.Date(2026, 10, 15, 16, 28, 26, 0, time.UTC)
	if err := (state.ScanState{
		LastScanAt: &scannedAt,
		LastStatus: "error",
		LastError:  "enrolling device: boom",
	}).Save(dirs.cache); err != nil {
		t.Fatal(err)
	}

	out, _ := runStatus(t, mdmconfig.Config{}, dirs)
	for _, want := range []string{
		"failed at 2026-10-15T16:28:26Z: enrolling device: boom",
		"Last submitted:  never",
		filepath.Join(dirs.cache, "scan.json"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
