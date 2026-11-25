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
