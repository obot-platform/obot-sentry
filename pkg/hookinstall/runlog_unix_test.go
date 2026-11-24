//go:build !windows

package hookinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecureRunDirNamesWhatItRefuses proves a run directory replaced by a
// symlink or a regular file is refused with an error that says which, rather
// than the "not a directory" the no-follow open reports for both.
func TestSecureRunDirNamesWhatItRefuses(t *testing.T) {
	parent := t.TempDir()
	target := t.TempDir()

	link := filepath.Join(parent, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := secureRunDir(parent, link); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlinked run directory: got %v, want a symbolic link refusal", err)
	}

	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := secureRunDir(parent, file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("regular-file run directory: got %v, want a not-a-directory refusal", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() == 0o700 {
		t.Fatalf("symlink target %q was chmodded through the link", target)
	}
}
