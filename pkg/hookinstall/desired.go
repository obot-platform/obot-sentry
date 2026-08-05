package hookinstall

import (
	"slices"
	"strings"

	"github.com/obot-platform/obot-sentry/pkg/enforce"
	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

const (
	statusMessagePostTool = "Submitting Obot audit log"
	claudeStatusFailure   = "Submitting Obot audit failure"
	statusMessagePreTool  = "Checking Obot tool policy"
)

func preToolEvents(agent localagent.Agent, enforcing bool) []string {
	if !enforcing {
		return nil
	}
	events := enforce.Events(agent)
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, string(event))
	}
	return out
}

// --- Claude Code: nested JSON (matcher group -> inner command hooks) ---

type claudeInnerHook struct {
	Type          string `json:"type"`
	Command       string `json:"command"`
	Shell         string `json:"shell,omitempty"`
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
	// PreToolUse is omitted entirely when this run is not installing enforcement.
	PreToolUse []claudeMatcherGroup `json:"PreToolUse,omitempty"`
}

type claudeDocument struct {
	Hooks claudeHooks `json:"hooks"`
}

func desiredClaude(exe, goos string, enforcing bool) claudeDocument {
	matcherGroup := func(command, status string) claudeMatcherGroup {
		shell := ""
		if goos == "windows" {
			shell = "powershell"
		}
		return claudeMatcherGroup{
			Matcher: "*",
			Hooks: []claudeInnerHook{{
				Type:          "command",
				Command:       command,
				Shell:         shell,
				Timeout:       hookTimeout,
				StatusMessage: status,
			}},
		}
	}
	audit := func(p phase, status string) []claudeMatcherGroup {
		return []claudeMatcherGroup{
			matcherGroup(hookCommand(exe, goos, localagent.ClaudeCode, commandArgs(localagent.ClaudeCode, p)), status),
		}
	}
	doc := claudeDocument{Hooks: claudeHooks{
		PostToolUse:        audit(phasePostTool, statusMessagePostTool),
		PostToolUseFailure: audit(phaseFailure, claudeStatusFailure),
	}}
	for _, event := range preToolEvents(localagent.ClaudeCode, enforcing) {
		doc.Hooks.PreToolUse = append(doc.Hooks.PreToolUse, matcherGroup(
			hookCommand(exe, goos, localagent.ClaudeCode, enforceCommandArgs(localagent.ClaudeCode, event)),
			statusMessagePreTool))
	}
	return doc
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
	// The two enforcement events, omitted when not installing enforcement (see
	// claudeHooks.PreToolUse).
	BeforeMCPExecution []cursorHook `json:"beforeMCPExecution,omitempty"`
	PreToolUse         []cursorHook `json:"preToolUse,omitempty"`
}

type cursorDocument struct {
	Version int         `json:"version"`
	Hooks   cursorHooks `json:"hooks"`
}

// cursorVersion is the only supported Cursor hooks schema version; the writer
// forces this value on convergence.
const cursorVersion = 1

func desiredCursor(exe, goos string, enforcing bool) cursorDocument {
	// failClosed is the one per-entry field that differs between the two phases in
	// this file. An audit hook that cannot launch must not block the user's work;
	// an enforcement hook that cannot launch must block, because a hook that
	// silently does not run is a control that is not there.
	entry := func(argv []string, failClosed bool) cursorHook {
		return cursorHook{
			Type:       "command",
			Command:    hookCommand(exe, goos, localagent.Cursor, argv),
			Timeout:    hookTimeout,
			FailClosed: failClosed,
		}
	}
	audit := func(p phase) []cursorHook {
		return []cursorHook{entry(commandArgs(localagent.Cursor, p), false)}
	}
	doc := cursorDocument{
		Version: cursorVersion,
		Hooks: cursorHooks{
			PostToolUse:        audit(phasePostTool),
			PostToolUseFailure: audit(phaseFailure),
		},
	}
	for _, event := range preToolEvents(localagent.Cursor, enforcing) {
		hook := entry(enforceCommandArgs(localagent.Cursor, event), true)
		switch enforce.Event(event) {
		case enforce.EventCursorBeforeMCPExecution:
			doc.Hooks.BeforeMCPExecution = append(doc.Hooks.BeforeMCPExecution, hook)
		case enforce.EventCursorPreToolUse:
			doc.Hooks.PreToolUse = append(doc.Hooks.PreToolUse, hook)
		}
	}
	return doc
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

// desiredVSCode takes no enforcement argument: VS Code is out of enforcement
// scope, so there is no pre-tool field for a value to go into. Its post-tool
// audit entry is unaffected by an --enforce run.
func desiredVSCode(exe, goos string) vscodeDocument {
	return vscodeDocument{Hooks: vscodeHooks{
		PostToolUse: []vscodeHook{{
			Type:    "command",
			Command: hookCommand(exe, goos, localagent.VSCode, commandArgs(localagent.VSCode, phasePostTool)),
			Timeout: hookTimeout,
		}},
	}}
}

// --- Codex: pinned [features] values plus a nested array-of-tables group ---
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

// codexFeaturePin is one [features] key obot-sentry forces in
// requirements.toml, modeled as an ordered slice for the same reason as
// settingValue: the desired set is deterministic and reviewable in one place.
type codexFeaturePin struct {
	Key   string
	Value bool
}

const (
	codexFeatureHooks                  = "hooks"
	codexFeatureNonPrefixedMCPToolName = "non_prefixed_mcp_tool_names"
)

// codexFeaturePins returns the [features] values obot-sentry forces.
//
// A value in requirements.toml is a PIN, not a default: Codex overwrites whatever
// config.toml said with it (ManagedFeatures.normalize_candidate), and every
// runtime toggle is validated against it, so a user cannot change these. There is
// no user-writable requirements layer — the sources are /etc/codex, the enterprise
// cloud bundle, legacy managed_config.toml, and macOS managed preferences — which
// is what makes pinning a control rather than a suggestion.
//
//   - hooks must be on or no hook fires and there is no enforcement at all. It is
//     already Codex's default; pinning it stops a user turning it off.
//   - non_prefixed_mcp_tool_names must be off. It makes the tools of the servers
//     named in its server_names list drop the mcp__ prefix, which is the only
//     thing marking a call as MCP: obot-sentry would classify those calls by the
//     built-in-tool heuristics instead, so an MCP call would be judged against the
//     built-in-tools toggle rather than the MCP allowlist — and a server named
//     "reader" would be reported as a file read. ~/.codex/config.toml is
//     user-writable, so without this pin that is self-service. Off is also Codex's
//     default today, so this guards against a user enabling it and against the
//     default changing as the feature leaves development.
func codexFeaturePins() []codexFeaturePin {
	return []codexFeaturePin{
		{Key: codexFeatureHooks, Value: true},
		{Key: codexFeatureNonPrefixedMCPToolName, Value: false},
	}
}

type codexDesired struct {
	// Features are the [features] values forced into requirements.toml.
	Features    []codexFeaturePin
	PostToolUse []codexHookGroup
	// PreToolUse is empty when not installing enforcement, so the writer leaves
	// that event's array-of-tables alone.
	PreToolUse []codexHookGroup
}

func desiredCodex(exe, goos string, enforcing bool) codexDesired {
	group := func(argv []string, status string) codexHookGroup {
		cmd := hookCommand(exe, goos, localagent.Codex, argv)
		inner := codexInnerHook{
			Type:          "command",
			Command:       cmd,
			Timeout:       hookTimeout,
			StatusMessage: status,
		}
		if goos == "windows" {
			inner.CommandWindows = cmd
		}
		return codexHookGroup{Matcher: ".*", Hooks: []codexInnerHook{inner}}
	}
	desired := codexDesired{
		Features: codexFeaturePins(),
		PostToolUse: []codexHookGroup{
			group(commandArgs(localagent.Codex, phasePostTool), statusMessagePostTool),
		},
	}
	for _, event := range preToolEvents(localagent.Codex, enforcing) {
		desired.PreToolUse = append(desired.PreToolUse,
			group(enforceCommandArgs(localagent.Codex, event), statusMessagePreTool))
	}
	return desired
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

type vscodeHookLocation struct {
	Path   string
	Source string
}

// vscodeDefaultHookLocations is VS Code's shipped set of hook source folders,
// transcribed from the source-folder table in Visual Studio Code 1.130.0
// (Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js). VS Code
// derives the default value of chat.hookFilesLocations from this table by
// enabling every entry in it, so every path here is read unless a setting turns
// it off.
var vscodeDefaultHookLocations = []vscodeHookLocation{
	{Path: ".github/hooks", Source: "github-workspace"},
	{Path: ".claude/settings.local.json", Source: "claude-workspace-local"},
	{Path: ".claude/settings.json", Source: "claude-workspace"},
	{Path: "~/.copilot/hooks", Source: "copilot-personal"},
	{Path: "~/.claude/settings.json", Source: "claude-personal"},
}

// vscodeOwnHookLocation is the VS Code hook location obot-sentry writes its own
// hook file into; the file itself is a destination (see Destinations).
const vscodeOwnHookLocation = "~/.copilot/hooks"

// claudeHookSourcePrefix marks a VS Code hook location that holds Claude Code's
// hook definitions. VS Code tags each location with its owner, and the Claude
// ones are the "claude-" family.
const claudeHookSourcePrefix = "claude-"

// desiredVSCodeHookLocations returns the values obot-sentry merges under
// chat.hookFilesLocations, derived from VS Code's own default set rather than
// listed out: enable the location holding our hook file, and disable every
// default location that holds Claude Code's.
//
// The exclusions are not a preference. obot-sentry installs a Claude Code hook
// into ~/.claude/settings.json, which VS Code reads by default — so left alone,
// VS Code fires our Claude Code hook as well as our VS Code one, and the same
// call is audited twice, once under the wrong agent. VS Code ships this same
// override for its own Agents Window, as the agentsWindow default of
// chat.hookFilesLocations.
//
// Deriving beats listing here for two reasons: a Claude location added by a
// later VS Code release is covered by the rule rather than silently missed, and
// .github/hooks is left enabled for a stated reason — it is Copilot's own
// location, not Claude's — rather than by omission.
func desiredVSCodeHookLocations() []settingValue {
	disable := make([]string, 0, len(vscodeDefaultHookLocations))
	for _, loc := range vscodeDefaultHookLocations {
		if strings.HasPrefix(loc.Source, claudeHookSourcePrefix) {
			disable = append(disable, loc.Path)
		}
	}
	// Sorted rather than kept in VS Code's table order: this order is the order
	// missing keys are appended to a user's settings file, so it should stay put
	// even if a VS Code release reorders its own table.
	slices.Sort(disable)

	out := make([]settingValue, 0, len(disable)+1)
	out = append(out, settingValue{Key: vscodeOwnHookLocation, Value: true})
	for _, path := range disable {
		out = append(out, settingValue{Key: path, Value: false})
	}
	return out
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
