package hookinstall

import (
	"os"
	"strings"
)

// This file centralizes desired-state construction in typed builders so hook
// event names, phases, quoting, timeouts, and status messages are defined once
// and reused by both the golden tests and the per-agent config writers. The
// builders take a resolved executable path plus the target GOOS and emit the
// exact managed entry each agent expects.

const (
	statusMessagePostTool = "Submitting Obot audit log"
	claudeStatusFailure   = "Submitting Obot audit failure"
)

// --- Claude Code: nested JSON (matcher group -> inner command hooks) ---

type claudeInnerHook struct {
	Type          string `json:"type"`
	Command       string `json:"command"`
	Timeout       int    `json:"timeout"`
	StatusMessage string `json:"statusMessage"`
}

type claudeMatcherGroup struct {
	Matcher string            `json:"matcher"`
	Hooks   []claudeInnerHook `json:"hooks"`
}

type claudeHooks struct {
	PostToolUse        []claudeMatcherGroup `json:"PostToolUse"`
	PostToolUseFailure []claudeMatcherGroup `json:"PostToolUseFailure"`
}

type claudeDocument struct {
	Hooks claudeHooks `json:"hooks"`
}

func desiredClaude(exe, goos string) claudeDocument {
	group := func(p phase, status string) []claudeMatcherGroup {
		return []claudeMatcherGroup{{
			Matcher: "*",
			Hooks: []claudeInnerHook{{
				Type:          "command",
				Command:       hookCommand(exe, goos, AgentClaudeCode, p),
				Timeout:       hookTimeout,
				StatusMessage: status,
			}},
		}}
	}
	return claudeDocument{Hooks: claudeHooks{
		PostToolUse:        group(phasePostTool, statusMessagePostTool),
		PostToolUseFailure: group(phaseFailure, claudeStatusFailure),
	}}
}

// --- Cursor: direct JSON entries with failClosed and a forced version ---

type cursorHook struct {
	Type       string `json:"type"`
	Command    string `json:"command"`
	Timeout    int    `json:"timeout"`
	FailClosed bool   `json:"failClosed"`
}

type cursorHooks struct {
	PostToolUse        []cursorHook `json:"postToolUse"`
	PostToolUseFailure []cursorHook `json:"postToolUseFailure"`
}

type cursorDocument struct {
	Version int         `json:"version"`
	Hooks   cursorHooks `json:"hooks"`
}

// cursorVersion is the only supported Cursor hooks schema version; the writer
// forces this value on convergence.
const cursorVersion = 1

func desiredCursor(exe, goos string) cursorDocument {
	entry := func(p phase) []cursorHook {
		return []cursorHook{{
			Type:       "command",
			Command:    hookCommand(exe, goos, AgentCursor, p),
			Timeout:    hookTimeout,
			FailClosed: false,
		}}
	}
	return cursorDocument{
		Version: cursorVersion,
		Hooks: cursorHooks{
			PostToolUse:        entry(phasePostTool),
			PostToolUseFailure: entry(phaseFailure),
		},
	}
}

// --- Visual Studio Code: dedicated obot-sentry.json, direct PostToolUse entry ---

type vscodeHook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type vscodeHooks struct {
	PostToolUse []vscodeHook `json:"PostToolUse"`
}

type vscodeDocument struct {
	Hooks vscodeHooks `json:"hooks"`
}

func desiredVSCode(exe, goos string) vscodeDocument {
	return vscodeDocument{Hooks: vscodeHooks{
		PostToolUse: []vscodeHook{{
			Type:    "command",
			Command: hookCommand(exe, goos, AgentVSCode, phasePostTool),
			Timeout: hookTimeout,
		}},
	}}
}

// --- Codex: [features] hooks=true plus a nested array-of-tables group ---
//
// The Codex desired state is modeled as a typed structure rather than a
// serialized TOML document: the merge writer decodes the existing
// requirements.toml, applies these values, and re-encodes the whole file. On
// Windows both command and command_windows carry the same call-operator form.

type codexInnerHook struct {
	Type    string
	Command string
	// CommandWindows mirrors Command on Windows so the PowerShell call-operator
	// form is written to both keys; it is empty on other platforms.
	CommandWindows string
	Timeout        int
	StatusMessage  string
}

type codexHookGroup struct {
	Matcher string
	Hooks   []codexInnerHook
}

type codexDesired struct {
	// FeaturesHooks is the value forced into [features].hooks.
	FeaturesHooks bool
	PostToolUse   []codexHookGroup
}

func desiredCodex(exe, goos string) codexDesired {
	cmd := hookCommand(exe, goos, AgentCodex, phasePostTool)
	inner := codexInnerHook{
		Type:          "command",
		Command:       cmd,
		Timeout:       hookTimeout,
		StatusMessage: statusMessagePostTool,
	}
	if goos == "windows" {
		inner.CommandWindows = cmd
	}
	return codexDesired{
		FeaturesHooks: true,
		PostToolUse: []codexHookGroup{{
			Matcher: ".*",
			Hooks:   []codexInnerHook{inner},
		}},
	}
}

// --- Visual Studio Code user settings: chat.hookFilesLocations values ---

// settingValue is one key/value pair to merge into an existing settings object,
// modeled as an ordered slice so the desired set is deterministic and the merge
// writer can insert missing keys in a stable order without disturbing custom
// locations the operator already configured.
type settingValue struct {
	Key   string
	Value bool
}

// desiredVSCodeHookLocations returns the values obot-sentry merges under
// chat.hookFilesLocations: enable the dedicated Copilot hook directory and
// disable all three default Claude hook locations so VS Code does not also fire
// the Claude Code hook and produce duplicate, mislabeled audit events.
func desiredVSCodeHookLocations() []settingValue {
	return []settingValue{
		{Key: "~/.copilot/hooks", Value: true},
		{Key: ".claude/settings.json", Value: false},
		{Key: ".claude/settings.local.json", Value: false},
		{Key: "~/.claude/settings.json", Value: false},
	}
}

// vscodeSettingsDocument is the whole-document shape used only when writing a
// brand-new VS Code settings file: a single chat.hookFilesLocations object
// holding the obot-sentry-owned values. An existing file is edited through the JSONC
// syntax tree instead so unrelated settings, comments, and formatting survive.
type vscodeSettingsDocument struct {
	HookFilesLocations map[string]bool `json:"chat.hookFilesLocations"`
}

// newVSCodeSettings builds the desired document for a new VS Code settings file.
func newVSCodeSettings() vscodeSettingsDocument {
	m := make(map[string]bool, 4)
	for _, sv := range desiredVSCodeHookLocations() {
		m[sv.Key] = sv.Value
	}
	return vscodeSettingsDocument{HookFilesLocations: m}
}

// --- Destinations: static per-OS model of every managed file ---

// Destination describes one managed configuration file as a static, per-OS
// model. Machine-scoped destinations carry an absolute Abs path; user-scoped
// destinations carry a slash-separated Rel path that the caller resolves
// against the active console user's home. This model performs no path
// verification or active-user resolution of its own.
type Destination struct {
	Agent  Agent
	Label  string
	Scope  Scope
	Format Format
	Abs    string // set for ScopeMachine
	Rel    string // set for ScopeUser (slash-separated, relative to home)
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
			{Agent: AgentClaudeCode, Label: AgentClaudeCode.DisplayName(), Scope: ScopeUser, Format: FormatJSON, Rel: ".claude/settings.json"},
			{Agent: AgentCodex, Label: AgentCodex.DisplayName(), Scope: ScopeMachine, Format: FormatTOML, Abs: "/etc/codex/requirements.toml"},
			{Agent: AgentVSCode, Label: AgentVSCode.DisplayName(), Scope: ScopeUser, Format: FormatJSON, Rel: ".copilot/hooks/obot-sentry.json"},
			{Agent: AgentCursor, Label: AgentCursor.DisplayName(), Scope: ScopeMachine, Format: FormatJSON, Abs: "/Library/Application Support/Cursor/hooks.json"},
			{Agent: AgentVSCode, Label: "VS Code settings", Scope: ScopeUser, Format: FormatJSONC, Rel: "Library/Application Support/Code/User/settings.json"},
		}
	case "windows":
		pd := windowsProgramData()
		return []Destination{
			{Agent: AgentClaudeCode, Label: AgentClaudeCode.DisplayName(), Scope: ScopeUser, Format: FormatJSON, Rel: ".claude/settings.json"},
			{Agent: AgentCodex, Label: AgentCodex.DisplayName(), Scope: ScopeMachine, Format: FormatTOML, Abs: winJoin(pd, "OpenAI", "Codex", "requirements.toml")},
			{Agent: AgentVSCode, Label: AgentVSCode.DisplayName(), Scope: ScopeUser, Format: FormatJSON, Rel: ".copilot/hooks/obot-sentry.json"},
			{Agent: AgentCursor, Label: AgentCursor.DisplayName(), Scope: ScopeMachine, Format: FormatJSON, Abs: winJoin(pd, "Cursor", "hooks.json")},
			{Agent: AgentVSCode, Label: "VS Code settings", Scope: ScopeUser, Format: FormatJSONC, Rel: "AppData/Roaming/Code/User/settings.json"},
		}
	default:
		return nil
	}
}
