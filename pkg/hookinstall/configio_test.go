package hookinstall

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// testUser is owned by the current process user, so chownToUser is exercised
// without requiring root.
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
		dir, err := os.Stat(filepath.Join(home, ".copilot", "hooks"))
		if err != nil {
			t.Fatal(err)
		}
		if perm := dir.Mode().Perm(); perm != 0o700 {
			t.Fatalf("expected private 0700 user directory, got %o", perm)
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
	entries, err := os.ReadDir(filepath.Join(home, ".claude"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "settings.json" {
		t.Fatalf("unexpected leftovers in the config directory: %v", entries)
	}
}

// An interrupted run of this same pid leaves a temp file that would fail O_EXCL.
func TestCommitClearsStaleTempFromSamePID(t *testing.T) {
	home := t.TempDir()
	u := testUser(home)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, fmt.Sprintf(".settings.json.%d.obot-sentry-tmp", os.Getpid()))
	if err := os.WriteFile(stale, []byte("interrupted"), 0o600); err != nil {
		t.Fatal(err)
	}

	abs := filepath.Join(dir, "settings.json")
	if err := commitConfigFile(ScopeUser, u, abs, []byte("managed")); err != nil {
		t.Fatalf("commit over a stale temp file failed: %v", err)
	}
	if got, _ := os.ReadFile(abs); string(got) != "managed" {
		t.Fatalf("destination content = %q, want managed", got)
	}
	if _, err := os.Lstat(stale); err == nil {
		t.Fatal("stale temp file survived the commit")
	}
}

// Mirrors macOS's /etc -> private/etc: a relative symlink inside the root is
// traversed.
func TestMachineConfigTraversesRootContainedIntermediateSymlink(t *testing.T) {
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
	if err := os.Symlink(filepath.Join("private", "etc"), filepath.Join(base, "etc")); err != nil {
		t.Fatal(err)
	}

	abs := filepath.Join(base, "etc", "codex", "requirements.toml")
	data := []byte("[features]\nhooks = true\n")
	if _, ok, err := readConfigFile(ScopeMachine, "", abs); err != nil || ok {
		t.Fatalf("missing config should read as absent: ok=%v err=%v", ok, err)
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
	info, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("expected world-readable 0644 machine file, got %o", perm)
	}
}

func TestSymlinkedDestinationIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink planting requires privilege on Windows")
	}
	home := t.TempDir()
	u := testUser(home)
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

	if _, _, err := readConfigFile(ScopeUser, home, link); err == nil {
		t.Fatal("expected a symlinked config to be refused, not followed")
	}
	if err := commitConfigFile(ScopeUser, u, link, []byte("managed")); err == nil {
		t.Fatal("expected a commit onto a symlinked destination to be refused")
	}
	if got, _ := os.ReadFile(target); string(got) != "original" {
		t.Fatalf("symlink target was written through: %q", got)
	}
}

func TestCommitRefusesNonRegularTarget(t *testing.T) {
	home := t.TempDir()
	u := testUser(home)
	abs := filepath.Join(home, "settings.json")
	if err := os.Mkdir(abs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := commitConfigFile(ScopeUser, u, abs, []byte("x")); err == nil {
		t.Fatal("expected commit to refuse overwriting a directory")
	}
}

func TestRefusesEscapingIntermediateSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink planting requires privilege on Windows")
	}
	home := t.TempDir()
	u := testUser(home)
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
		t.Fatal("expected a read through an escaping intermediate symlink to be refused")
	}
	if err := commitConfigFile(ScopeUser, u, abs, []byte("managed")); err == nil {
		t.Fatal("expected a commit through an escaping intermediate symlink to be refused")
	}
	if got, _ := os.ReadFile(filepath.Join(evil, "hooks", "obot-sentry.json")); string(got) != "planted" {
		t.Fatalf("wrote outside the root: %q", got)
	}
}

func TestReadRefusesPathEscapingHome(t *testing.T) {
	home := t.TempDir()
	outside := filepath.Join(filepath.Dir(home), "elsewhere.json")
	if _, _, err := readConfigFile(ScopeUser, home, outside); err == nil {
		t.Fatal("expected a path outside the rooted home to be refused")
	}
}

func TestDirChain(t *testing.T) {
	for _, tc := range []struct {
		dir  string
		want []string
	}{
		{".", nil},
		{"", nil},
		{string(filepath.Separator), nil},
		{".claude", []string{".claude"}},
		{filepath.Join(".copilot", "hooks"), []string{".copilot", filepath.Join(".copilot", "hooks")}},
	} {
		got := dirChain(tc.dir)
		if len(got) != len(tc.want) {
			t.Fatalf("dirChain(%q) = %v, want %v", tc.dir, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("dirChain(%q) = %v, want %v", tc.dir, got, tc.want)
			}
		}
	}
}

func TestRelWithinRejectsEscape(t *testing.T) {
	base := filepath.FromSlash("/home/alice")
	if _, err := relWithin(base, filepath.Join(base, "sub", "file")); err != nil {
		t.Fatalf("a contained path should resolve: %v", err)
	}
	if _, err := relWithin(base, filepath.FromSlash("/etc/passwd")); err == nil {
		t.Fatal("expected an escaping absolute path to be rejected")
	}
	if _, err := relWithin(base, filepath.Join(base, "..", "bob", "file")); err == nil {
		t.Fatal("expected a parent-escaping path to be rejected")
	}
}

func TestRootForMachineUsesVolumeRoot(t *testing.T) {
	rootDir, rel, err := rootFor(ScopeMachine, "", filepath.FromSlash("/etc/codex/requirements.toml"))
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.VolumeName(filepath.FromSlash("/etc/codex/requirements.toml")) + string(filepath.Separator)
	if rootDir != wantRoot {
		t.Fatalf("machine root = %q, want %q", rootDir, wantRoot)
	}
	if filepath.Join(rootDir, rel) != filepath.Clean(filepath.FromSlash("/etc/codex/requirements.toml")) {
		t.Fatalf("root+rel = %q", filepath.Join(rootDir, rel))
	}
}
