package hookinstall

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestQuotePOSIX(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"clean path unquoted", "/usr/local/bin/obot-sentry", "/usr/local/bin/obot-sentry"},
		{"space single-quoted", "/opt/Obot Tools/obot-sentry", "'/opt/Obot Tools/obot-sentry'"},
		{"apostrophe escaped", "/home/o'brien/obot-sentry", `'/home/o'\''brien/obot-sentry'`},
		{"unicode quoted", "/opt/obôt-sentry/obot-sentry", "'/opt/obôt-sentry/obot-sentry'"},
		{"empty quoted", "", "''"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := quotePOSIX(tc.in); got != tc.want {
				t.Fatalf("quotePOSIX(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestQuoteWindows(t *testing.T) {
	got := quoteWindows(`C:\Program Files\Obot\obot-sentry\obot-sentry.exe`)
	want := `"C:\Program Files\Obot\obot-sentry\obot-sentry.exe"`
	if got != want {
		t.Fatalf("quoteWindows = %q, want %q", got, want)
	}
}

// TestHookCommandGolden pins the exact command string for every agent/phase on
// both operating systems, using the durable packaged executable paths.
func TestHookCommandGolden(t *testing.T) {
	const (
		macExe = packagedDarwinExecutable
		winExe = packagedWindowsExecutable
	)
	tests := []struct {
		name  string
		exe   string
		goos  string
		agent Agent
		phase phase
		want  string
	}{
		{"darwin claude post", macExe, "darwin", AgentClaudeCode, phasePostTool,
			"/usr/local/bin/obot-sentry audit submit --agent claude-code --phase post-tool --managed-by obot-sentry"},
		{"darwin claude failure", macExe, "darwin", AgentClaudeCode, phaseFailure,
			"/usr/local/bin/obot-sentry audit submit --agent claude-code --phase failure --managed-by obot-sentry"},
		{"darwin codex post", macExe, "darwin", AgentCodex, phasePostTool,
			"/usr/local/bin/obot-sentry audit submit --agent codex --phase post-tool --managed-by obot-sentry"},
		{"darwin vscode post", macExe, "darwin", AgentVSCode, phasePostTool,
			"/usr/local/bin/obot-sentry audit submit --agent vscode --phase post-tool --managed-by obot-sentry"},
		{"darwin cursor post", macExe, "darwin", AgentCursor, phasePostTool,
			"/usr/local/bin/obot-sentry audit submit --agent cursor --phase post-tool --managed-by obot-sentry"},

		// Windows Claude Code and Cursor: directly quoted executable, no operator.
		{"windows claude post", winExe, "windows", AgentClaudeCode, phasePostTool,
			`"C:\Program Files\Obot\obot-sentry\obot-sentry.exe" audit submit --agent claude-code --phase post-tool --managed-by obot-sentry`},
		{"windows cursor failure", winExe, "windows", AgentCursor, phaseFailure,
			`"C:\Program Files\Obot\obot-sentry\obot-sentry.exe" audit submit --agent cursor --phase failure --managed-by obot-sentry`},
		// Windows Codex and VS Code: PowerShell call operator prefix.
		{"windows codex post", winExe, "windows", AgentCodex, phasePostTool,
			`& "C:\Program Files\Obot\obot-sentry\obot-sentry.exe" audit submit --agent codex --phase post-tool --managed-by obot-sentry`},
		{"windows vscode post", winExe, "windows", AgentVSCode, phasePostTool,
			`& "C:\Program Files\Obot\obot-sentry\obot-sentry.exe" audit submit --agent vscode --phase post-tool --managed-by obot-sentry`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hookCommand(tc.exe, tc.goos, tc.agent, tc.phase); got != tc.want {
				t.Fatalf("hookCommand = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestProductionCommandsHaveNoDebugOrSecrets guards every generated command
// against test/debug flags, deployment credentials, unsupported phases, and
// input-mutation options leaking into production hook config.
func TestProductionCommandsHaveNoDebugOrSecrets(t *testing.T) {
	forbidden := []string{
		"--dry-run",
		"--print-normalized",
		"--server-url",
		"ServerURL",
		"--enrollment-key",
		"EnrollmentKey",
		"--input",
		"pre-tool",
	}
	for _, goos := range []string{"darwin", "windows"} {
		exe := "/usr/local/bin/obot-sentry"
		if goos == "windows" {
			exe = `C:\Program Files\Obot\obot-sentry\obot-sentry.exe`
		}
		for _, agent := range Agents() {
			for _, p := range []phase{phasePostTool, phaseFailure} {
				cmd := hookCommand(exe, goos, agent, p)
				for _, bad := range forbidden {
					if strings.Contains(cmd, bad) {
						t.Fatalf("command for %s/%s contains forbidden token %q: %q", goos, agent, bad, cmd)
					}
				}
				if !strings.Contains(cmd, "--managed-by obot-sentry") {
					t.Fatalf("command for %s/%s missing ownership marker: %q", goos, agent, cmd)
				}
			}
		}
	}
}

func TestPackagedExecutablePaths(t *testing.T) {
	tests := []struct {
		goos string
		want string
	}{
		{"darwin", "/usr/local/bin/obot-sentry"},
		{"windows", `C:\Program Files\Obot\obot-sentry\obot-sentry.exe`},
	}
	for _, tc := range tests {
		t.Run(tc.goos, func(t *testing.T) {
			got, err := packagedExecutable(tc.goos)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("packagedExecutable(%q) = %q, want %q", tc.goos, got, tc.want)
			}
		})
	}

	if _, err := packagedExecutable("linux"); !errors.Is(err, errUnsupportedPlatform) {
		t.Fatalf("packagedExecutable(linux) error = %v, want %v", err, errUnsupportedPlatform)
	}
}

func TestDefaultExecutableUsesCurrentPlatformPackagePath(t *testing.T) {
	want, wantErr := packagedExecutable(runtime.GOOS)
	got, err := DefaultExecutable()
	if !errors.Is(err, wantErr) {
		t.Fatalf("DefaultExecutable error = %v, want %v", err, wantErr)
	}
	if got != want {
		t.Fatalf("DefaultExecutable = %q, want %q", got, want)
	}
}

func TestValidateExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode-bit checks do not apply on Windows")
	}
	// base holds the test binaries; it is created before TMPDIR is redirected so
	// it is a sibling of — not inside — the directory validateExecutable treats
	// as temporary. This lets the valid case pass while the temp-rejection case
	// still exercises the os.TempDir() branch.
	base := t.TempDir()
	t.Setenv("TMPDIR", t.TempDir())

	writeFile := func(name string, perm os.FileMode) string {
		p := filepath.Join(base, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), perm); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, perm); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("valid", func(t *testing.T) {
		if err := validateExecutable(writeFile("valid", 0o755)); err != nil {
			t.Fatalf("expected valid executable, got %v", err)
		}
	})
	t.Run("not executable", func(t *testing.T) {
		err := validateExecutable(writeFile("noexec", 0o644))
		if err == nil || !strings.Contains(err.Error(), "not readable/executable by normal users") {
			t.Fatalf("expected not-readable/executable error, got %v", err)
		}
	})
	t.Run("owner-only executable", func(t *testing.T) {
		// 0700 has an execute bit but not for the normal agent users the hooks
		// run as, so it must be rejected even though a bare has-any-exec check
		// would accept it.
		err := validateExecutable(writeFile("owneronly", 0o700))
		if err == nil || !strings.Contains(err.Error(), "not readable/executable by normal users") {
			t.Fatalf("expected not-readable/executable error, got %v", err)
		}
	})
	t.Run("group or world writable", func(t *testing.T) {
		err := validateExecutable(writeFile("writable", 0o757))
		if err == nil || !strings.Contains(err.Error(), "writable by non-administrators") {
			t.Fatalf("expected writable error, got %v", err)
		}
	})
	t.Run("non-regular", func(t *testing.T) {
		dir := filepath.Join(base, "adir")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		err := validateExecutable(dir)
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("expected non-regular error, got %v", err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		err := validateExecutable(filepath.Join(base, "does-not-exist"))
		if err == nil || !strings.Contains(err.Error(), "not accessible") {
			t.Fatalf("expected not-accessible error, got %v", err)
		}
	})
	t.Run("inside temp dir", func(t *testing.T) {
		p := filepath.Join(os.TempDir(), "obot-sentry-temp-binary")
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(p) })
		err := validateExecutable(p)
		if err == nil || !strings.Contains(err.Error(), "temporary directory") {
			t.Fatalf("expected temporary-directory error, got %v", err)
		}
	})
}
