package hookinstall

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// testUser builds a TargetUser anchored at home and owned by the current
// process user, so the POSIX chown in applyOwnership is a no-op-equivalent that
// still exercises the code path without requiring root.
func testUser(home string) *TargetUser {
	return &TargetUser{Username: "tester", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}
}

func TestCommitAndReadUserConfigRoundTrip(t *testing.T) {
	home := t.TempDir()
	u := testUser(home)
	abs := filepath.Join(home, ".copilot", "hooks", "obot-sentry.json")

	if _, ok, err := readConfigFile(ScopeUser, home, abs); err != nil || ok {
		t.Fatalf("missing file should read as absent: ok=%v err=%v", ok, err)
	}

	data := []byte("{\"hooks\":{}}\n")
	if err := commitConfigFile(ScopeUser, u, abs, data); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	got, ok, err := readConfigFile(ScopeUser, home, abs)
	if err != nil || !ok {
		t.Fatalf("expected to read back the file: ok=%v err=%v", ok, err)
	}
	if string(got) != string(data) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, data)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(abs)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("expected private 0600 user file, got %o", perm)
		}
	}
}

func TestCommitOverwritesRegularFile(t *testing.T) {
	home := t.TempDir()
	u := testUser(home)
	abs := filepath.Join(home, ".claude", "settings.json")

	if err := commitConfigFile(ScopeUser, u, abs, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := commitConfigFile(ScopeUser, u, abs, []byte("second")); err != nil {
		t.Fatal(err)
	}
	got, _, err := readConfigFile(ScopeUser, home, abs)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Fatalf("expected the second write to win, got %q", got)
	}
}

func TestMachineConfigAllowsRootContainedIntermediateSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "private", "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Mirror the macOS filesystem layout: /etc -> private/etc.
	if err := os.Symlink(filepath.Join("private", "etc"), filepath.Join(base, "etc")); err != nil {
		t.Fatal(err)
	}

	abs := filepath.Join(base, "etc", "codex", "requirements.toml")
	data := []byte("[features]\nhooks = true\n")
	if _, ok, err := readConfigFile(ScopeMachine, "", abs); err != nil || ok {
		t.Fatalf("missing config through root-contained machine symlink should read as absent: ok=%v err=%v", ok, err)
	}
	if err := commitConfigFile(ScopeMachine, nil, abs, data); err != nil {
		t.Fatalf("commit through root-contained machine symlink failed: %v", err)
	}
	got, ok, err := readConfigFile(ScopeMachine, "", abs)
	if err != nil || !ok {
		t.Fatalf("read through root-contained machine symlink failed: ok=%v err=%v", ok, err)
	}
	if string(got) != string(data) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, data)
	}
	if got, err := os.ReadFile(filepath.Join(base, "private", "etc", "codex", "requirements.toml")); err != nil || string(got) != string(data) {
		t.Fatalf("resolved target was not written: got=%q err=%v", got, err)
	}
}

func TestReadRefusesSymlinkedConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink planting requires privilege on Windows")
	}
	home := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "settings.json")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readConfigFile(ScopeUser, home, link); err == nil {
		t.Fatal("expected a symlinked config to be refused, not followed")
	}
}

func TestCommitRefusesNonRegularTarget(t *testing.T) {
	home := t.TempDir()
	u := testUser(home)
	// A directory where the config file should be is a non-regular target.
	abs := filepath.Join(home, "settings.json")
	if err := os.Mkdir(abs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := commitConfigFile(ScopeUser, u, abs, []byte("x")); err == nil {
		t.Fatal("expected commit to refuse overwriting a directory")
	}
}

func TestReadRefusesIntermediateSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink planting requires privilege on Windows")
	}
	home := t.TempDir()
	// Plant a symlinked intermediate directory: ~/.copilot -> /tmp/xxx/evil.
	evil := t.TempDir()
	if err := os.MkdirAll(filepath.Join(evil, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evil, "hooks", "obot-sentry.json"), []byte("planted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(evil, filepath.Join(home, ".copilot")); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(home, ".copilot", "hooks", "obot-sentry.json")
	if _, _, err := readConfigFile(ScopeUser, home, abs); err == nil {
		t.Fatal("expected a read through a symlinked intermediate directory to be refused")
	}
}

func TestCommitReplacesPlantedFinalSymlinkWithoutFollowing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink planting requires privilege on Windows")
	}
	home := t.TempDir()
	u := testUser(home)

	// A user plants ~/.claude/settings.json -> a file they want us to clobber.
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".claude", "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := commitConfigFile(ScopeUser, u, link, []byte("managed")); err != nil {
		t.Fatalf("commit over a planted symlink should self-heal, got %v", err)
	}
	// The link target must be untouched (never followed)...
	if got, _ := os.ReadFile(target); string(got) != "original" {
		t.Fatalf("symlink target was written through: %q", got)
	}
	// ...and the destination is now a real file with the managed content.
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("destination is still a symlink after commit")
	}
	if got, _ := os.ReadFile(link); string(got) != "managed" {
		t.Fatalf("destination content = %q, want managed", got)
	}
}

func TestReadRefusesPathEscapingHome(t *testing.T) {
	home := t.TempDir()
	outside := filepath.Join(filepath.Dir(home), "elsewhere.json")
	if _, _, err := readConfigFile(ScopeUser, home, outside); err == nil {
		t.Fatal("expected a path outside the anchored home to be refused")
	}
}
