package hookinstall

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

type Destination struct {
	Agent  Agent
	Label  string
	Scope  Scope
	Format Format
	Abs    string // set for ScopeMachine
	Rel    string // set for ScopeUser (slash-separated, relative to home)
}

// ResolvePath turns a destination model into the concrete path to converge on this
// machine. A machine-scoped destination is already absolute; a user-scoped one is
// resolved against the active console user's home, which is why there is no
// resolution without one.
func (d Destination) ResolvePath(u *TargetUser) (string, error) {
	switch d.Scope {
	case ScopeMachine:
		return d.Abs, nil
	case ScopeUser:
		if u == nil || u.HomeDir == "" {
			return "", fmt.Errorf("cannot resolve user-scoped destination %q without an active console user", d.Label)
		}
		return filepath.Join(u.HomeDir, filepath.FromSlash(d.Rel)), nil
	default:
		return "", fmt.Errorf("destination %q has unknown scope %q", d.Label, d.Scope)
	}
}

// windowsProgramData resolves the machine configuration root used by Codex and
// Cursor on Windows, falling back to the conventional path when the environment
// variable is unset (for example when modeling Windows destinations from a test
// on another OS).
func windowsProgramData() string {
	if pd := os.Getenv("ProgramData"); pd != "" {
		return pd
	}
	return `C:\ProgramData`
}

// winJoin joins Windows path components with backslashes, trimming any trailing
// separator on base. It is used to model Windows destinations deterministically
// regardless of the host OS the model is built on (filepath.Join would emit
// forward slashes when run on non-Windows).
func winJoin(base string, parts ...string) string {
	out := strings.TrimRight(base, `\`)
	for _, p := range parts {
		out += `\` + p
	}
	return out
}

// Destinations returns the full, ordered set of managed destinations for goos:
// the four agent hook files plus the VS Code user-settings file. Only the
// darwin and windows layouts are defined; other platforms return nil and are
// rejected earlier by the platform check.
func Destinations(goos string) []Destination {
	switch goos {
	case "darwin":
		return []Destination{
			{Agent: localagent.ClaudeCode, Label: localagent.ClaudeCode.DisplayName(), Scope: ScopeUser, Format: FormatJSON, Rel: ".claude/settings.json"},
			{Agent: localagent.Codex, Label: localagent.Codex.DisplayName(), Scope: ScopeMachine, Format: FormatTOML, Abs: "/etc/codex/requirements.toml"},
			{Agent: localagent.VSCode, Label: localagent.VSCode.DisplayName(), Scope: ScopeUser, Format: FormatJSON, Rel: ".copilot/hooks/obot-sentry.json"},
			{Agent: localagent.Cursor, Label: localagent.Cursor.DisplayName(), Scope: ScopeMachine, Format: FormatJSON, Abs: "/Library/Application Support/Cursor/hooks.json"},
			{Agent: localagent.VSCode, Label: "VS Code settings", Scope: ScopeUser, Format: FormatJSONC, Rel: "Library/Application Support/Code/User/settings.json"},
		}
	case "windows":
		pd := windowsProgramData()
		return []Destination{
			{Agent: localagent.ClaudeCode, Label: localagent.ClaudeCode.DisplayName(), Scope: ScopeUser, Format: FormatJSON, Rel: ".claude/settings.json"},
			{Agent: localagent.Codex, Label: localagent.Codex.DisplayName(), Scope: ScopeMachine, Format: FormatTOML, Abs: winJoin(pd, "OpenAI", "Codex", "requirements.toml")},
			{Agent: localagent.VSCode, Label: localagent.VSCode.DisplayName(), Scope: ScopeUser, Format: FormatJSON, Rel: ".copilot/hooks/obot-sentry.json"},
			{Agent: localagent.Cursor, Label: localagent.Cursor.DisplayName(), Scope: ScopeMachine, Format: FormatJSON, Abs: winJoin(pd, "Cursor", "hooks.json")},
			{Agent: localagent.VSCode, Label: "VS Code settings", Scope: ScopeUser, Format: FormatJSONC, Rel: "AppData/Roaming/Code/User/settings.json"},
		}
	default:
		return nil
	}
}
