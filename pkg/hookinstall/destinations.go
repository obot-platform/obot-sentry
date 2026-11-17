package hookinstall

import (
	"fmt"
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
	// Retired marks a file obot-sentry used to install hooks into and no longer
	// does. Install removes the owned hooks from it, exactly as uninstall does,
	// and never writes new ones, so an upgraded machine does not keep running a
	// hook from both the old location and the new one.
	Retired bool
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

// kiroHookRel is the dedicated Kiro hook file. Kiro loads every *.json under
// ~/.kiro/hooks for every workspace, on both platforms, so one owned file there
// covers the user without touching hooks they wrote themselves.
const kiroHookRel = ".kiro/hooks/obot-sentry.json"

// Claude Code's hooks live in its managed settings file rather than the user's
// ~/.claude/settings.json. Claude Code reads user settings from
// $CLAUDE_CONFIG_DIR when that is set, so a hook in the user file is skipped by
// any session started with a different configuration directory. Managed settings
// are read from a fixed machine path whatever that variable holds, are not
// writable by the user, and keep running when an administrator sets
// allowManagedHooksOnly.
const (
	claudeManagedSettingsDarwin = "/Library/Application Support/ClaudeCode/managed-settings.json"
	// claudeUserSettingsRel is where the hooks were installed before they moved
	// to managed settings. It stays as a retired destination so the old hooks are
	// removed.
	claudeUserSettingsRel   = ".claude/settings.json"
	claudeUserSettingsLabel = "Claude Code (user settings)"
)

// Destinations returns the full, ordered set of managed destinations for goos:
// the five agent hook files, the VS Code user-settings file, and the retired
// Claude Code user-settings file. Only the darwin and windows layouts are
// defined; other platforms return nil and are rejected earlier by the platform
// check.
func Destinations(goos string) []Destination {
	switch goos {
	case "darwin":
		return []Destination{
			{Agent: localagent.ClaudeCode, Label: localagent.ClaudeCode.DisplayName(), Scope: ScopeMachine, Format: FormatJSON, Abs: claudeManagedSettingsDarwin},
			{Agent: localagent.ClaudeCode, Label: claudeUserSettingsLabel, Scope: ScopeUser, Format: FormatJSON, Rel: claudeUserSettingsRel, Retired: true},
			{Agent: localagent.Codex, Label: localagent.Codex.DisplayName(), Scope: ScopeMachine, Format: FormatTOML, Abs: "/etc/codex/requirements.toml"},
			{Agent: localagent.VSCode, Label: localagent.VSCode.DisplayName(), Scope: ScopeUser, Format: FormatJSON, Rel: ".copilot/hooks/obot-sentry.json"},
			{Agent: localagent.Cursor, Label: localagent.Cursor.DisplayName(), Scope: ScopeMachine, Format: FormatJSON, Abs: "/Library/Application Support/Cursor/hooks.json"},
			{Agent: localagent.VSCode, Label: "VS Code settings", Scope: ScopeUser, Format: FormatJSONC, Rel: "Library/Application Support/Code/User/settings.json"},
			{Agent: localagent.Kiro, Label: localagent.Kiro.DisplayName(), Scope: ScopeUser, Format: FormatJSON, Rel: kiroHookRel},
		}
	case "windows":
		pd := windowsProgramData()
		return []Destination{
			{Agent: localagent.ClaudeCode, Label: localagent.ClaudeCode.DisplayName(), Scope: ScopeMachine, Format: FormatJSON, Abs: winJoin(windowsProgramFiles(), "ClaudeCode", "managed-settings.json")},
			{Agent: localagent.ClaudeCode, Label: claudeUserSettingsLabel, Scope: ScopeUser, Format: FormatJSON, Rel: claudeUserSettingsRel, Retired: true},
			{Agent: localagent.Codex, Label: localagent.Codex.DisplayName(), Scope: ScopeMachine, Format: FormatTOML, Abs: winJoin(pd, "OpenAI", "Codex", "requirements.toml")},
			{Agent: localagent.VSCode, Label: localagent.VSCode.DisplayName(), Scope: ScopeUser, Format: FormatJSON, Rel: ".copilot/hooks/obot-sentry.json"},
			{Agent: localagent.Cursor, Label: localagent.Cursor.DisplayName(), Scope: ScopeMachine, Format: FormatJSON, Abs: winJoin(pd, "Cursor", "hooks.json")},
			{Agent: localagent.VSCode, Label: "VS Code settings", Scope: ScopeUser, Format: FormatJSONC, Rel: "AppData/Roaming/Code/User/settings.json"},
			{Agent: localagent.Kiro, Label: localagent.Kiro.DisplayName(), Scope: ScopeUser, Format: FormatJSON, Rel: kiroHookRel},
		}
	default:
		return nil
	}
}
