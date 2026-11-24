package hookinstall

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testUnitTimeout is the unit deadline in these tests. It applies to every unit,
// not only the stalled one, so it must leave the others ample time: a unit
// fsyncs each file it writes, which can take hundreds of milliseconds on a
// loaded CI runner.
const testUnitTimeout = 2 * time.Second

// stallReadsUnder replaces readConfig so any read of a path under prefix blocks
// until the test ends, simulating a stalled open (a dead network home, or on
// Windows a user holding an oplock on their hook file).
func stallReadsUnder(t *testing.T, prefix string) {
	t.Helper()
	release := make(chan struct{})
	orig, origTimeout := readConfig, unitTimeout
	readConfig = func(scope Scope, u *TargetUser, absPath string) ([]byte, bool, error) {
		if strings.HasPrefix(absPath, prefix) {
			<-release
		}
		return orig(scope, u, absPath)
	}
	unitTimeout = testUnitTimeout
	t.Cleanup(func() {
		close(release)
		readConfig, unitTimeout = orig, origTimeout
	})
}

func deadlineInstaller(machineRoot string, users ...*TargetUser) (*Installer, *bytes.Buffer) {
	var out bytes.Buffer
	return &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveExe:          func() (string, error) { return macExe, nil },
		ResolveUsers:        func() (UserSet, error) { return UserSet{Users: users}, nil },
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: tempDestinations(machineRoot),
		Out:                 &out,
	}, &out
}

// TestRunAbandonsAStalledUserAndConvergesTheRest proves one user whose files
// stall is reported failed after the deadline while machine policy and every
// other user still converge.
func TestRunAbandonsAStalledUserAndConvergesTheRest(t *testing.T) {
	aliceHome, bobHome := t.TempDir(), t.TempDir()
	machineRoot := realTempDir(t)
	alice := &TargetUser{Username: "alice", HomeDir: aliceHome, UID: os.Getuid(), GID: os.Getgid()}
	bob := &TargetUser{Username: "bob", HomeDir: bobHome, UID: os.Getuid(), GID: os.Getgid()}
	stallReadsUnder(t, aliceHome)

	inst, out := deadlineInstaller(machineRoot, alice, bob)
	start := time.Now()
	if err := inst.Run(t.Context()); err == nil {
		t.Fatal("a stalled user should fail the run")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("run took %s; the stalled user was not abandoned", elapsed)
	}
	for _, path := range []string{
		filepath.Join(machineRoot, "Cursor/hooks.json"),
		filepath.Join(bobHome, ".copilot/hooks/obot-sentry.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s not converged despite only alice stalling: %v\n%s", path, err, out.String())
		}
	}
	if !strings.Contains(out.String(), "alice's hooks took longer than") {
		t.Fatalf("summary should report alice's timeout:\n%s", out.String())
	}
}

// TestRunAbandonsStalledMachineScopeAndStillConvergesUsers proves a stall on a
// machine file is reported failed after the deadline, users still converge, and
// retired user hooks are held back because their machine replacement did not
// land.
func TestRunAbandonsStalledMachineScopeAndStillConvergesUsers(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}

	// A retired Claude user-settings hook that must stay until the machine
	// replacement lands.
	old, err := mergeClaude(nil, macExe, "darwin", false)
	if err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(userFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userFile, old.data, 0o600); err != nil {
		t.Fatal(err)
	}
	stallReadsUnder(t, machineRoot)

	inst, out := deadlineInstaller(machineRoot, user)
	if err := inst.Run(t.Context()); err == nil {
		t.Fatal("a stalled machine scope should fail the run")
	}
	if !strings.Contains(out.String(), "machine-wide hooks took longer than") {
		t.Fatalf("summary should report the machine timeout:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".copilot/hooks/obot-sentry.json")); err != nil {
		t.Fatalf("user hooks not converged after the machine scope stalled: %v\n%s", err, out.String())
	}
	if got, err := os.ReadFile(userFile); err != nil || !bytes.Equal(got, old.data) {
		t.Fatalf("retired user hooks were removed although their machine replacement never landed: err=%v\n%s", err, out.String())
	}
}

// TestAbandonedUnitDoesNotWriteAfterDeadline proves a unit whose pre-commit
// re-read stalls past the deadline does not write once it resumes: it was
// already reported failed and the run moved on.
func TestAbandonedUnitDoesNotWriteAfterDeadline(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}
	target := filepath.Join(home, ".copilot", "hooks", "obot-sentry.json")

	orig, origTimeout := readConfig, unitTimeout
	unitTimeout = testUnitTimeout
	resumed := make(chan struct{})
	reads := 0
	readConfig = func(scope Scope, u *TargetUser, absPath string) ([]byte, bool, error) {
		if absPath == target {
			reads++
			if reads == 2 { // the re-read just before commit
				time.Sleep(unitTimeout + time.Second)
				defer close(resumed)
			}
		}
		return orig(scope, u, absPath)
	}
	t.Cleanup(func() { readConfig, unitTimeout = orig, origTimeout })

	inst, out := deadlineInstaller(machineRoot, user)
	if err := inst.Run(t.Context()); err == nil {
		t.Fatalf("the abandoned unit should fail the run:\n%s", out.String())
	}
	select {
	case <-resumed:
	case <-time.After(10 * time.Second):
		t.Fatal("the stalled re-read never resumed")
	}
	time.Sleep(500 * time.Millisecond) // let the abandoned unit finish its commit attempt
	if _, err := os.Stat(target); err == nil {
		t.Fatal("an abandoned unit wrote its file after the deadline")
	}
}

func TestLogErrTruncates(t *testing.T) {
	long := errors.New(strings.Repeat("k", 4<<20))
	got := logErr(long)
	if len(got) > maxLoggedErrBytes+64 || !strings.Contains(got, "bytes truncated") {
		t.Fatalf("logErr kept %d bytes; want at most about %d with a truncation note", len(got), maxLoggedErrBytes)
	}
	if logErr(errors.New("short")) != "short" || logErr(nil) != "" {
		t.Fatal("logErr altered a short or nil error")
	}
}
