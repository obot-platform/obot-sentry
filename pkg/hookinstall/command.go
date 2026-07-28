package hookinstall

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

// hookTimeout is the per-hook timeout, in seconds, written into every managed
// entry. It is the same for all four agents.
const hookTimeout = 30

// These are the final executable locations owned by the MDM packages. Hook
// configuration must refer to these paths, rather than to a build artifact or
// an MDM download/staging path that happened to launch hook-install.
const (
	packagedDarwinExecutable  = "/usr/local/bin/obot-sentry"
	packagedWindowsExecutable = `C:\Program Files\Obot\obot-sentry\obot-sentry.exe`
)

const (
	// managedByFlag is the flag that marks a hook command as obot-sentry-owned. It is
	// accepted (and ignored) by `obot-sentry audit submit` precisely so the installer
	// can recognize and replace its own entries on every run.
	managedByFlag = "--managed-by"
	// managedByValue is the sole accepted marker value.
	managedByValue = "obot-sentry"
	// managedMarker is the exact token pair every command this package generates
	// carries, and the signal used to recognize obot-sentry-owned entries.
	managedMarker = managedByFlag + " " + managedByValue
)

// isOwnedCommand reports whether command is an obot-sentry-managed hook command,
// identified by the `--managed-by obot-sentry` marker. Any command without the marker
// is left untouched. A substring check is sufficient because every command this
// package writes carries the marker in exactly this form; there are no
// pre-existing obot-sentry-owned hook configurations in other layouts to recognize.
func isOwnedCommand(command string) bool {
	return strings.Contains(command, managedMarker)
}

// phase names the hook lifecycle point. These are the exact `--phase` argument
// values accepted by `obot-sentry audit submit`.
type phase string

const (
	phasePostTool phase = "post-tool"
	phaseFailure  phase = "failure"
)

// commandArgs returns the audit-submit arguments for an agent/phase, excluding
// the executable. The `--managed-by obot-sentry` marker is always present and is the
// sole ownership signal used during convergence. No server URL, enrollment
// credential, input-mutation, or debug flag is ever included; hook execution
// owns per-user enrollment and fail-open submission.
func commandArgs(agent localagent.Agent, p phase) []string {
	return []string{
		"audit", "submit",
		"--agent", string(agent),
		"--phase", string(p),
		managedByFlag, managedByValue,
	}
}

func enforceCommandArgs(agent localagent.Agent, event string) []string {
	return []string{
		"enforce",
		"--agent", string(agent),
		"--event", event,
		managedByFlag, managedByValue,
	}
}

// windowsUsesCallOperator reports whether an agent's Windows command runner
// requires the PowerShell call operator (`& "..."`) prefix. Codex and VS Code
// do; Claude Code and Cursor invoke the double-quoted executable directly.
func windowsUsesCallOperator(agent localagent.Agent) bool {
	return agent == localagent.Codex || agent == localagent.VSCode
}

// hookCommand renders the full hook command string for one agent on goos from
// already-built arguments (commandArgs or enforceCommandArgs), quoting the
// executable for the correct command runner:
//
//   - non-Windows: POSIX-shell quoting (single-quote only when needed for
//     spaces, apostrophes, or other unsafe characters), so any path stays one
//     token.
//   - Windows Claude Code / Cursor: a double-quoted executable, invoked directly.
//   - Windows Codex / VS Code: the same double-quoted executable prefixed with
//     the PowerShell call operator `& `.
func hookCommand(exe, goos string, agent localagent.Agent, argv []string) string {
	args := strings.Join(argv, " ")
	if goos == "windows" {
		quoted := quoteWindows(exe)
		if windowsUsesCallOperator(agent) {
			return "& " + quoted + " " + args
		}
		return quoted + " " + args
	}
	return quotePOSIX(exe) + " " + args
}

// posixSafe matches characters that never need quoting in a POSIX shell. A path
// composed only of these can be emitted verbatim, so an ordinary install path
// stays unquoted.
func posixSafe(r rune) bool {
	switch {
	case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return true
	}
	switch r {
	case '_', '@', '%', '+', '=', ':', ',', '.', '/', '-':
		return true
	}
	return false
}

// quotePOSIX renders s as a single POSIX-shell token. Paths made up entirely of
// shell-safe characters are returned verbatim; anything else (spaces,
// apostrophes, Unicode, shell metacharacters) is wrapped in single quotes with
// embedded and escaped single quotes.
func quotePOSIX(s string) string {
	if s != "" {
		safe := true
		for _, r := range s {
			if !posixSafe(r) {
				safe = false
				break
			}
		}
		if safe {
			return s
		}
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// quoteWindows wraps s in double quotes for the Windows command runners. A
// Windows path cannot contain a double quote, so no escaping is required; this
// keeps a path with spaces (e.g. `C:\Program Files\...`) as a single token.
func quoteWindows(s string) string {
	return `"` + s + `"`
}

// packagedExecutable returns the executable location owned by the MDM package
// for goos. Keeping this mapping in the hook generator prevents an invocation
// from an MDM cache or other staging directory from leaving hooks pointed at a
// transient copy of the binary.
func packagedExecutable(goos string) (string, error) {
	switch goos {
	case "darwin":
		return packagedDarwinExecutable, nil
	case "windows":
		return packagedWindowsExecutable, nil
	default:
		return "", errUnsupportedPlatform
	}
}

// DefaultExecutable returns the current platform's MDM-owned executable path
// for embedding in hook configuration. The installer validates that the package
// has placed a usable executable there before changing any hook files.
func DefaultExecutable() (string, error) {
	return packagedExecutable(runtime.GOOS)
}

// validateExecutable rejects an executable path that must not be embedded in a
// machine-managed hook: a missing target, a non-regular file, a non-executable
// file, a file inside a temporary directory, or (on non-Windows platforms) a
// file that is group- or world-writable. Deeper platform-specific ownership
// checks (root-owned on macOS, an administrator-only ACL on Windows) are not
// enforced here; tests inject an already-approved path rather than weakening
// these production checks.
func validateExecutable(path string) error {
	if path == "" {
		return fmt.Errorf("obot-sentry executable path is empty")
	}
	if tmp := os.TempDir(); tmp != "" {
		if rel, err := filepath.Rel(tmp, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return fmt.Errorf("obot-sentry executable %q is inside a temporary directory; install it to a durable location first", path)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("obot-sentry executable %q is not accessible: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("obot-sentry executable %q is not a regular file", path)
	}
	if runtime.GOOS != "windows" {
		perm := info.Mode().Perm()
		// Hooks run as the normal agent users, not as the privileged installer, so
		// the binary must be readable and executable by others — an owner-only
		// (e.g. 0700) binary would pass a bare has-any-exec-bit check yet fail to
		// launch for those users, silently dropping audit events under fail-open.
		if perm&0o005 != 0o005 {
			return fmt.Errorf("obot-sentry executable %q is not readable/executable by normal users (mode %o); hooks run as non-privileged agent users", path, perm)
		}
		if perm&0o022 != 0 {
			return fmt.Errorf("obot-sentry executable %q is writable by non-administrators (mode %o); an MDM-managed hook must point at an admin-owned binary", path, perm)
		}
	}
	return nil
}
