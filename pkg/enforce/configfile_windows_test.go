//go:build windows

package enforce

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenConfigFileRejectsSpecialNamespaces(t *testing.T) {
	for _, path := range []string{
		`\\.\pipe\obot-sentry-test`,
		`\\?\GLOBALROOT\Device\NamedPipe\obot-sentry-test`,
		`\\localhost\pipe\obot-sentry-test`,
		`\\localhost\mailslot\obot-sentry-test`,
	} {
		t.Run(strings.ReplaceAll(path, `\`, "_"), func(t *testing.T) {
			f, err := openConfigFile(path)
			if f != nil {
				_ = f.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "namespace") {
				t.Fatalf("openConfigFile(%q) error = %v, want special-namespace rejection", path, err)
			}
		})
	}
}

func TestWindowsSpecialNamespaceAllowsOrdinaryUNCShare(t *testing.T) {
	path := `\\server\share\project\.cursor\mcp.json`
	if hasWindowsSpecialNamespace(path) {
		t.Fatalf("ordinary UNC path %q was classified as a special namespace", path)
	}
}

// TestNewEnvDoesNotTrustUserProfile covers the home directory the hook reads
// user configuration from: the account's profile directory, whatever
// USERPROFILE says. A USERPROFILE naming another directory is recorded as an
// override rather than followed.
func TestNewEnvDoesNotTrustUserProfile(t *testing.T) {
	// Untouched, USERPROFILE and the account's profile directory are the same
	// text, which is all that is compared. If they were spelled differently every
	// MCP call on the machine would be reported as an override.
	if env, err := NewEnv(); err != nil || env.HomeOverride != nil {
		t.Fatalf("NewEnv in an unmodified environment: Home = %q, USERPROFILE = %q, err = %v; want no override",
			env.Home, os.Getenv("USERPROFILE"), err)
	}

	decoy := t.TempDir()
	for _, home := range []string{decoy, "relative-home"} {
		t.Setenv("USERPROFILE", home)
		env, err := NewEnv()
		if err != nil {
			t.Fatalf("NewEnv with USERPROFILE=%q: %v", home, err)
		}
		if !filepath.IsAbs(env.Home) || strings.EqualFold(env.Home, home) {
			t.Errorf("Home = %q with USERPROFILE=%q, want the account's profile directory", env.Home, home)
		}
		if env.HomeOverride == nil || *env.HomeOverride != home {
			t.Errorf("HomeOverride = %v, want %q", env.HomeOverride, home)
		}
	}
}
