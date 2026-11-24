//go:build !windows

package hookinstall

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestReadConfigFileRefusesFIFOWithoutBlocking proves a named pipe in place of a
// hook file is refused instead of hanging the privileged run.
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
