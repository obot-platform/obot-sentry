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
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := decideDue(tt.state, tt.inputs, interval, now, tt.boot)
			if got.Due != tt.want {
				t.Fatalf("decideDue = %+v, want due=%v", got, tt.want)
			}
			if got.Reason == "" {
				t.Fatal("decision has no reason")
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
