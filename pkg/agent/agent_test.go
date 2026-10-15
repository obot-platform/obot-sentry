package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obot-platform/obot/apiclient/types"

	"github.com/obot-platform/obot-sentry/pkg/client"
	"github.com/obot-platform/obot-sentry/pkg/identity"
	"github.com/obot-platform/obot-sentry/pkg/mdmconfig"
	"github.com/obot-platform/obot-sentry/pkg/state"
)

// enrolledAgent builds an Agent whose local state already records an
// enrollment for the identity under identityDir, so EnsureEnrolled is a no-op
// and SubmitLocalAgentAuditLogs proceeds straight to a live submit.
func enrolledAgent(t *testing.T, serverURL, enrollmentKey string) (*Agent, *identity.Identity, state.State) {
	t.Helper()
	dir := t.TempDir()

	id, err := identity.Load(dir)
	if err != nil {
		t.Fatalf("load identity: %v", err)
	}
	now := time.Now().UTC()
	st := state.State{
		DeviceID:             id.DeviceID,
		ServerURL:            client.NormalizeBaseURL(serverURL),
		PublicKeyFingerprint: id.PublicKeyFingerprint(),
		EnrolledAt:           &now,
	}
	if err := st.Save(dir); err != nil {
		t.Fatalf("save state: %v", err)
	}

	a := New(dir, dir, mdmconfig.Config{ServerURL: serverURL, EnrollmentKey: enrollmentKey})
	return a, id, st
}

// A 401/403 with no enrollment key configured must surface as an unauthorized
// (*ErrHTTP) error, not a plain "not enrolled" error, so the caller discards
// the event and fails open rather than spooling something that can never
// authenticate.
func TestSubmitLocalAgentAuditLogsUnauthorizedWithoutKeyDoesNotReEnroll(t *testing.T) {
	var submitCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/local-agent-audit-logs" {
			submitCalls++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		t.Errorf("unexpected request to %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	a, id, st := enrolledAgent(t, srv.URL, "")

	err := a.SubmitLocalAgentAuditLogs(t.Context(), id, st, []types.LocalAgentToolCallAuditLogInput{{}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !client.IsUnauthorized(err) {
		t.Fatalf("expected an unauthorized (*ErrHTTP) error so the caller discards without spooling, got %v", err)
	}
	if submitCalls != 1 {
		t.Fatalf("expected exactly one submit attempt (no re-enroll/retry), got %d", submitCalls)
	}
}

// A 5xx response is a transient failure: the caller spools and retries. It must
// not be classified as an auth (unauthorized) or client (4xx) error.
func TestSubmitLocalAgentAuditLogs5xxIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/local-agent-audit-logs" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	a, id, st := enrolledAgent(t, srv.URL, "")

	err := a.SubmitLocalAgentAuditLogs(context.Background(), id, st, []types.LocalAgentToolCallAuditLogInput{{}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if client.IsUnauthorized(err) || client.IsClientError(err) {
		t.Fatalf("5xx must not be auth/client error, got %v", err)
	}
	if !client.IsTransient(err) {
		t.Fatalf("5xx must be transient so the caller spools, got %v", err)
	}
}

// Once the server accepts a batch, a failure to update the local submission
// timestamp must not make callers spool or retain that already-delivered batch.
func TestSubmitLocalAgentAuditLogsStateSaveFailureDoesNotFailSubmit(t *testing.T) {
	var submitCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/local-agent-audit-logs" {
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		submitCalls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	a, id, st := enrolledAgent(t, srv.URL, "")
	// State.Save uses an atomic temporary file, so a missing parent directory
	// deterministically fails without depending on platform permission rules.
	a.DataDir = filepath.Join(t.TempDir(), "missing")

	if err := a.SubmitLocalAgentAuditLogs(t.Context(), id, st, []types.LocalAgentToolCallAuditLogInput{{}}); err != nil {
		t.Fatalf("successful submit should ignore timestamp save failure: %v", err)
	}
	if submitCalls != 1 {
		t.Fatalf("expected exactly one successful submit, got %d", submitCalls)
	}
}

// A placeholder key can't re-enroll either, so a rejected device credential
// must surface as the original 401 rather than a key error that would spool.
func TestSubmitLocalAgentAuditLogsUnauthorizedWithPlaceholderKeyDoesNotReEnroll(t *testing.T) {
	var submitCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/local-agent-audit-logs" {
			submitCalls++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		t.Errorf("unexpected request to %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	a, id, st := enrolledAgent(t, srv.URL, mdmconfig.EnrollmentKeyPlaceholder)

	err := a.SubmitLocalAgentAuditLogs(t.Context(), id, st, []types.LocalAgentToolCallAuditLogInput{{}})
	if !client.IsUnauthorized(err) {
		t.Fatalf("expected an unauthorized (*ErrHTTP) error so the caller discards without spooling, got %v", err)
	}
	if submitCalls != 1 {
		t.Fatalf("expected exactly one submit attempt (no re-enroll/retry), got %d", submitCalls)
	}
}

// Keys that can't possibly enroll are rejected locally, before any request.
func TestEnsureEnrolledRejectsMalformedKeyWithoutContactingServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	for _, key := range []string{"", mdmconfig.EnrollmentKeyPlaceholder, "ode1-not-a-key"} {
		dir := t.TempDir()
		_, _, err := New(dir, dir, mdmconfig.Config{ServerURL: srv.URL, EnrollmentKey: key}).EnsureEnrolled(t.Context())
		if _, ok := errors.AsType[*mdmconfig.EnrollmentKeyError](err); !ok {
			t.Errorf("EnsureEnrolled with key %q = %v, want an *EnrollmentKeyError", key, err)
		}
	}
}

// A 401 from enroll is reported as a key problem while still reading as
// unauthorized to the audit path, which discards on 401.
func TestEnsureEnrolledReportsRejectedKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	dir := t.TempDir()
	_, _, err := New(dir, dir, mdmconfig.Config{ServerURL: srv.URL, EnrollmentKey: "ode1-3-17-secret"}).EnsureEnrolled(t.Context())
	keyErr, ok := errors.AsType[*mdmconfig.EnrollmentKeyError](err)
	if !ok {
		t.Fatalf("expected an *EnrollmentKeyError for a 401 from enroll, got %v", err)
	}
	if !strings.Contains(keyErr.Error(), "server rejected the enrollment key") {
		t.Errorf("error = %q, want it to say the server rejected the key", keyErr.Error())
	}
	if !client.IsUnauthorized(err) {
		t.Errorf("a rejected key must still read as unauthorized for callers that discard on 401, got %v", err)
	}
}

// Only auth failures blame the key; a server error stays a runtime failure.
func TestEnsureEnrolledLeavesTransientFailuresAlone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	dir := t.TempDir()
	_, _, err := New(dir, dir, mdmconfig.Config{ServerURL: srv.URL, EnrollmentKey: "ode1-3-17-secret"}).EnsureEnrolled(t.Context())
	if err == nil {
		t.Fatal("expected an error")
	}
	if _, ok := errors.AsType[*mdmconfig.EnrollmentKeyError](err); ok {
		t.Fatalf("a 503 is not a key problem, got %v", err)
	}
}
