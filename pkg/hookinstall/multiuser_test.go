package hookinstall

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

// TestRunConvergesEveryUser proves the per-user files of every resolved user are
// converged, and that one user's malformed file fails only that user's
// destination.
func TestRunConvergesEveryUser(t *testing.T) {
	machineRoot := realTempDir(t)
	alice := &TargetUser{Username: "alice", HomeDir: t.TempDir(), UID: os.Getuid(), GID: os.Getgid()}
	bob := &TargetUser{Username: "bob", HomeDir: t.TempDir(), UID: os.Getuid(), GID: os.Getgid()}

	const copilotRel = ".copilot/hooks/obot-sentry.json"
	const settingsRel = "Library/Application Support/Code/User/settings.json"
	bobCopilot := filepath.Join(bob.HomeDir, copilotRel)
	if err := os.MkdirAll(filepath.Dir(bobCopilot), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bobCopilot, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveExe:          func() (string, error) { return macExe, nil },
		ResolveUsers:        func() (UserList, error) { return UserList{Users: []*TargetUser{alice, bob}}, nil },
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: tempDestinations(machineRoot),
		Out:                 &out,
	}
	if err := inst.Run(context.Background()); err == nil {
		t.Fatal("expected bob's malformed file to fail the run")
	}

	for _, f := range []string{
		filepath.Join(alice.HomeDir, copilotRel),
		filepath.Join(alice.HomeDir, settingsRel),
		filepath.Join(bob.HomeDir, settingsRel),
	} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("%s was not converged: %v", f, err)
		}
	}
	if got, _ := os.ReadFile(bobCopilot); string(got) != "{" {
		t.Fatalf("bob's malformed file was rewritten: %q", got)
	}

	summary := out.String()
	if !strings.Contains(summary, "Users: alice") || !strings.Contains(summary, "bob (") {
		t.Fatalf("summary does not list both users:\n%s", summary)
	}
	var failedRows []string
	for _, line := range strings.Split(summary, "\n") {
		if strings.Contains(line, " failed ") {
			failedRows = append(failedRows, line)
		}
	}
	if len(failedRows) != 1 || !strings.Contains(failedRows[0], "bob") {
		t.Fatalf("want exactly bob's Copilot file failed, got %q\n%s", failedRows, summary)
	}
}

// TestRunWithNoUsersConvergesMachineHooks proves a machine where no profile has
// been set up yet still gets its machine-wide hooks.
func TestRunWithNoUsersConvergesMachineHooks(t *testing.T) {
	machineRoot := realTempDir(t)
	var out bytes.Buffer
	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveExe:          func() (string, error) { return macExe, nil },
		ResolveUsers:        func() (UserList, error) { return UserList{}, nil },
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: tempDestinations(machineRoot),
		Out:                 &out,
	}
	if err := inst.Run(context.Background()); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(machineRoot, "ClaudeCode/managed-settings.json")); err != nil {
		t.Fatalf("machine-wide Claude Code hooks were not installed: %v", err)
	}
	if !strings.Contains(out.String(), "Users: none") {
		t.Fatalf("summary does not say no users were found:\n%s", out.String())
	}
}

func TestParseDsclUIDs(t *testing.T) {
	out := []byte("_www                     70\ndaemon                   1\nalice                    501\nnobody                   -2\nJane Doe                 502\nadmobile                 -1876543210\n\nbadline\n")
	got := parseDsclUIDs(out, 500)
	want := []localAccount{{"alice", "501"}, {"Jane Doe", "502"}, {"admobile", "2418424086"}}
	if len(got) != len(want) {
		t.Fatalf("parseDsclUIDs = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseDsclUIDs = %+v, want %+v", got, want)
		}
	}
}

func TestIsUserProfileSID(t *testing.T) {
	for sid, want := range map[string]bool{
		"S-1-5-18": false,
		"S-1-5-19": false,
		"S-1-5-20": false,
		"S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464": false,
		"S-1-5-21-1004336348-1177238915-682003330-1001":                  true,
		"S-1-5-21-1004336348-1177238915-682003330-1001.bak":              false,
		"S-1-12-1-3047302187-1149123414-3048245632-2741651528":           true,
	} {
		if got := isUserProfileSID(sid); got != want {
			t.Errorf("isUserProfileSID(%q) = %v, want %v", sid, got, want)
		}
	}
}

// TestRunKeepsEveryUsersClaudeHooksWhenManagedInstallFails extends the
// single-user case: a failed managed-settings write must leave the retired
// user-settings hooks of every user in place, not only the first.
func TestRunKeepsEveryUsersClaudeHooksWhenManagedInstallFails(t *testing.T) {
	machineRoot := realTempDir(t)
	old, err := mergeClaude(nil, macExe, "darwin", true)
	if err != nil {
		t.Fatal(err)
	}
	var users []*TargetUser
	for _, name := range []string{"alice", "bob"} {
		u := &TargetUser{Username: name, HomeDir: t.TempDir(), UID: os.Getuid(), GID: os.Getgid()}
		f := filepath.Join(u.HomeDir, ".claude", "settings.json")
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, old.data, 0o600); err != nil {
			t.Fatal(err)
		}
		users = append(users, u)
	}
	managedFile := filepath.Join(machineRoot, "ClaudeCode/managed-settings.json")
	plan := Plan{
		GOOS: "darwin", Executable: macExe, Users: users, Enforce: true,
		Destinations: []Destination{
			{Agent: localagent.ClaudeCode, Label: "Claude Code (user settings)", Scope: ScopeUser, Format: FormatJSON, Rel: ".claude/settings.json", Retired: true},
			{Agent: localagent.ClaudeCode, Label: "Claude Code", Scope: ScopeMachine, Format: FormatJSON, Abs: managedFile},
		},
	}
	changes, err := buildChanges(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(managedFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managedFile, []byte(`{"model":"opus"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	results := commitChanges(t.Context(), plan, changes)
	if len(results) != 3 {
		t.Fatalf("results = %+v, want one per user plus the machine file", results)
	}
	for _, u := range users {
		if got, err := os.ReadFile(filepath.Join(u.HomeDir, ".claude", "settings.json")); err != nil || !bytes.Equal(got, old.data) {
			t.Fatalf("%s's user settings hooks were removed without a replacement: err=%v\n%s", u.Username, err, got)
		}
	}
}

// TestRunConvergesPartialUserListAndReportsError covers a resolver that could
// list only some users: those are converged, and the run still fails.
func TestRunConvergesPartialUserListAndReportsError(t *testing.T) {
	machineRoot := realTempDir(t)
	alice := &TargetUser{Username: "alice", HomeDir: t.TempDir(), UID: os.Getuid(), GID: os.Getgid()}
	listErr := errors.New("dscl failed")
	var out bytes.Buffer
	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveExe:          func() (string, error) { return macExe, nil },
		ResolveUsers:        func() (UserList, error) { return UserList{Users: []*TargetUser{alice}}, listErr },
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: tempDestinations(machineRoot),
		Out:                 &out,
	}
	if err := inst.Run(context.Background()); !errors.Is(err, listErr) {
		t.Fatalf("Run = %v, want the listing error", err)
	}
	if _, err := os.Stat(filepath.Join(alice.HomeDir, ".copilot/hooks/obot-sentry.json")); err != nil {
		t.Fatalf("the listed user was not converged: %v", err)
	}
	if !strings.Contains(out.String(), "not all could be listed") {
		t.Fatalf("summary does not report the partial listing:\n%s", out.String())
	}
}

func TestWithUserDeduplicatesByHome(t *testing.T) {
	list := withUser(UserList{}, &TargetUser{Username: "a", HomeDir: `C:\Users\Alice`})
	list = withUser(list, &TargetUser{Username: "a2", HomeDir: `c:\users\alice`})
	list = withUser(list, &TargetUser{Username: "b", HomeDir: `C:\Users\Bob`})
	if len(list.Users) != 2 || list.Users[0].Username != "a" {
		t.Fatalf("withUser = %+v", list.Users)
	}
}

func TestRunUninstallClearsSchedule(t *testing.T) {
	cleared := false
	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveUsers:        func() (UserList, error) { return UserList{}, nil },
		ResolveDestinations: func(string) []Destination { return nil },
		ClearSchedule:       func() error { cleared = true; return nil },
		Uninstall:           true,
		Out:                 &bytes.Buffer{},
	}
	if err := inst.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !cleared {
		t.Fatal("uninstall did not clear the hook check record")
	}
}

func TestIsTemporaryProfile(t *testing.T) {
	for _, tt := range []struct {
		home, account string
		want          bool
	}{
		{`C:\Users\TEMP`, `CORP\alice`, true},
		{`C:\Users\TEMP.CORP`, `CORP\alice`, true},
		{`C:\Users\TEMP.CORP.001`, `S-1-5-21-1-2-3-1001`, true},
		{`C:\Users\temp.lab`, `LAB\temp.lab`, false},
		{`C:\Users\Temp`, `PC\temp`, false},
		{`C:\Users\temp.lab.000`, `LAB\temp.lab`, false},
		{`C:\Users\Templeton`, `CORP\templeton`, false},
		{`C:\Users\alice`, `CORP\alice`, false},
	} {
		if got := isTemporaryProfile(tt.home, tt.account); got != tt.want {
			t.Errorf("isTemporaryProfile(%q, %q) = %v, want %v", tt.home, tt.account, got, tt.want)
		}
	}
}

// TestRunGroupTimeoutIsolatesStalledGroup proves a group that stalls is
// abandoned and reported failed while every other group still converges.
func TestRunGroupTimeoutIsolatesStalledGroup(t *testing.T) {
	machineRoot := realTempDir(t)
	alice := &TargetUser{Username: "alice", HomeDir: t.TempDir(), UID: os.Getuid(), GID: os.Getgid()}
	unblock := make(chan struct{})
	var out bytes.Buffer
	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveExe:          func() (string, error) { return macExe, nil },
		ResolveUsers:        func() (UserList, error) { return UserList{Users: []*TargetUser{alice}}, nil },
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: tempDestinations(machineRoot),
		// Stands in for a machine-wide file that blocks its reader.
		LockMachineDirs: func([]Destination, bool) (map[string]error, func()) {
			<-unblock
			return nil, func() {}
		},
		GroupTimeout: 100 * time.Millisecond,
		Out:          &out,
	}
	if err := inst.Run(context.Background()); err == nil {
		t.Fatal("expected the stalled group to fail the run")
	}
	if _, err := os.Stat(filepath.Join(alice.HomeDir, ".copilot/hooks/obot-sentry.json")); err != nil {
		t.Fatalf("a user's hooks were held up by the stalled machine-wide group: %v", err)
	}
	// Let the abandoned groups come unstuck: they must still write nothing.
	close(unblock)
	time.Sleep(300 * time.Millisecond)
	for _, f := range []string{"etc/codex/requirements.toml", "Cursor/hooks.json", "ClaudeCode/managed-settings.json"} {
		if _, err := os.Stat(filepath.Join(machineRoot, f)); !os.IsNotExist(err) {
			t.Fatalf("an abandoned group wrote %s after its deadline (err=%v)", f, err)
		}
	}
	if !strings.Contains(out.String(), "timed out") {
		t.Fatalf("summary does not report the timeout:\n%s", out.String())
	}
	// The retired user file is left alone while its replacement did not land.
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "Claude Code (user settings)") && strings.Contains(line, " removed ") {
			t.Fatalf("retired hooks removed though the managed write timed out:\n%s", out.String())
		}
	}
}

// TestRunDedupesUsersAfterLocking proves a stale profile that fails the home
// check does not knock out the live one that shares its directory.
func TestRunDedupesUsersAfterLocking(t *testing.T) {
	machineRoot := realTempDir(t)
	home := t.TempDir()
	stale := &TargetUser{Username: "stale", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}
	live := &TargetUser{Username: "live", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}
	var out bytes.Buffer
	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveExe:          func() (string, error) { return macExe, nil },
		ResolveUsers:        func() (UserList, error) { return UserList{Users: []*TargetUser{stale, live}}, nil },
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: tempDestinations(machineRoot),
		LockHomes: func(l UserList) (UserList, func()) {
			l.Users = l.Users[1:]
			l.Skipped = append(l.Skipped, SkippedUser{Username: "stale", Reason: "wrong owner"})
			return l, func() {}
		},
		Out: &out,
	}
	if err := inst.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Users: live") {
		t.Fatalf("the live user was not converged:\n%s", out.String())
	}
}
