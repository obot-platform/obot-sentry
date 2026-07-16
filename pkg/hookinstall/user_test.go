package hookinstall

import (
	"os"
	"path/filepath"
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

	if err := validateHomeDir(filepath.Join(home, "does-not-exist")); err == nil {
		t.Fatal("a missing home should be rejected")
	}

	file := filepath.Join(home, "regular")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateHomeDir(file); err == nil {
		t.Fatal("a non-directory home should be rejected")
	}
}
