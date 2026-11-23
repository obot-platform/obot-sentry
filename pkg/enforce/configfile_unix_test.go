//go:build darwin || linux

package enforce

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestNewEnvDoesNotTrustHome covers the home directory the hook reads user
// configuration from: the account's, whatever HOME says. A HOME naming another
// directory is recorded as an override rather than followed.
func TestNewEnvDoesNotTrustHome(t *testing.T) {
	decoy := t.TempDir()
	for _, home := range []string{decoy, "relative-home"} {
		t.Setenv("HOME", home)
		env, err := NewEnv()
		if err != nil {
			t.Fatalf("NewEnv with HOME=%q: %v", home, err)
		}
		if !filepath.IsAbs(env.Home) || env.Home == home || strings.HasPrefix(env.Home, decoy) {
			t.Errorf("Home = %q with HOME=%q, want the account's home directory", env.Home, home)
		}
		if env.HomeOverride != home {
			t.Errorf("HomeOverride = %q, want %q", env.HomeOverride, home)
		}
	}
}

func TestConfigLoaderRefusesFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	result := make(chan loadResult, 1)
	go func() {
		_, res := newConfigLoader().readConfig(ctx, path)
		result <- res
	}()

	select {
	case res := <-result:
		if res != loadUnusable {
			t.Fatalf("FIFO load = %v, want loadUnusable", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("opening a FIFO blocked")
	}
}
