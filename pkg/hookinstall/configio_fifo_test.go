//go:build !windows

package hookinstall

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestReadConfigFileRefusesFIFOWithoutBlocking(t *testing.T) {
	home := t.TempDir()
	abs := filepath.Join(home, "settings.json")
	if err := syscall.Mkfifo(abs, 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	if _, _, err := readConfigFile(ScopeUser, testUser(home), abs); err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("FIFO config err = %v, want a non-regular refusal", err)
	}
}

// TestChownCreatedDirRefusesASwappedInDirectory covers a user swapping another
// directory (here, a non-empty one) in for the one this run just created.
func TestChownCreatedDirRefusesASwappedInDirectory(t *testing.T) {
	home := t.TempDir()
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	u := testUser(home)

	if err := os.Mkdir(filepath.Join(home, "fresh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := chownCreatedDir(root, "fresh", u); err != nil {
		t.Fatalf("an empty directory this run created was refused: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(home, "elsewhere", "contents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", filepath.Join(home, "swapped")); err != nil {
		t.Fatal(err)
	}
	if err := chownCreatedDir(root, "swapped", u); err == nil {
		t.Fatal("a link to a non-empty directory was handed over")
	}
}
