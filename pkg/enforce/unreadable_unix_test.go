//go:build darwin || linux

package enforce

import (
	"os"
	"testing"
)

// makeUnreadable denies every access to dir until the test ends. Root reads any
// directory regardless of its mode, so the test is skipped there.
func makeUnreadable(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads any directory")
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}
