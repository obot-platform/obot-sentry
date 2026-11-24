package hookinstall

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

	if _, ok, err := readConfigFile(ScopeUser, testUser(home), abs); err != nil || ok {
		t.Fatalf("missing file should read as absent: ok=%v err=%v", ok, err)
	}

	data := []byte("{\"hooks\":{}}\n")
	if err := commitConfigFile(ScopeUser, u, abs, data); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	got, ok, err := readConfigFile(ScopeUser, testUser(home), abs)
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
	got, _, err := readConfigFile(ScopeUser, testUser(home), abs)
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
	if _, ok, err := readConfigFile(ScopeMachine, nil, abs); err != nil || ok {
		t.Fatalf("missing config should read as absent: ok=%v err=%v", ok, err)
	}
	if err := commitConfigFile(ScopeMachine, nil, abs, data); err != nil {
		t.Fatalf("commit through root-contained machine symlink failed: %v", err)
	}
	got, ok, err := readConfigFile(ScopeMachine, nil, abs)
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

	if _, _, err := readConfigFile(ScopeUser, testUser(home), link); err == nil {
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
	if _, _, err := readConfigFile(ScopeUser, testUser(home), abs); err == nil {
		t.Fatal("expected a read through an escaping intermediate symlink to be refused")
	}
	if err := commitConfigFile(ScopeUser, u, abs, []byte("managed")); err == nil {
		t.Fatal("expected a commit through an escaping intermediate symlink to be refused")
	}
	if got, _ := os.ReadFile(filepath.Join(evil, "hooks", "obot-sentry.json")); string(got) != "planted" {
		t.Fatalf("wrote outside the root: %q", got)
	}
}

func TestUserConfigRefusesRootContainedIntermediateSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows")
	}
	home := t.TempDir()
	u := testUser(home)
	actual := filepath.Join(home, "actual-copilot")
	if err := os.MkdirAll(filepath.Join(actual, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("actual-copilot", filepath.Join(home, ".copilot")); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(home, ".copilot", "hooks", "obot-sentry.json")

	if _, _, err := readConfigFile(ScopeUser, testUser(home), abs); err == nil {
		t.Fatal("read followed a user-scope intermediate symlink")
	}
	if err := commitConfigFile(ScopeUser, u, abs, []byte("managed")); err == nil {
		t.Fatal("commit followed a user-scope intermediate symlink")
	}
	if _, err := os.Lstat(filepath.Join(actual, "hooks", "obot-sentry.json")); !os.IsNotExist(err) {
		t.Fatalf("symlink target was changed: %v", err)
	}
}

func TestReadRefusesPathEscapingHome(t *testing.T) {
	home := t.TempDir()
	outside := filepath.Join(filepath.Dir(home), "elsewhere.json")
	if _, _, err := readConfigFile(ScopeUser, testUser(home), outside); err == nil {
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
	// The path has to be one this host would install to: Windows requires a local
	// drive-letter volume, which the /etc location Codex uses on macOS has none of.
	abs := filepath.FromSlash("/etc/codex/requirements.toml")
	if runtime.GOOS == "windows" {
		abs = filepath.Join(windowsProgramData(), "OpenAI", "Codex", "requirements.toml")
	}

	rootDir, rel, err := rootFor(ScopeMachine, "", abs)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.VolumeName(abs) + string(filepath.Separator)
	if rootDir != wantRoot {
		t.Fatalf("machine root = %q, want %q", rootDir, wantRoot)
	}
	if filepath.Join(rootDir, rel) != filepath.Clean(abs) {
		t.Fatalf("root+rel = %q, want %q", filepath.Join(rootDir, rel), filepath.Clean(abs))
	}
}

// TestWriteAtomicErrorsNameTheAbsolutePath covers a failed replace. os.Root
// reports paths relative to the root, which for a machine file on Windows is
// the path without its drive ("Program Files\..."), so the error has to name
// the file the caller knows instead.
func TestWriteAtomicErrorsNameTheAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	// A non-empty directory where the file belongs: no platform renames a file
	// over one.
	if err := os.MkdirAll(filepath.Join(dir, "managed-settings.json", "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	abs := filepath.Join(dir, "managed-settings.json")
	err = writeAtomic(root, "managed-settings.json", abs, []byte("{}"), 0o644, nil)
	if err == nil {
		t.Fatal("writeAtomic replaced a non-empty directory")
	}
	msg := err.Error()
	if !strings.Contains(msg, fmt.Sprintf("%q", abs)) {
		t.Fatalf("error %q does not name %s", msg, abs)
	}
	if strings.Contains(msg, "obot-sentry-tmp") {
		t.Fatalf("error %q leaks the root-relative temporary path", msg)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temporary file left behind: %v", entries)
	}
}

// TestReadRefusesHardLinkedConfig proves a config with a second link is
// refused rather than read: the link could point the privileged reader at a
// file its user cannot read, whose contents the merge would copy into a file
// they own.
func TestReadRefusesHardLinkedConfig(t *testing.T) {
	home := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(secret, []byte(`{"token":"s3cret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(home, ".cursor", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, abs); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, _, err := readConfigFile(ScopeUser, testUser(home), abs); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("readConfigFile = %v, want a hard-link refusal", err)
	}
	if _, _, err := readConfigFile(ScopeMachine, nil, abs); err == nil {
		t.Fatal("a machine-scoped hard-linked config was read")
	}
}

// TestReadRefusesConfigOwnedByAnotherUser proves a user-scoped config owned by
// some third account is refused.
func TestReadRefusesConfigOwnedByAnotherUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owners are not compared on Windows")
	}
	home := t.TempDir()
	abs := filepath.Join(home, "settings.json")
	if err := os.WriteFile(abs, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := &TargetUser{Username: "other", HomeDir: home, UID: os.Getuid() + 1, GID: os.Getgid()}
	if os.Getuid() == 0 {
		t.Skip("a root-owned file is allowed")
	}
	if _, _, err := readConfigFile(ScopeUser, other, abs); err == nil {
		t.Fatal("a config owned by another account was read")
	}
}

// TestReadRefusesFIFOWithoutBlocking proves a FIFO where a config should be is
// refused at once rather than blocking the reader.
func TestReadRefusesFIFOWithoutBlocking(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no FIFOs in the Windows file system")
	}
	home := t.TempDir()
	abs := filepath.Join(home, "settings.json")
	if err := mkfifo(abs); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		// The Lstat check refuses a FIFO already in place; open it directly
		// too, as a swap after that check would.
		_, _, err := readConfigFile(ScopeUser, testUser(home), abs)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO was read as a config")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	opened := make(chan error, 1)
	go func() {
		f, err := root.OpenFile("settings.json", configReadFlags, 0)
		if err == nil {
			_ = f.Close()
		}
		opened <- err
	}()
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO with configReadFlags blocked")
	}
}

// TestWriteAtomicHandsFileToOwnerBeforeRename proves the new file is given to
// its owner while it still has its temporary name: once renamed into the user's
// directory, the name could be swapped for a hard link, and a chown by name
// would then hand over whatever it pointed to.
func TestWriteAtomicHandsFileToOwnerBeforeRename(t *testing.T) {
	home := t.TempDir()
	u := testUser(home)
	abs := filepath.Join(home, "settings.json")
	calls := 0
	orig := chownOpenFile
	t.Cleanup(func() { chownOpenFile = orig })
	chownOpenFile = func(f *os.File, got *TargetUser) error {
		calls++
		if got != u {
			t.Errorf("handed to %+v, want %+v", got, u)
		}
		if _, err := os.Lstat(abs); !os.IsNotExist(err) {
			t.Errorf("the file was already in place when handed over (err=%v)", err)
		}
		if filepath.Base(f.Name()) == "settings.json" {
			t.Errorf("handed over by its final name %q", f.Name())
		}
		return nil
	}
	if err := commitConfigFile(ScopeUser, u, abs, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("chownOpenFile called %d times, want 1", calls)
	}
}
