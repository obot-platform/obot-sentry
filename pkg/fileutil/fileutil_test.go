package fileutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileAtomicWritesContentAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	if err := WriteFileAtomic(path, []byte("contents\n"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "contents\n" {
		t.Fatalf("content = %q, want %q", string(data), "contents\n")
	}
	if runtime.GOOS == "windows" {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %o, want 600", got)
	}
}
