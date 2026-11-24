package hookinstall

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateHomeDir(t *testing.T) {
	home := t.TempDir()
	if err := validateHomeDir(home); err != nil {
		t.Fatalf("a real directory should validate, got %v", err)
	}

	if err := validateHomeDir(""); err == nil {
		t.Fatal("an empty home should be rejected")
	}

	// A missing home under an existing parent is an account that has not
	// signed in yet: rejected as not existing, so the resolver drops it.
	if err := validateHomeDir(filepath.Join(home, "does-not-exist")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing home under an existing parent should be fs.ErrNotExist, got %v", err)
	}

	// A missing parent means the volume or folder holding the home is gone:
	// rejected with an error that is not fs.ErrNotExist, so it is reported.
	gone := filepath.Join(home, "unmounted-volume", "user")
	err := validateHomeDir(gone)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a home whose parent is missing should be reported, not dropped, got %v", err)
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Fatalf("unexpected error for a home whose parent is missing: %v", err)
	}

	file := filepath.Join(home, "regular")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateHomeDir(file); err == nil {
		t.Fatal("a non-directory home should be rejected")
	}
}
