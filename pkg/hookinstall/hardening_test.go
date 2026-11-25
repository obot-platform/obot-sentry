package hookinstall

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckJSONDepth(t *testing.T) {
	for _, tt := range []struct {
		name string
		doc  string
		ok   bool
	}{
		{"flat", `{"a": [1, 2], "b": {"c": true}}`, true},
		{"brackets in strings", `{"a": "[[[[[[[[[[", "b": "\"[[[["}`, true},
		{"brackets in comments", "{\n// [[[[[[[[\n/* {{{{{{{{ */ \"a\": 1}", true},
		{"at the limit", strings.Repeat("[", maxJSONDepth) + strings.Repeat("]", maxJSONDepth), true},
		{"past the limit", strings.Repeat("[", maxJSONDepth+1), false},
	} {
		if err := checkJSONDepth([]byte(tt.doc), maxJSONDepth); (err == nil) != tt.ok {
			t.Errorf("%s: err = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}

// TestRunSurvivesDeeplyNestedUserConfig plants a document deep enough to
// overflow hujson's recursion (a fatal error, not a panic) in one user's hook
// file. Without the depth check this kills the test binary.
func TestRunSurvivesDeeplyNestedUserConfig(t *testing.T) {
	aliceHome, bobHome := t.TempDir(), t.TempDir()
	machineRoot := realTempDir(t)
	alice := &TargetUser{Username: "alice", HomeDir: aliceHome, UID: os.Getuid(), GID: os.Getgid()}
	bob := &TargetUser{Username: "bob", HomeDir: bobHome, UID: os.Getuid(), GID: os.Getgid()}

	planted := filepath.Join(aliceHome, ".copilot", "hooks", "obot-sentry.json")
	if err := os.MkdirAll(filepath.Dir(planted), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planted, []byte(`{"a":`+strings.Repeat("[", 700_000)), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveExe:          func() (string, error) { return macExe, nil },
		ResolveUsers:        func() (UserSet, error) { return UserSet{Users: []*TargetUser{alice, bob}}, nil },
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: tempDestinations(machineRoot),
		Out:                 &out,
	}
	if err := inst.Run(t.Context()); err == nil {
		t.Fatal("the planted file should fail its row")
	}
	if !strings.Contains(out.String(), "nested deeper than") {
		t.Fatalf("summary should report the depth refusal:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(bobHome, ".copilot/hooks/obot-sentry.json")); err != nil {
		t.Fatalf("a later user was not converged: %v\n%s", err, out.String())
	}
}

// TestRunRecoversPanicInAUserUnit proves a panic while converging one user
// fails only that user's rows instead of killing the run.
func TestRunRecoversPanicInAUserUnit(t *testing.T) {
	aliceHome, bobHome := t.TempDir(), t.TempDir()
	machineRoot := realTempDir(t)
	alice := &TargetUser{Username: "alice", HomeDir: aliceHome, UID: os.Getuid(), GID: os.Getgid()}
	bob := &TargetUser{Username: "bob", HomeDir: bobHome, UID: os.Getuid(), GID: os.Getgid()}
	orig := readConfig
	readConfig = func(scope Scope, u *TargetUser, absPath string) ([]byte, bool, error) {
		if strings.HasPrefix(absPath, aliceHome) {
			panic("boom")
		}
		return orig(scope, u, absPath)
	}
	t.Cleanup(func() { readConfig = orig })

	var out bytes.Buffer
	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveExe:          func() (string, error) { return macExe, nil },
		ResolveUsers:        func() (UserSet, error) { return UserSet{Users: []*TargetUser{alice, bob}}, nil },
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: tempDestinations(machineRoot),
		Out:                 &out,
	}
	if err := inst.Run(t.Context()); err == nil {
		t.Fatal("a panicking unit should fail the run")
	}
	if !strings.Contains(out.String(), "internal error: boom") {
		t.Fatalf("summary should report the recovered panic:\n%s", out.String())
	}
	for _, path := range []string{filepath.Join(machineRoot, "Cursor/hooks.json"), filepath.Join(bobHome, ".copilot/hooks/obot-sentry.json")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s not converged after alice's unit panicked: %v", path, err)
		}
	}
}

// TestRunMarksItselfFailedWhileInProgress proves the state records a run as
// failed while it converges, so one killed partway is retried rather than
// trusted as the previous success.
func TestRunMarksItselfFailedWhileInProgress(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	runDir := t.TempDir()
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	if err := saveScheduleState(runDir, newScheduleInputs(true, []*TargetUser{user}), now.Add(-20*time.Minute)); err != nil {
		t.Fatal(err)
	}

	var midRun *scheduleState
	orig := readConfig
	readConfig = func(scope Scope, u *TargetUser, absPath string) ([]byte, bool, error) {
		if midRun == nil {
			midRun, _ = loadScheduleState(runDir)
		}
		return orig(scope, u, absPath)
	}
	t.Cleanup(func() { readConfig = orig })

	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveExe:          func() (string, error) { return macExe, nil },
		ResolveUsers:        func() (UserSet, error) { return UserSet{Users: []*TargetUser{user}}, nil },
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: tempDestinations(machineRoot),
		RunDir:              func() (string, error) { return runDir, nil },
		Enforce:             true,
		IfDue:               true,
		Interval:            15 * time.Minute,
		Now:                 func() time.Time { return now },
		Out:                 &bytes.Buffer{},
	}
	if err := inst.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if midRun == nil || !midRun.Failed {
		t.Fatalf("state during the run = %+v, want it marked failed", midRun)
	}
	if after, _ := loadScheduleState(runDir); after == nil || after.Failed || !after.LastRunAt.Equal(now) {
		t.Fatalf("state after a successful run = %+v, want a fresh success", after)
	}
}

func TestCheckTOMLDepth(t *testing.T) {
	for _, tt := range []struct {
		name string
		doc  string
		ok   bool
	}{
		{"real shape", "[features]\nhooks = true\n\n[[hooks.PostToolUse]]\nhooks = [{ type = \"command\", command = \"x\" }]\n", true},
		{"brackets in basic and literal strings", "a = \"[[[[[[[[[[[[[[[[[[[[\"\nb = '{{{{{{{{{{{{{{{{{{{{'\nc = \"\\\"[[[[[[[[[[[[[[[[[[\"\n", true},
		{"brackets in multi-line strings", "a = \"\"\"\n[[[[[[[[[[[[[[[[[[[[\n\"\"\"\nb = '''\n{{{{{{{{{{{{{{{{{{{{\n'''\n", true},
		{"brackets in comments", "# [[[[[[[[[[[[[[[[[[[[[[[[\na = 1\n", true},
		{"quotes before a multi-line closer", "note = \"\"\"He said \"hi\"\"\"\"\nargs = [\"--allow=" + strings.Repeat("[", 20) + "x" + strings.Repeat("]", 20) + "\"]\n", true},
		{"literal quotes before a multi-line closer", "note = '''it''''\nargs = ['" + strings.Repeat("{", 20) + "']\n", true},
		{"nesting hidden behind a 4-quote closer", "x = \"\"\"q\"\"\"\"\na = " + strings.Repeat("{a=", maxTOMLDepth+1), false},
		{"nesting hidden behind a 5-quote closer", "x = '''q'''''\na = " + strings.Repeat("[", maxTOMLDepth+1), false},
		{"nesting hidden behind an escaped backslash and six quotes", "x = \"\"\"" + `\\` + "\"\"\"\"\"\"\na = " + strings.Repeat("[", maxTOMLDepth+1) + "1" + strings.Repeat("]", maxTOMLDepth+1) + "\n", false},
		{"valid file after an escaped backslash and six quotes", "x = \"\"\"" + `\\` + "\"\"\"\"\"\"\nb =\"x\"\nc = \"" + strings.Repeat("[", maxTOMLDepth+1) + "\"\n", true},
		{"at the limit", "a = " + strings.Repeat("[", maxTOMLDepth) + strings.Repeat("]", maxTOMLDepth), true},
		{"arrays past the limit", "a = " + strings.Repeat("[", maxTOMLDepth+1), false},
		{"inline tables past the limit", "a = " + strings.Repeat("{a=", maxTOMLDepth+1), false},
	} {
		if err := checkTOMLDepth([]byte(tt.doc), maxTOMLDepth); (err == nil) != tt.ok {
			t.Errorf("%s: err = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}

// TestRunRefusesDeeplyNestedCodexTOML plants nested inline tables in the
// machine-wide Codex file, which without the check exhaust memory (about 470 MB
// for 8 KB, growing with the square of the depth) or overflow the stack. The
// run must fail cleanly instead, and quickly.
func TestRunRefusesDeeplyNestedCodexTOML(t *testing.T) {
	machineRoot := realTempDir(t)
	codex := filepath.Join(machineRoot, "etc", "codex", "requirements.toml")
	if err := os.MkdirAll(filepath.Dir(codex), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codex, []byte("a = "+strings.Repeat("{a=", 20_000)), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveExe:          func() (string, error) { return macExe, nil },
		ResolveUsers:        func() (UserSet, error) { return UserSet{}, nil },
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: tempDestinations(machineRoot),
		Out:                 &out,
	}
	start := time.Now()
	err := inst.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "nested deeper than") {
		t.Fatalf("err = %v, want the TOML depth refusal", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("refusing the file took %s", elapsed)
	}
	if _, statErr := os.Stat(filepath.Join(machineRoot, "Cursor/hooks.json")); statErr == nil {
		t.Fatal("a machine-scope preflight failure still wrote files")
	}
}

// TestRunFailsWhenResolvingAUserPanicked proves a user skipped because of a
// recovered panic fails the run, so it is retried rather than recorded as a
// success.
func TestRunFailsWhenResolvingAUserPanicked(t *testing.T) {
	machineRoot := realTempDir(t)
	runDir := t.TempDir()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	if err := saveScheduleState(runDir, newScheduleInputs(false, nil), now.Add(-20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	inst := &Installer{
		GOOS:       "darwin",
		Privilege:  func() error { return nil },
		ResolveExe: func() (string, error) { return macExe, nil },
		ResolveUsers: func() (UserSet, error) {
			return UserSet{Skipped: []SkippedUser{{Name: "carol", Err: recovered("boom")}}}, nil
		},
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: tempDestinations(machineRoot),
		RunDir:              func() (string, error) { return runDir, nil },
		Now:                 func() time.Time { return now },
		Out:                 &bytes.Buffer{},
	}
	err := inst.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "resolving user carol") {
		t.Fatalf("err = %v, want the run failed for carol's internal error", err)
	}
	if _, statErr := os.Stat(filepath.Join(machineRoot, "Cursor/hooks.json")); statErr != nil {
		t.Fatalf("machine policy should still converge: %v", statErr)
	}
	if st, _ := loadScheduleState(runDir); st == nil || !st.Failed {
		t.Fatalf("state = %+v, want the run recorded as failed", st)
	}
	logData, _ := os.ReadFile(filepath.Join(runDir, RunLogName))
	if !strings.Contains(string(logData), "internal error stack") {
		t.Fatalf("the panic's stack was not logged:\n%s", logData)
	}
}

func panicLogInstaller(t *testing.T, machineRoot, runDir string, users UserSet) *Installer {
	t.Helper()
	return &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveExe:          func() (string, error) { return macExe, nil },
		ResolveUsers:        func() (UserSet, error) { return users, nil },
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: tempDestinations(machineRoot),
		RunDir:              func() (string, error) { return runDir, nil },
		Out:                 &bytes.Buffer{},
	}
}

func stackLines(t *testing.T, runDir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, RunLogName))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "internal error stack")
}

// A resolver panic on a poll that is not due still fails the run; its stack
// must reach the log even though nothing converges.
func TestNotDuePollLogsResolverPanicStack(t *testing.T) {
	runDir := t.TempDir()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	if err := saveScheduleState(runDir, newScheduleInputs(false, nil), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	inst := panicLogInstaller(t, realTempDir(t), runDir, UserSet{Skipped: []SkippedUser{{Name: "carol", Err: recovered("boom")}}})
	inst.IfDue, inst.Interval = true, 15*time.Minute
	inst.Now = func() time.Time { return now }
	if err := inst.Run(t.Context()); err == nil {
		t.Fatal("want the resolver panic reported")
	}
	if n := stackLines(t, runDir); n != 1 {
		t.Fatalf("logged %d stacks, want 1", n)
	}
}

func TestMachineUnitPanicStackIsLogged(t *testing.T) {
	machineRoot, runDir := realTempDir(t), t.TempDir()
	orig := readConfig
	readConfig = func(scope Scope, u *TargetUser, absPath string) ([]byte, bool, error) {
		if scope == ScopeMachine {
			panic("machine boom")
		}
		return orig(scope, u, absPath)
	}
	t.Cleanup(func() { readConfig = orig })
	if err := panicLogInstaller(t, machineRoot, runDir, UserSet{}).Run(t.Context()); err == nil || !strings.Contains(err.Error(), "machine boom") {
		t.Fatalf("err = %v, want the machine panic", err)
	}
	if n := stackLines(t, runDir); n != 1 {
		t.Fatalf("logged %d stacks, want 1", n)
	}
}

func TestUserUnitPanicStackIsLoggedOnce(t *testing.T) {
	machineRoot, runDir := realTempDir(t), t.TempDir()
	home := t.TempDir()
	orig := readConfig
	readConfig = func(scope Scope, u *TargetUser, absPath string) ([]byte, bool, error) {
		if strings.HasPrefix(absPath, home) {
			panic("user boom")
		}
		return orig(scope, u, absPath)
	}
	t.Cleanup(func() { readConfig = orig })
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}
	if err := panicLogInstaller(t, machineRoot, runDir, UserSet{Users: []*TargetUser{user}}).Run(t.Context()); err == nil {
		t.Fatal("want the user unit's panic to fail the run")
	}
	if n := stackLines(t, runDir); n != 1 {
		t.Fatalf("logged %d stacks for one panic across several rows, want 1", n)
	}
}
