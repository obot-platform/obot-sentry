package hookinstall

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseDsclUserList(t *testing.T) {
	out := []byte(`_amavisd                 83
_appleevents             55
daemon                   1
nobody                   -2
root                     0
Guest                    201
alice                    501
bob                      502
malformed
carol                    notanumber
`)
	got := parseDsclUserList(out)
	want := []dsclAccount{{Name: "alice", UID: "501"}, {Name: "bob", UID: "502"}}
	if !slices.Equal(got, want) {
		t.Fatalf("parseDsclUserList = %+v, want %+v", got, want)
	}
}

func TestIsUserProfileSID(t *testing.T) {
	for sid, want := range map[string]bool{
		"S-1-5-18": false,
		"S-1-5-19": false,
		"S-1-5-20": false,
		"S-1-5-21-1004336348-1177238915-682003330-1001":     true,
		"S-1-5-21-1004336348-1177238915-682003330-1001.bak": false,
		"S-1-12-1-1234567890-1234567890-1234567890-123":     true,
		"S-1-5-80-0": false,
	} {
		if got := isUserProfileSID(sid); got != want {
			t.Errorf("isUserProfileSID(%q) = %v, want %v", sid, got, want)
		}
	}
}

func TestDecideDue(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	boot := now.Add(-24 * time.Hour)
	inputs := scheduleInputs{Enforce: true, Homes: []string{"/Users/alice", "/Users/bob"}}
	state := func(ago time.Duration) *scheduleState {
		return &scheduleState{LastRunAt: now.Add(-ago), Enforce: true, Homes: []string{"/Users/alice", "/Users/bob"}}
	}
	interval := 15 * time.Minute

	for _, tt := range []struct {
		name   string
		state  *scheduleState
		inputs scheduleInputs
		boot   time.Time
		want   bool
		reason string
	}{
		{name: "no state", state: nil, inputs: inputs, boot: boot, want: true},
		{name: "recent run", state: state(5 * time.Minute), inputs: inputs, boot: boot, want: false},
		{name: "interval elapsed", state: state(15 * time.Minute), inputs: inputs, boot: boot, want: true},
		{name: "a poll a few seconds early still counts", state: state(14*time.Minute + 50*time.Second), inputs: inputs, boot: boot, want: true},
		{name: "rebooted since last run", state: state(5 * time.Minute), inputs: inputs, boot: now.Add(-time.Minute), want: true},
		{name: "unknown boot time", state: state(5 * time.Minute), inputs: inputs, boot: time.Time{}, want: false},
		{name: "future-dated state", state: state(-time.Hour), inputs: inputs, boot: boot, want: true},
		{name: "upgraded binary", state: state(5 * time.Minute), inputs: scheduleInputs{Version: "v9.9.9", Enforce: true, Homes: inputs.Homes}, boot: boot, want: true},
		{name: "enforcement changed", state: state(5 * time.Minute), inputs: scheduleInputs{Enforce: false, Homes: inputs.Homes}, boot: boot, want: true},
		{name: "new user profile", state: state(5 * time.Minute), inputs: scheduleInputs{Enforce: true, Homes: []string{"/Users/alice", "/Users/bob", "/Users/carol"}}, boot: boot, want: true},
		{name: "listing failed mid-interval", state: state(5 * time.Minute), inputs: scheduleInputs{Enforce: true, HomesUnknown: true}, boot: boot, want: false},
		{name: "listing failed after the interval", state: state(20 * time.Minute), inputs: scheduleInputs{Enforce: true, HomesUnknown: true}, boot: boot, want: true, reason: "interval elapsed"},
		{name: "previous run failed", state: &scheduleState{LastRunAt: now.Add(-5 * time.Minute), Enforce: true, Homes: inputs.Homes, Failed: true}, inputs: inputs, boot: boot, want: true, reason: "previous run failed"},
		{name: "input change after a failed run names the change", state: &scheduleState{LastRunAt: now.Add(-5 * time.Minute), Enforce: false, Homes: inputs.Homes, Failed: true}, inputs: inputs, boot: boot, want: true, reason: "enforcement setting changed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := decideDue(tt.state, tt.inputs, interval, now, tt.boot)
			if got.Due != tt.want {
				t.Fatalf("decideDue = %+v, want due=%v", got, tt.want)
			}
			if got.Reason == "" {
				t.Fatal("decision has no reason")
			}
			if tt.reason != "" && got.Reason != tt.reason {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.reason)
			}
		})
	}
}

func TestScheduleStateRoundTripAndCorruption(t *testing.T) {
	dir := t.TempDir()
	if st, err := loadScheduleState(dir); st != nil || err != nil {
		t.Fatalf("absent state = %+v, %v; want nil, nil", st, err)
	}

	started := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	inputs := newScheduleInputs(true, []*TargetUser{{HomeDir: "/b"}, {HomeDir: "/a"}})
	if err := saveScheduleState(dir, inputs, started); err != nil {
		t.Fatal(err)
	}
	st, err := loadScheduleState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.LastRunAt.Equal(started) || !st.Enforce || !slices.Equal(st.Homes, []string{"/a", "/b"}) {
		t.Fatalf("round-tripped state = %+v", st)
	}

	if err := os.WriteFile(filepath.Join(dir, scheduleStateName), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, err := loadScheduleState(dir); st != nil || err == nil {
		t.Fatalf("corrupt state = %+v, %v; want nil and an error", st, err)
	}
}

func TestOpenRunLogAppendsAndRotates(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, RunLogName)

	for range 2 {
		log, closer, err := openRunLog(dir)
		if err != nil {
			t.Fatal(err)
		}
		log.Info("hook destination changed", "agent", "Kiro")
		_ = closer.Close()
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "hook destination changed"); n != 2 {
		t.Fatalf("log has %d entries, want 2 (append, not truncate):\n%s", n, data)
	}

	// Grow past the cap: the next open rotates it aside and starts fresh.
	if err := os.WriteFile(logPath, bytes.Repeat([]byte("x"), maxRunLogBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	_, closer, err := openRunLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = closer.Close()
	if info, err := os.Stat(logPath + ".1"); err != nil || info.Size() != maxRunLogBytes {
		t.Fatalf("rotated log missing or wrong size: %v", err)
	}
	if info, err := os.Stat(logPath); err != nil || info.Size() != 0 {
		t.Fatalf("fresh log missing or not empty: %v", err)
	}
}

func TestRunIfDueThrottlesAndLogs(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	runDir := t.TempDir()
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	newInstaller := func(out *bytes.Buffer) *Installer {
		return &Installer{
			GOOS:                "darwin",
			Privilege:           func() error { return nil },
			ResolveExe:          func() (string, error) { return macExe, nil },
			ResolveUsers:        func() (UserSet, error) { return UserSet{Users: []*TargetUser{user}}, nil },
			ProvisionIdentity:   func() (string, error) { return "/id", nil },
			ResolveDestinations: tempDestinations(machineRoot),
			RunDir:              func() (string, error) { return runDir, nil },
			IfDue:               true,
			Interval:            15 * time.Minute,
			Now:                 func() time.Time { return now },
			BootTime:            func() (time.Time, error) { return now.Add(-time.Hour), nil },
			Out:                 out,
		}
	}

	var first bytes.Buffer
	if err := newInstaller(&first).Run(t.Context()); err != nil {
		t.Fatalf("first run: %v\n%s", err, first.String())
	}
	if !strings.Contains(first.String(), "installed") {
		t.Fatalf("first run did not converge:\n%s", first.String())
	}

	// Remove a hook file: within the interval the drift is not yet repaired.
	vscodeHook := filepath.Join(home, ".copilot/hooks/obot-sentry.json")
	if err := os.Remove(vscodeHook); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	var second bytes.Buffer
	if err := newInstaller(&second).Run(t.Context()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !strings.Contains(second.String(), "not due") {
		t.Fatalf("second run should have been skipped:\n%s", second.String())
	}
	if _, err := os.Stat(vscodeHook); err == nil {
		t.Fatal("a skipped run wrote a hook file")
	}

	now = now.Add(10 * time.Minute)
	var third bytes.Buffer
	if err := newInstaller(&third).Run(t.Context()); err != nil {
		t.Fatalf("third run: %v", err)
	}
	if _, err := os.Stat(vscodeHook); err != nil {
		t.Fatalf("drift was not repaired after the interval: %v", err)
	}

	logData, err := os.ReadFile(filepath.Join(runDir, RunLogName))
	if err != nil {
		t.Fatal(err)
	}
	log := string(logData)
	if n := strings.Count(log, "hook convergence finished"); n != 2 {
		t.Fatalf("log has %d convergences, want 2 (the skip is not logged):\n%s", n, log)
	}
	if !strings.Contains(log, "status=installed") || !strings.Contains(log, "user=alice") || !strings.Contains(log, `reason="interval elapsed"`) {
		t.Fatalf("log is missing the repair details:\n%s", log)
	}
}

func TestRunWithNoUsersConvergesMachineOnly(t *testing.T) {
	machineRoot := realTempDir(t)
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
	if err := inst.Run(t.Context()); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(machineRoot, "Cursor/hooks.json")); err != nil {
		t.Fatalf("machine hooks not converged without users: %v", err)
	}
	if !strings.Contains(out.String(), "none found") {
		t.Fatalf("summary should say no users were found:\n%s", out.String())
	}
}

func TestRunIfDueRetriesFailedRunOnNextPoll(t *testing.T) {
	machineRoot := realTempDir(t)
	runDir := t.TempDir()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	exeErr := errors.New("executable mid-upgrade")

	newInstaller := func(resolveExe func() (string, error)) *Installer {
		return &Installer{
			GOOS:                "darwin",
			Privilege:           func() error { return nil },
			ResolveExe:          resolveExe,
			ResolveUsers:        func() (UserSet, error) { return UserSet{}, nil },
			ProvisionIdentity:   func() (string, error) { return "/id", nil },
			ResolveDestinations: tempDestinations(machineRoot),
			RunDir:              func() (string, error) { return runDir, nil },
			IfDue:               true,
			Interval:            15 * time.Minute,
			Now:                 func() time.Time { return now },
			BootTime:            func() (time.Time, error) { return now.Add(-time.Hour), nil },
			Out:                 &bytes.Buffer{},
		}
	}

	if err := newInstaller(func() (string, error) { return "", exeErr }).Run(t.Context()); !errors.Is(err, exeErr) {
		t.Fatalf("first run err = %v, want %v", err, exeErr)
	}
	if st, _ := loadScheduleState(runDir); st != nil {
		t.Fatalf("a failed run recorded state: %+v", st)
	}

	now = now.Add(5 * time.Minute)
	if err := newInstaller(func() (string, error) { return macExe, nil }).Run(t.Context()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(machineRoot, "Cursor/hooks.json")); err != nil {
		t.Fatalf("the retry on the next poll did not converge: %v", err)
	}
	if st, _ := loadScheduleState(runDir); st == nil || !st.LastRunAt.Equal(now) {
		t.Fatalf("the successful retry did not record state: %+v", st)
	}
}

func TestUninstallClearsScheduleState(t *testing.T) {
	runDir := t.TempDir()
	if err := saveScheduleState(runDir, scheduleInputs{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveUsers:        func() (UserSet, error) { return UserSet{}, nil },
		ResolveDestinations: tempDestinations(realTempDir(t)),
		RunDir:              func() (string, error) { return runDir, nil },
		Uninstall:           true,
		Out:                 &bytes.Buffer{},
	}
	if err := inst.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st, err := loadScheduleState(runDir); st != nil || err != nil {
		t.Fatalf("schedule state after uninstall = %+v, %v; want none", st, err)
	}
}

func TestRunIfDueListingFailureIsNotAProfileChange(t *testing.T) {
	machineRoot := realTempDir(t)
	runDir := t.TempDir()
	listErr := errors.New("dscl failed")
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	inputs := newScheduleInputs(false, []*TargetUser{{HomeDir: "/Users/alice"}})

	newInstaller := func() (*Installer, *bytes.Buffer) {
		var out bytes.Buffer
		return &Installer{
			GOOS:                "darwin",
			Privilege:           func() error { return nil },
			ResolveExe:          func() (string, error) { return macExe, nil },
			ResolveUsers:        func() (UserSet, error) { return UserSet{}, listErr },
			ProvisionIdentity:   func() (string, error) { return "/id", nil },
			ResolveDestinations: tempDestinations(machineRoot),
			RunDir:              func() (string, error) { return runDir, nil },
			IfDue:               true,
			Interval:            15 * time.Minute,
			Now:                 func() time.Time { return now },
			BootTime:            func() (time.Time, error) { return now.Add(-time.Hour), nil },
			Out:                 &out,
		}, &out
	}

	// Mid-interval: not due, nothing converged, but the failure is still reported.
	if err := saveScheduleState(runDir, inputs, now.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	inst, out := newInstaller()
	if err := inst.Run(t.Context()); !errors.Is(err, listErr) {
		t.Fatalf("err = %v, want the listing error", err)
	}
	if !strings.Contains(out.String(), "not due") {
		t.Fatalf("a listing failure mid-interval made the run due:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(machineRoot, "Cursor/hooks.json")); err == nil {
		t.Fatal("a not-due run converged machine policy")
	}

	// After the interval: machine policy converges for the right reason, and
	// nothing is recorded, so the next poll retries.
	if err := saveScheduleState(runDir, inputs, now.Add(-20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	inst, out = newInstaller()
	if err := inst.Run(t.Context()); !errors.Is(err, listErr) {
		t.Fatalf("err = %v, want the listing error", err)
	}
	if _, err := os.Stat(filepath.Join(machineRoot, "Cursor/hooks.json")); err != nil {
		t.Fatalf("machine policy not converged once due: %v\n%s", err, out.String())
	}
	logData, err := os.ReadFile(filepath.Join(runDir, RunLogName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), `reason="interval elapsed"`) || strings.Contains(string(logData), "user profiles changed") {
		t.Fatalf("log should give the interval, not a profile change, as the reason:\n%s", logData)
	}
	if st, _ := loadScheduleState(runDir); st == nil || !st.LastRunAt.Equal(now.Add(-20*time.Minute)) || !st.Failed || !slices.Equal(st.Homes, inputs.Homes) {
		t.Fatalf("a run whose listing failed should keep the last success's time and homes and be marked failed: %+v", st)
	}
}

// enforcementHooks counts obot-sentry enforce commands in the machine-scoped
// Claude Code file of a tempDestinations tree.
func enforcementHooks(t *testing.T, machineRoot string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(machineRoot, "ClaudeCode/managed-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "obot-sentry enforce")
}

// TestRunIfDueComparesAgainstAFailedRunsInputs: a failed run still rewrites
// machine policy with its inputs. If the next poll compared against the last
// successful run instead, turning enforcement back on mid-interval would be
// "not due" and the enforcement hooks the failed run removed would stay gone.
func TestRunIfDueComparesAgainstAFailedRunsInputs(t *testing.T) {
	machineRoot := realTempDir(t)
	runDir := t.TempDir()
	listErr := errors.New("dscl failed")
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	newInstaller := func(enforce bool, listing error) *Installer {
		return &Installer{
			GOOS:                "darwin",
			Privilege:           func() error { return nil },
			ResolveExe:          func() (string, error) { return macExe, nil },
			ResolveUsers:        func() (UserSet, error) { return UserSet{}, listing },
			ProvisionIdentity:   func() (string, error) { return "/id", nil },
			ResolveDestinations: tempDestinations(machineRoot),
			RunDir:              func() (string, error) { return runDir, nil },
			Enforce:             enforce,
			IfDue:               true,
			Interval:            15 * time.Minute,
			Now:                 func() time.Time { return now },
			BootTime:            func() (time.Time, error) { return now.Add(-time.Hour), nil },
			Out:                 &bytes.Buffer{},
		}
	}

	if err := newInstaller(true, nil).Run(t.Context()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if n := enforcementHooks(t, machineRoot); n == 0 {
		t.Fatal("first run installed no enforcement hooks")
	}
	succeededAt := now

	// Enforcement is turned off, and that run fails after converging machine
	// policy without the enforcement hooks.
	now = now.Add(time.Minute)
	if err := newInstaller(false, listErr).Run(t.Context()); !errors.Is(err, listErr) {
		t.Fatalf("failing run err = %v, want the listing error", err)
	}
	if n := enforcementHooks(t, machineRoot); n != 0 {
		t.Fatalf("the failing run left %d enforcement hooks; the scenario needs it to remove them", n)
	}
	st, _ := loadScheduleState(runDir)
	if st == nil || !st.Failed || st.Enforce || !st.LastRunAt.Equal(succeededAt) {
		t.Fatalf("failed run state = %+v, want its inputs (enforce=false), failed, and the last success's time", st)
	}

	// Enforcement is turned back on mid-interval: due, and the hooks return.
	now = now.Add(time.Minute)
	if err := newInstaller(true, nil).Run(t.Context()); err != nil {
		t.Fatalf("re-enable run: %v", err)
	}
	if n := enforcementHooks(t, machineRoot); n == 0 {
		t.Fatal("re-enabling enforcement mid-interval did not restore the enforcement hooks")
	}
	if st, _ := loadScheduleState(runDir); st == nil || st.Failed || !st.Enforce || !st.LastRunAt.Equal(now) {
		t.Fatalf("successful run state = %+v, want enforce=true, not failed, recorded now", st)
	}
}

// TestRunIfDueRetriesAFailedRunMidInterval: a failure marks the state, so the
// next poll retries even when no input changed and the interval has not
// elapsed.
func TestRunIfDueRetriesAFailedRunMidInterval(t *testing.T) {
	machineRoot := realTempDir(t)
	runDir := t.TempDir()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	exeErr := errors.New("executable mid-upgrade")
	newInstaller := func(resolveExe func() (string, error)) (*Installer, *bytes.Buffer) {
		var out bytes.Buffer
		return &Installer{
			GOOS:                "darwin",
			Privilege:           func() error { return nil },
			ResolveExe:          resolveExe,
			ResolveUsers:        func() (UserSet, error) { return UserSet{}, nil },
			ProvisionIdentity:   func() (string, error) { return "/id", nil },
			ResolveDestinations: tempDestinations(machineRoot),
			RunDir:              func() (string, error) { return runDir, nil },
			IfDue:               true,
			Interval:            15 * time.Minute,
			Now:                 func() time.Time { return now },
			BootTime:            func() (time.Time, error) { return now.Add(-time.Hour), nil },
			Out:                 &out,
		}, &out
	}

	inst, _ := newInstaller(func() (string, error) { return macExe, nil })
	if err := inst.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(machineRoot, "Cursor/hooks.json")); err != nil {
		t.Fatal(err)
	}

	// An input change makes this poll due, and the run fails.
	if err := saveScheduleState(runDir, scheduleInputs{Version: "v0.0.0-old"}, now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	inst, _ = newInstaller(func() (string, error) { return "", exeErr })
	if err := inst.Run(t.Context()); !errors.Is(err, exeErr) {
		t.Fatalf("err = %v, want %v", err, exeErr)
	}

	now = now.Add(5 * time.Minute)
	inst, out := newInstaller(func() (string, error) { return macExe, nil })
	if err := inst.Run(t.Context()); err != nil {
		t.Fatalf("retry: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "not due") {
		t.Fatalf("the poll after a failed run was not due:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(machineRoot, "Cursor/hooks.json")); err != nil {
		t.Fatalf("the retry did not converge: %v", err)
	}
	logData, err := os.ReadFile(filepath.Join(runDir, RunLogName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), `reason="previous run failed"`) {
		t.Fatalf("log should give the failed run as the reason:\n%s", logData)
	}
}

// TestManualRunRecordsItsInputs: a plain hook-install rewrites the same files
// as a scheduled run, so the next poll must compare against what it wrote. A
// manual --enforce=false followed by a poll with enforcement on is due.
func TestManualRunRecordsItsInputs(t *testing.T) {
	machineRoot := realTempDir(t)
	runDir := t.TempDir()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	newInstaller := func(enforce, ifDue bool) (*Installer, *bytes.Buffer) {
		var out bytes.Buffer
		return &Installer{
			GOOS:                "darwin",
			Privilege:           func() error { return nil },
			ResolveExe:          func() (string, error) { return macExe, nil },
			ResolveUsers:        func() (UserSet, error) { return UserSet{}, nil },
			ProvisionIdentity:   func() (string, error) { return "/id", nil },
			ResolveDestinations: tempDestinations(machineRoot),
			RunDir:              func() (string, error) { return runDir, nil },
			Enforce:             enforce,
			IfDue:               ifDue,
			Interval:            15 * time.Minute,
			Now:                 func() time.Time { return now },
			BootTime:            func() (time.Time, error) { return now.Add(-time.Hour), nil },
			Out:                 &out,
		}, &out
	}

	inst, _ := newInstaller(true, true)
	if err := inst.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	inst, _ = newInstaller(false, false)
	if err := inst.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st, _ := loadScheduleState(runDir); st == nil || st.Enforce || !st.LastRunAt.Equal(now) {
		t.Fatalf("manual run state = %+v, want enforce=false recorded now", st)
	}
	now = now.Add(time.Minute)
	inst, out := newInstaller(true, true)
	if err := inst.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "not due") || enforcementHooks(t, machineRoot) == 0 {
		t.Fatalf("enforcement on after a manual --enforce=false was not converged:\n%s", out.String())
	}
}
