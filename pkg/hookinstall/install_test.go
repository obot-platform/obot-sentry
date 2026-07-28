package hookinstall

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

func TestSupportedPlatform(t *testing.T) {
	for goos, want := range map[string]bool{
		"darwin":  true,
		"windows": true,
		"linux":   false,
		"freebsd": false,
		"":        false,
	} {
		if got := supportedPlatform(goos); got != want {
			t.Fatalf("supportedPlatform(%q) = %v, want %v", goos, got, want)
		}
	}
}

// failIfCalled returns a seam that fails the test if the installer invokes it,
// used to prove preflight short-circuits in the correct order.
func failIfCalledPrivilege(t *testing.T) func() error {
	t.Helper()
	return func() error {
		t.Fatal("privilege check must not run on an unsupported platform")
		return nil
	}
}

func failIfCalledResolve(t *testing.T) func() (string, error) {
	t.Helper()
	return func() (string, error) {
		t.Fatal("executable resolution must not run after an earlier preflight failure")
		return "", nil
	}
}

func failIfCalledResolveUser(t *testing.T) func() (*TargetUser, error) {
	t.Helper()
	return func() (*TargetUser, error) {
		t.Fatal("user resolution must not run after an earlier preflight failure")
		return nil, nil
	}
}

func failIfCalledProvision(t *testing.T) func() (string, error) {
	t.Helper()
	return func() (string, error) {
		t.Fatal("identity provisioning must not run after an earlier preflight failure")
		return "", nil
	}
}

// stubUser returns a valid resolved user seam for success-path tests.
func stubUser() func() (*TargetUser, error) {
	return func() (*TargetUser, error) {
		return &TargetUser{Username: "alice", HomeDir: "/Users/alice", UID: 501, GID: 20}, nil
	}
}

func TestRunUnsupportedPlatformMakesNoChanges(t *testing.T) {
	var out bytes.Buffer
	inst := &Installer{
		GOOS:              "linux",
		Privilege:         failIfCalledPrivilege(t),
		ResolveExe:        failIfCalledResolve(t),
		ResolveUser:       failIfCalledResolveUser(t),
		ProvisionIdentity: failIfCalledProvision(t),
		Out:               &out,
	}
	err := inst.Run(context.Background())
	if !errors.Is(err, errUnsupportedPlatform) {
		t.Fatalf("expected unsupported-platform error, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output on unsupported platform, got %q", out.String())
	}
}

func TestRunPrivilegeFailureShortCircuits(t *testing.T) {
	wantErr := errors.New("needs sudo")
	var out bytes.Buffer
	inst := &Installer{
		GOOS:              "darwin",
		Privilege:         func() error { return wantErr },
		ResolveExe:        failIfCalledResolve(t),
		ResolveUser:       failIfCalledResolveUser(t),
		ProvisionIdentity: failIfCalledProvision(t),
		Out:               &out,
	}
	err := inst.Run(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected privilege error, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output on privilege failure, got %q", out.String())
	}
}

func TestRunResolveExeFailure(t *testing.T) {
	wantErr := errors.New("bad executable")
	var out bytes.Buffer
	inst := &Installer{
		GOOS:              "windows",
		Privilege:         func() error { return nil },
		ResolveExe:        func() (string, error) { return "", wantErr },
		ResolveUser:       failIfCalledResolveUser(t),
		ProvisionIdentity: failIfCalledProvision(t),
		Out:               &out,
	}
	err := inst.Run(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected resolve error, got %v", err)
	}
}

func TestRunUserResolutionFailureShortCircuits(t *testing.T) {
	wantErr := errors.New("no active console user")
	var out bytes.Buffer
	inst := &Installer{
		GOOS:              "darwin",
		Privilege:         func() error { return nil },
		ResolveExe:        func() (string, error) { return macExe, nil },
		ResolveUser:       func() (*TargetUser, error) { return nil, wantErr },
		ProvisionIdentity: failIfCalledProvision(t),
		Out:               &out,
	}
	err := inst.Run(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected user-resolution error, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output on user-resolution failure, got %q", out.String())
	}
}

func TestRunIdentityProvisionFailure(t *testing.T) {
	wantErr := errors.New("identity dir not writable")
	var out bytes.Buffer
	inst := &Installer{
		GOOS:              "darwin",
		Privilege:         func() error { return nil },
		ResolveExe:        func() (string, error) { return macExe, nil },
		ResolveUser:       stubUser(),
		ProvisionIdentity: func() (string, error) { return "", wantErr },
		Out:               &out,
	}
	if err := inst.Run(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("expected provisioning error, got %v", err)
	}
}

// realTempDir returns a temp directory with any symlinks resolved. Most tests
// use a canonical machine root so only tests specifically concerned with
// machine-directory symlinks exercise that behavior.
func realTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// tempDestinations builds a destination set rooted entirely under machineRoot
// (for machine-scoped files) so an end-to-end Run can commit real files on the
// test host without touching system paths. User-scoped files still resolve
// against the injected home. The layout mirrors the darwin production model.
func tempDestinations(machineRoot string) func(string) []Destination {
	return func(string) []Destination {
		return []Destination{
			{Agent: localagent.ClaudeCode, Label: "Claude Code", Scope: ScopeUser, Format: FormatJSON, Rel: ".claude/settings.json"},
			{Agent: localagent.Codex, Label: "Codex", Scope: ScopeMachine, Format: FormatTOML, Abs: filepath.Join(machineRoot, "etc/codex/requirements.toml")},
			{Agent: localagent.VSCode, Label: "Visual Studio Code", Scope: ScopeUser, Format: FormatJSON, Rel: ".copilot/hooks/obot-sentry.json"},
			{Agent: localagent.Cursor, Label: "Cursor", Scope: ScopeMachine, Format: FormatJSON, Abs: filepath.Join(machineRoot, "Cursor/hooks.json")},
			{Agent: localagent.VSCode, Label: "VS Code settings", Scope: ScopeUser, Format: FormatJSONC, Rel: "Library/Application Support/Code/User/settings.json"},
		}
	}
}

func TestRunConvergesAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}

	newInstaller := func(out *bytes.Buffer) *Installer {
		return &Installer{
			GOOS:                "darwin",
			Privilege:           func() error { return nil },
			ResolveExe:          func() (string, error) { return macExe, nil },
			ResolveUser:         func() (*TargetUser, error) { return user, nil },
			ProvisionIdentity:   func() (string, error) { return "/Library/Application Support/obot/obot-sentry", nil },
			ResolveDestinations: tempDestinations(machineRoot),
			Out:                 out,
		}
	}

	// First run installs every destination fresh.
	var out bytes.Buffer
	if err := newInstaller(&out).Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	got := out.String()
	mustContain := []string{
		macExe,
		"alice",
		"/Library/Application Support/obot/obot-sentry",
		"Claude Code", "Codex", "Visual Studio Code", "Cursor", "VS Code settings",
		"installed",
		restartReminder,
	}
	for _, s := range mustContain {
		if !strings.Contains(got, s) {
			t.Fatalf("summary missing %q:\n%s", s, got)
		}
	}
	if strings.Contains(got, string(StatusFailed)) {
		t.Fatalf("unexpected failure in first run:\n%s", got)
	}
	assertNoSecrets(t, got)

	files := []string{
		filepath.Join(home, ".claude/settings.json"),
		filepath.Join(machineRoot, "etc/codex/requirements.toml"),
		filepath.Join(home, ".copilot/hooks/obot-sentry.json"),
		filepath.Join(machineRoot, "Cursor/hooks.json"),
		filepath.Join(home, "Library/Application Support/Code/User/settings.json"),
	}
	before := map[string][]byte{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("expected %s written: %v", f, err)
		}
		if !strings.Contains(string(data), managedMarker) && !strings.Contains(string(data), "chat.hookFilesLocations") {
			t.Fatalf("%s missing managed content:\n%s", f, data)
		}
		before[f] = data
	}

	// Second run must report every destination unchanged and rewrite nothing.
	var out2 bytes.Buffer
	if err := newInstaller(&out2).Run(context.Background()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !strings.Contains(out2.String(), "unchanged") || strings.Contains(out2.String(), "installed") {
		t.Fatalf("second run should be all unchanged:\n%s", out2.String())
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, before[f]) {
			t.Fatalf("%s not byte-identical on second run:\n--- first ---\n%s\n--- second ---\n%s", f, before[f], data)
		}
	}
}

// TestRunAbortsOnMalformedConfigBeforeWriting proves a malformed existing
// document fails preflight without writing any file.
func TestRunAbortsOnMalformedConfigBeforeWriting(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}

	// Seed a malformed Claude settings file.
	claudePath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudePath, []byte(`{"hooks": `), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveExe:          func() (string, error) { return macExe, nil },
		ResolveUser:         func() (*TargetUser, error) { return user, nil },
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: tempDestinations(machineRoot),
		Out:                 &out,
	}
	if err := inst.Run(context.Background()); err == nil {
		t.Fatal("expected malformed config to abort the run")
	}
	// No machine file may have been written: preflight aborts before any commit.
	if _, err := os.Stat(filepath.Join(machineRoot, "etc/codex/requirements.toml")); err == nil {
		t.Fatal("Codex file was written despite a preflight abort")
	}
	if out.Len() != 0 {
		t.Fatalf("expected no summary output on preflight abort, got:\n%s", out.String())
	}
}

// TestRunCancelledContextAbortsBeforeWriting proves a cancelled context stops
// the run during preflight — before any config file is written and before the
// summary is printed — and surfaces the context error.
func TestRunCancelledContextAbortsBeforeWriting(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var out bytes.Buffer
	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveExe:          func() (string, error) { return macExe, nil },
		ResolveUser:         func() (*TargetUser, error) { return user, nil },
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: tempDestinations(machineRoot),
		Out:                 &out,
	}
	if err := inst.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(machineRoot, "etc/codex/requirements.toml")); err == nil {
		t.Fatal("a machine file was written despite a cancelled context")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude/settings.json")); err == nil {
		t.Fatal("a user file was written despite a cancelled context")
	}
	if out.Len() != 0 {
		t.Fatalf("expected no summary output on a cancelled run, got:\n%s", out.String())
	}
}

func TestFormatSummaryIsDeterministicAndSafe(t *testing.T) {
	results := []Result{
		{Agent: localagent.ClaudeCode, Label: "Claude Code", Scope: ScopeUser, Path: "/Users/x/.claude/settings.json", Status: StatusInstalled},
		{Agent: localagent.Codex, Label: "Codex", Scope: ScopeMachine, Path: "/etc/codex/requirements.toml", Status: StatusUpdated, DuplicatesRemoved: 2},
		{Agent: localagent.VSCode, Label: "Visual Studio Code", Scope: ScopeUser, Path: "/Users/x/.copilot/hooks/obot-sentry.json", Status: StatusUnchanged},
		{Agent: localagent.Cursor, Label: "Cursor", Scope: ScopeMachine, Path: "/Library/Application Support/Cursor/hooks.json", Status: StatusFailed, Err: errors.New("permission denied")},
	}

	var first bytes.Buffer
	FormatSummary(&first, macExe, results)
	var second bytes.Buffer
	FormatSummary(&second, macExe, results)
	if first.String() != second.String() {
		t.Fatalf("FormatSummary is not deterministic:\n%s\n---\n%s", first.String(), second.String())
	}

	got := first.String()
	mustContain := []string{
		macExe,
		"installed", "updated", "unchanged", "failed",
		"2 duplicates removed",
		"permission denied",
		"/etc/codex/requirements.toml",
		restartReminder,
	}
	for _, s := range mustContain {
		if !strings.Contains(got, s) {
			t.Fatalf("summary missing %q:\n%s", s, got)
		}
	}
	assertNoSecrets(t, got)
}

// assertNoSecrets guards operator-facing output against leaking deployment
// credentials or config contents.
func assertNoSecrets(t *testing.T, out string) {
	t.Helper()
	for _, bad := range []string{"EnrollmentKey", "ServerURL", "ode1-", "--enrollment-key", "--server-url", "--dry-run"} {
		if strings.Contains(out, bad) {
			t.Fatalf("output leaks %q:\n%s", bad, out)
		}
	}
}
