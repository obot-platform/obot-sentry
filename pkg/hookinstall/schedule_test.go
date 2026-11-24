package hookinstall

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScheduleDueReason(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	booted := now.Add(-time.Hour)
	base := runRecord{
		LastRunAt:  now.Add(-5 * time.Minute),
		Version:    "v1",
		Executable: macExe,
		Enforce:    false,
		Users:      []string{"/Users/alice"},
	}
	for _, tt := range []struct {
		name   string
		prev   func(r runRecord) runRecord
		next   func(r runRecord) runRecord
		booted time.Time
		want   string
	}{
		{name: "recent and unchanged", want: ""},
		{name: "interval elapsed", prev: func(r runRecord) runRecord { r.LastRunAt = now.Add(-15 * time.Minute); return r }, want: "interval elapsed"},
		{name: "a tick just short of the interval", prev: func(r runRecord) runRecord { r.LastRunAt = now.Add(-15*time.Minute + 100*time.Millisecond); return r }, want: "interval elapsed"},
		{name: "a tick early", prev: func(r runRecord) runRecord { r.LastRunAt = now.Add(-10 * time.Minute); return r }, want: ""},
		{name: "previous check failed", prev: func(r runRecord) runRecord { r.Failed = true; return r }, want: "previous check failed"},
		{name: "third failure backs off to 20 minutes", prev: func(r runRecord) runRecord { r.Failures = 3; r.LastRunAt = now.Add(-10 * time.Minute); return r }, want: ""},
		{name: "third failure retried after 20 minutes", prev: func(r runRecord) runRecord { r.Failures = 3; r.LastRunAt = now.Add(-20 * time.Minute); return r }, want: "previous check failed"},
		{name: "future record", prev: func(r runRecord) runRecord { r.LastRunAt = now.Add(time.Minute); return r }, want: "previous check is in the future"},
		{name: "upgrade", next: func(r runRecord) runRecord { r.Version = "v2"; return r }, want: "obot-sentry version changed"},
		{name: "moved executable", next: func(r runRecord) runRecord { r.Executable = "/opt/obot-sentry"; return r }, want: "executable changed"},
		{name: "enforcement toggled", next: func(r runRecord) runRecord { r.Enforce = true; return r }, want: "enforcement setting changed"},
		{name: "new user", next: func(r runRecord) runRecord { r.Users = []string{"/Users/alice", "/Users/bob"}; return r }, want: "users changed"},
		{name: "restarted since", booted: now.Add(-time.Minute), want: "system restarted"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			prev, next := base, base
			if tt.prev != nil {
				prev = tt.prev(prev)
			}
			if tt.next != nil {
				next = tt.next(next)
			}
			bootAt := booted
			if !tt.booted.IsZero() {
				bootAt = tt.booted
			}
			interval := 15 * time.Minute
			if strings.Contains(tt.name, "third failure") {
				interval = time.Hour
			}
			s := &Schedule{Interval: interval, BootTime: func() (time.Time, error) { return bootAt, nil }}
			if got := s.dueReason(prev, next, now); got != tt.want {
				t.Fatalf("dueReason = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestScheduleBootTimeErrorIsNotARestart(t *testing.T) {
	now := time.Now()
	r := runRecord{LastRunAt: now.Add(-time.Minute)}
	s := &Schedule{Interval: time.Hour, BootTime: func() (time.Time, error) { return time.Time{}, errors.New("unknown") }}
	if got := s.dueReason(r, r, now); got != "" {
		t.Fatalf("dueReason = %q, want not due", got)
	}
}

// TestRunWithScheduleSkipsUntilDue drives Run as the OS scheduler does: the first
// tick converges and records, the next one within the interval does nothing, a
// tick after the interval converges again and repairs a removed hook.
func TestRunWithScheduleSkipsUntilDue(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	statePath := filepath.Join(t.TempDir(), "hook-install", "state.json")
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}
	now := time.Now()

	var logBuf bytes.Buffer
	run := func(at time.Time) (string, error) {
		var out bytes.Buffer
		inst := &Installer{
			GOOS:                "darwin",
			Privilege:           func() error { return nil },
			ResolveExe:          func() (string, error) { return macExe, nil },
			ResolveUsers:        func() (UserList, error) { return UserList{Users: []*TargetUser{user}}, nil },
			ProvisionIdentity:   func() (string, error) { return "/id", nil },
			ResolveDestinations: tempDestinations(machineRoot),
			Out:                 &out,
			Log:                 slog.New(slog.NewTextHandler(&logBuf, nil)),
			Schedule: &Schedule{
				Interval:  15 * time.Minute,
				StatePath: statePath,
				Version:   "v1",
				Now:       func() time.Time { return at },
				BootTime:  func() (time.Time, error) { return now.Add(-24 * time.Hour), nil },
			},
		}
		err := inst.Run(context.Background())
		return out.String(), err
	}

	if out, err := run(now); err != nil || !strings.Contains(out, "installed") {
		t.Fatalf("first tick: err=%v\n%s", err, out)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("first tick did not record the check: %v", err)
	}

	cursor := filepath.Join(machineRoot, "Cursor/hooks.json")
	if err := os.Remove(cursor); err != nil {
		t.Fatal(err)
	}
	if out, err := run(now.Add(5 * time.Minute)); err != nil || out != "" {
		t.Fatalf("tick within the interval ran: err=%v\n%s", err, out)
	}
	if _, err := os.Stat(cursor); !os.IsNotExist(err) {
		t.Fatal("tick within the interval repaired a hook")
	}

	logBuf.Reset()
	out, err := run(now.Add(16 * time.Minute))
	if err != nil {
		t.Fatalf("tick after the interval: %v\n%s", err, out)
	}
	if _, err := os.Stat(cursor); err != nil {
		t.Fatalf("removed Cursor hooks were not restored: %v", err)
	}
	log := logBuf.String()
	for _, want := range []string{`msg="hook installed"`, "agent=Cursor", `reason="interval elapsed"`, "installed=1"} {
		if !strings.Contains(log, want) {
			t.Fatalf("run log missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "agent=Codex") {
		t.Fatalf("run log lists a destination that did not change:\n%s", log)
	}
}

func TestRunWithScheduleIgnoresCorruptRecord(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(statePath, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Schedule{Interval: time.Hour, StatePath: statePath}
	var logBuf bytes.Buffer
	rec, due := s.check(Plan{}, slog.New(slog.NewTextHandler(&logBuf, nil)))
	if !due || rec.Reason != "no previous check" {
		t.Fatalf("check = (%+v, %v), want due with no previous check", rec, due)
	}
	if !strings.Contains(logBuf.String(), "ignoring hook check record") {
		t.Fatalf("corrupt record not reported:\n%s", logBuf.String())
	}
}

// TestRunWithScheduleRetriesAFailedCheck proves a check that failed is due again
// on the next tick instead of waiting out the interval, and that once it
// succeeds the interval applies again.
func TestRunWithScheduleRetriesAFailedCheck(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	statePath := filepath.Join(t.TempDir(), "hook-install", "state.json")
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}
	copilot := filepath.Join(home, ".copilot", "hooks", "obot-sentry.json")
	if err := os.MkdirAll(filepath.Dir(copilot), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copilot, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	run := func(at time.Time) (string, error) {
		var out bytes.Buffer
		err := (&Installer{
			GOOS:                "darwin",
			Privilege:           func() error { return nil },
			ResolveExe:          func() (string, error) { return macExe, nil },
			ResolveUsers:        func() (UserList, error) { return UserList{Users: []*TargetUser{user}}, nil },
			ProvisionIdentity:   func() (string, error) { return "/id", nil },
			ResolveDestinations: tempDestinations(machineRoot),
			Out:                 &out,
			Schedule: &Schedule{
				Interval: time.Hour, StatePath: statePath, Version: "v1",
				Now:      func() time.Time { return at },
				BootTime: func() (time.Time, error) { return now.Add(-24 * time.Hour), nil },
			},
		}).Run(context.Background())
		return out.String(), err
	}
	if _, err := run(now); err == nil {
		t.Fatal("expected the malformed file to fail the first check")
	}
	if err := os.Remove(copilot); err != nil {
		t.Fatal(err)
	}
	if out, err := run(now.Add(5 * time.Minute)); err != nil || out == "" {
		t.Fatalf("the tick after a failed check did not retry: err=%v\n%s", err, out)
	}
	if _, err := os.Stat(copilot); err != nil {
		t.Fatalf("the retry did not restore the hook: %v", err)
	}
	if out, err := run(now.Add(10 * time.Minute)); err != nil || out != "" {
		t.Fatalf("a tick after a successful check ran: err=%v\n%s", err, out)
	}
}

func TestRetryDelay(t *testing.T) {
	s := &Schedule{Interval: 30 * time.Minute}
	for failures, want := range map[int]time.Duration{1: 5 * time.Minute, 2: 10 * time.Minute, 3: 20 * time.Minute, 4: 30 * time.Minute, 9: 30 * time.Minute} {
		if got := s.retryDelay(failures); got != want {
			t.Errorf("retryDelay(%d) = %v, want %v", failures, got, want)
		}
	}
}

// TestRunWithScheduleIgnoresRetiredFailures proves a failure in a retired
// destination — here a user's own unparsable ~/.claude/settings.json — does not
// make the next tick due: nothing there needs repairing.
func TestRunWithScheduleIgnoresRetiredFailures(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	statePath := filepath.Join(t.TempDir(), "hook-install", "state.json")
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}
	claude := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claude), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claude, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	run := func(at time.Time) (string, error) {
		var out bytes.Buffer
		err := (&Installer{
			GOOS:                "darwin",
			Privilege:           func() error { return nil },
			ResolveExe:          func() (string, error) { return macExe, nil },
			ResolveUsers:        func() (UserList, error) { return UserList{Users: []*TargetUser{user}}, nil },
			ProvisionIdentity:   func() (string, error) { return "/id", nil },
			ResolveDestinations: tempDestinations(machineRoot),
			Out:                 &out,
			Schedule: &Schedule{
				Interval: time.Hour, StatePath: statePath, Version: "v1",
				Now:      func() time.Time { return at },
				BootTime: func() (time.Time, error) { return now.Add(-24 * time.Hour), nil },
			},
		}).Run(context.Background())
		return out.String(), err
	}
	// Reported in the summary, but not a failed run: no hook is missing.
	if out, err := run(now); err != nil || !strings.Contains(out, string(StatusFailed)) {
		t.Fatalf("first check: err=%v, want success with the retired file reported failed\n%s", err, out)
	}
	if out, _ := run(now.Add(6 * time.Minute)); out != "" {
		t.Fatalf("a retired-only failure made the next tick due:\n%s", out)
	}
}
