package hookinstall

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

func TestLogRunRecordsChangesAndFailures(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	plan := Plan{Enforce: true, Users: []*TargetUser{{Username: "alice"}}}
	logRun(context.Background(), log, "hook-install", "", plan,
		[]SkippedUser{{Username: "bob", Reason: "not set up"}},
		[]Result{
			{Agent: localagent.Codex, Label: "Codex", Path: "/etc/codex/requirements.toml", Status: StatusUnchanged},
			{Agent: localagent.Cursor, Label: "Cursor", Path: "/c/hooks.json", Status: StatusUpdated, DuplicatesRemoved: 1},
			{Agent: localagent.Kiro, Label: "Kiro", User: "alice", Path: "/Users/alice/.kiro/hooks/obot-sentry.json", Status: StatusFailed, Err: errors.New("bad json")},
		})
	got := buf.String()
	for _, want := range []string{
		`msg="user skipped"`, "user=bob",
		`msg="hook repaired"`, "agent=Cursor", "duplicates_removed=1",
		`level=ERROR msg="hook check failed"`, "user=alice", `error="bad json"`,
		`level=ERROR msg="hook check complete"`, "reason=requested", "repaired=1", "failed=1", "unchanged=1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "agent=Codex") {
		t.Fatalf("unchanged destination was logged:\n%s", got)
	}
	assertNoSecrets(t, got)
}

func TestOpenRunLogRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "hook-install.log")
	f, err := openRunLogAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(bytes.Repeat([]byte("x"), maxRunLogSize)); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	f, err = openRunLogAt(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(f, "fresh\n")
	_ = f.Close()

	if got, err := os.ReadFile(path); err != nil || string(got) != "fresh\n" {
		t.Fatalf("log after rotation = %q (err=%v)", got, err)
	}
	if info, err := os.Stat(path + ".1"); err != nil || info.Size() != maxRunLogSize {
		t.Fatalf("rotated log missing or wrong size: %v", err)
	}
}

func TestOpenRunLogRefusesSharedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := openRunLogAt(filepath.Join(dir, "hook-install.log")); err == nil {
		t.Fatal("expected a world-writable log directory to be refused")
	}
}

func TestRunWarnsWhenRunLogUnavailable(t *testing.T) {
	var errOut bytes.Buffer
	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveExe:          func() (string, error) { return macExe, nil },
		ResolveUsers:        func() (UserList, error) { return UserList{}, nil },
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: func(string) []Destination { return nil },
		OpenLog:             func() (io.WriteCloser, error) { return nil, errors.New("read-only volume") },
		Out:                 &bytes.Buffer{},
		ErrOut:              &errOut,
	}
	if err := inst.Run(context.Background()); err != nil {
		t.Fatalf("an unavailable run log failed the run: %v", err)
	}
	if !strings.Contains(errOut.String(), "read-only volume") {
		t.Fatalf("no warning about the run log: %q", errOut.String())
	}
}
