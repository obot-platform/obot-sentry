package hookinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/enforce"
	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

// This file covers the enforcement half of convergence: the pre-tool entries an
// --enforce run adds, and — just as important — everything a run without it must
// leave alone.
//
// It deliberately matches no single production file. Enforcement is one decision
// threaded through three of them — desired.go decides which events to write,
// command.go builds the command that answers them, converge.go merges the entries
// — and the property worth testing is the end-to-end one: with enforcement off,
// not one pre-tool byte changes. Splitting these tests to sit beside each file
// would test the seams and lose that.

// claudeEnforceDarwinGolden is the whole Claude Code document an --enforce
// install writes, so the pre-tool entry is pinned in place rather than probed
// field by field.
const claudeEnforceDarwinGolden = `{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "/usr/local/bin/obot-sentry audit submit --agent claude-code --phase post-tool --managed-by obot-sentry",
            "timeout": 30,
            "statusMessage": "Submitting Obot audit log"
          }
        ]
      }
    ],
    "PostToolUseFailure": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "/usr/local/bin/obot-sentry audit submit --agent claude-code --phase failure --managed-by obot-sentry",
            "timeout": 30,
            "statusMessage": "Submitting Obot audit failure"
          }
        ]
      }
    ],
    "PreToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "/usr/local/bin/obot-sentry enforce --agent claude-code --event PreToolUse --managed-by obot-sentry",
            "timeout": 30,
            "statusMessage": "Checking Obot tool policy"
          }
        ]
      }
    ]
  }
}
`

// cursorEnforceDarwinGolden pins Cursor's two enforcement events and the one
// per-entry field that differs between the phases in this file: failClosed.
const cursorEnforceDarwinGolden = `{
  "version": 1,
  "hooks": {
    "postToolUse": [
      {
        "type": "command",
        "command": "/usr/local/bin/obot-sentry audit submit --agent cursor --phase post-tool --managed-by obot-sentry",
        "timeout": 30,
        "failClosed": false
      }
    ],
    "postToolUseFailure": [
      {
        "type": "command",
        "command": "/usr/local/bin/obot-sentry audit submit --agent cursor --phase failure --managed-by obot-sentry",
        "timeout": 30,
        "failClosed": false
      }
    ],
    "beforeMCPExecution": [
      {
        "type": "command",
        "command": "/usr/local/bin/obot-sentry enforce --agent cursor --event beforeMCPExecution --managed-by obot-sentry",
        "timeout": 30,
        "failClosed": true
      }
    ],
    "preToolUse": [
      {
        "type": "command",
        "command": "/usr/local/bin/obot-sentry enforce --agent cursor --event preToolUse --managed-by obot-sentry",
        "timeout": 30,
        "failClosed": true
      }
    ]
  }
}
`

func TestDesiredEnforceDocumentsGolden(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  any
		want string
	}{
		{"claude darwin", desiredClaude(macExe, "darwin", true), claudeEnforceDarwinGolden},
		{"cursor darwin", desiredCursor(macExe, "darwin", true), cursorEnforceDarwinGolden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := marshalHookJSON(tc.doc)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("desired document mismatch\n--- got ---\n%s\n--- want ---\n%s", got, tc.want)
			}
		})
	}
}

func TestAuditOnlyDocumentsCarryNoPreToolKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  any
		keys []string
	}{
		{"claude darwin", desiredClaude(macExe, "darwin", false), []string{"PreToolUse"}},
		{"claude windows", desiredClaude(winExe, "windows", false), []string{"PreToolUse"}},
		{"cursor darwin", desiredCursor(macExe, "darwin", false), []string{"beforeMCPExecution", "preToolUse"}},
		{"cursor windows", desiredCursor(winExe, "windows", false), []string{"beforeMCPExecution", "preToolUse"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := marshalHookJSON(tc.doc)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range tc.keys {
				if strings.Contains(string(got), key) {
					t.Errorf("an audit-only document carries %q:\n%s", key, got)
				}
			}
			if strings.Contains(string(got), "null") {
				t.Errorf("an audit-only document carries a null:\n%s", got)
			}
			if strings.Contains(string(got), "enforce --agent") {
				t.Errorf("an audit-only document carries an enforcement command:\n%s", got)
			}
		})
	}
}

func TestDesiredCodexEnforce(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			exe := macExe
			want := "/usr/local/bin/obot-sentry enforce --agent codex --event PreToolUse --managed-by obot-sentry"
			if goos == "windows" {
				exe = winExe
				// Codex uses the PowerShell call operator, on both keys.
				want = `& "C:\Program Files\Obot\obot-sentry\obot-sentry.exe" enforce --agent codex --event PreToolUse --managed-by obot-sentry`
			}

			got := desiredCodex(exe, goos, true)
			if len(got.PreToolUse) != 1 || got.PreToolUse[0].Matcher != ".*" {
				t.Fatalf("unexpected PreToolUse groups: %#v", got.PreToolUse)
			}
			inner := got.PreToolUse[0].Hooks
			if len(inner) != 1 {
				t.Fatalf("expected one inner hook, got %#v", inner)
			}
			if inner[0].Command != want {
				t.Errorf("command = %q, want %q", inner[0].Command, want)
			}
			if inner[0].Timeout != hookTimeout || inner[0].StatusMessage != statusMessagePreTool {
				t.Errorf("unexpected timeout/status: %#v", inner[0])
			}
			if goos == "windows" && inner[0].CommandWindows != want {
				t.Errorf("command_windows = %q, want %q", inner[0].CommandWindows, want)
			}
			if goos != "windows" && inner[0].CommandWindows != "" {
				t.Errorf("command_windows set off Windows: %q", inner[0].CommandWindows)
			}

			// An audit-only run leaves the event out entirely, which is what makes
			// the writer skip it.
			if audit := desiredCodex(exe, goos, false); len(audit.PreToolUse) != 0 {
				t.Errorf("an audit-only Codex desired state carries %d pre-tool groups", len(audit.PreToolUse))
			}
		})
	}
}

// The installer's pre-tool event set is pkg/enforce's, per agent. This is the
// lockstep test for the two halves of that split: an event enforce.Run cannot
// answer must never be written into a hook file, and an event it does answer must
// not be silently dropped by a builder that has no field for it.
func TestPreToolEventsTrackEnforce(t *testing.T) {
	for _, agent := range Agents() {
		t.Run(string(agent), func(t *testing.T) {
			var want []string
			for _, event := range enforce.Events(agent) {
				want = append(want, string(event))
			}
			got := preToolEvents(agent, true)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("preToolEvents = %v, want %v", got, want)
			}
			if off := preToolEvents(agent, false); len(off) != 0 {
				t.Errorf("preToolEvents without enforcement = %v, want none", off)
			}

			// Every event the hook implements has to land in the written document,
			// so a builder that gained an event but not a field fails here.
			doc := writtenPreToolKeys(t, agent)
			for _, event := range want {
				if !doc[event] {
					t.Errorf("event %q is not written into the %s hook file", event, agent)
				}
			}
			for event := range doc {
				if !slices.Contains(want, event) {
					t.Errorf("event %q is written but is not one enforce.Events reports", event)
				}
			}
		})
	}
}

// VS Code is out of enforcement scope, so an --enforce run must add nothing to
// its destinations — and that has to hold by construction, not by a special case
// in the installer.
func TestEnforceAddsNothingForVSCode(t *testing.T) {
	if events := enforce.Events(localagent.VSCode); len(events) != 0 {
		t.Fatalf("enforce.Events(vscode) = %v, want none", events)
	}
	if events := preToolEvents(localagent.VSCode, true); len(events) != 0 {
		t.Fatalf("preToolEvents(vscode, true) = %v, want none", events)
	}

	for _, goos := range []string{"darwin", "windows"} {
		for _, d := range Destinations(goos) {
			if d.Agent != localagent.VSCode {
				continue
			}
			audit, err := mergeConfig(d, nil, macExe, goos, false)
			if err != nil {
				t.Fatalf("%s: %v", d.Label, err)
			}
			enforcing, err := mergeConfig(d, nil, macExe, goos, true)
			if err != nil {
				t.Fatalf("%s: %v", d.Label, err)
			}
			if string(audit.data) != string(enforcing.data) {
				t.Errorf("%s (%s) differs under --enforce:\n--- audit ---\n%s\n--- enforce ---\n%s",
					d.Label, goos, audit.data, enforcing.data)
			}
			if strings.Contains(string(enforcing.data), "enforce --agent") {
				t.Errorf("%s (%s) gained an enforcement command:\n%s", d.Label, goos, enforcing.data)
			}
		}
	}
}

func TestAuditOnlyRunLeavesPreToolEntriesUntouched(t *testing.T) {
	for _, tc := range []struct {
		name   string
		agent  localagent.Agent
		format Format
	}{
		{"claude", localagent.ClaudeCode, FormatJSON},
		{"cursor", localagent.Cursor, FormatJSON},
		{"codex", localagent.Codex, FormatTOML},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := destFor(t, tc.agent, tc.format)

			// Converge with enforcement, then again without it.
			enforced, err := mergeConfig(d, nil, macExe, "darwin", true)
			if err != nil {
				t.Fatalf("enforcing merge: %v", err)
			}
			after, err := mergeConfig(d, enforced.data, macExe, "darwin", false)
			if err != nil {
				t.Fatalf("audit-only merge: %v", err)
			}
			if after.write {
				t.Errorf("an audit-only run rewrote a converged enforcing file:\n%s", after.data)
			}
			if after.status != StatusUnchanged {
				t.Errorf("status = %q, want unchanged", after.status)
			}
			if !strings.Contains(string(after.data), "enforce --agent") {
				t.Errorf("the pre-tool entry did not survive an audit-only run:\n%s", after.data)
			}
		})
	}
}

func TestEnforceConvergesExistingPreToolEntries(t *testing.T) {
	d := destFor(t, localagent.ClaudeCode, FormatJSON)
	const stale = `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "/opt/vendor/gatekeeper --check"}]},
      {"matcher": "*", "hooks": [{"type": "command", "command": "/old/path/obot-sentry enforce --agent claude-code --event PreToolUse --managed-by obot-sentry"}]},
      {"matcher": "*", "hooks": [{"type": "command", "command": "/older/obot-sentry enforce --agent claude-code --event PreToolUse --managed-by obot-sentry"}]}
    ]
  }
}
`
	out, err := mergeConfig(d, []byte(stale), macExe, "darwin", true)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if out.status != StatusUpdated {
		t.Errorf("status = %q, want updated", out.status)
	}
	if out.dupes != 1 {
		t.Errorf("dupes = %d, want 1", out.dupes)
	}
	body := string(out.data)
	if !strings.Contains(body, "/opt/vendor/gatekeeper --check") {
		t.Errorf("a third-party pre-tool hook was removed:\n%s", body)
	}
	if strings.Contains(body, "/old/path/obot-sentry") || strings.Contains(body, "/older/obot-sentry") {
		t.Errorf("a stale owned entry survived:\n%s", body)
	}
	if n := strings.Count(body, "--managed-by obot-sentry"); n != 3 {
		t.Errorf("owned entries = %d, want 3 (two audit, one pre-tool):\n%s", n, body)
	}

	// A second enforcing run changes nothing.
	again, err := mergeConfig(d, out.data, macExe, "darwin", true)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if again.write || again.status != StatusUnchanged {
		t.Errorf("second run: write=%v status=%q, want false/unchanged", again.write, again.status)
	}
}

// writtenPreToolKeys converges an agent's hook file with enforcement on and
// returns the event keys that carry an obot-sentry pre-tool entry.
func writtenPreToolKeys(t *testing.T, agent localagent.Agent) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	for _, d := range Destinations("darwin") {
		if d.Agent != agent || d.Format == FormatJSONC {
			continue
		}
		out, err := mergeConfig(d, nil, macExe, "darwin", true)
		if err != nil {
			t.Fatalf("%s: %v", d.Label, err)
		}
		var doc struct {
			Hooks map[string]json.RawMessage `json:"hooks"`
		}
		if d.Format == FormatTOML {
			// The Codex file is TOML; read the event names off the desired state the
			// writer used, and confirm each one appears in the encoded output.
			for _, event := range preToolEvents(agent, true) {
				if strings.Contains(string(out.data), "[[hooks."+event+"]]") {
					keys[event] = true
				}
			}
			continue
		}
		if err := json.Unmarshal(out.data, &doc); err != nil {
			t.Fatalf("%s: %v", d.Label, err)
		}
		for event, raw := range doc.Hooks {
			if strings.Contains(string(raw), "enforce --agent") {
				keys[event] = true
			}
		}
	}
	return keys
}

func TestEnforceCommandQuoting(t *testing.T) {
	for _, tc := range []struct {
		agent localagent.Agent
		goos  string
		exe   string
		want  []string
	}{
		{localagent.ClaudeCode, "darwin", macExe, []string{
			"/usr/local/bin/obot-sentry enforce --agent claude-code --event PreToolUse --managed-by obot-sentry"}},
		{localagent.Codex, "darwin", macExe, []string{
			"/usr/local/bin/obot-sentry enforce --agent codex --event PreToolUse --managed-by obot-sentry"}},
		{localagent.Cursor, "darwin", macExe, []string{
			"/usr/local/bin/obot-sentry enforce --agent cursor --event beforeMCPExecution --managed-by obot-sentry",
			"/usr/local/bin/obot-sentry enforce --agent cursor --event preToolUse --managed-by obot-sentry"}},
		{localagent.ClaudeCode, "windows", winExe, []string{
			`& "C:\Program Files\Obot\obot-sentry\obot-sentry.exe" enforce --agent claude-code --event PreToolUse --managed-by obot-sentry`}},
		{localagent.Codex, "windows", winExe, []string{
			`& "C:\Program Files\Obot\obot-sentry\obot-sentry.exe" enforce --agent codex --event PreToolUse --managed-by obot-sentry`}},
		{localagent.Cursor, "windows", winExe, []string{
			`"C:\Program Files\Obot\obot-sentry\obot-sentry.exe" enforce --agent cursor --event beforeMCPExecution --managed-by obot-sentry`,
			`"C:\Program Files\Obot\obot-sentry\obot-sentry.exe" enforce --agent cursor --event preToolUse --managed-by obot-sentry`}},
	} {
		t.Run(string(tc.agent)+"/"+tc.goos, func(t *testing.T) {
			events := preToolEvents(tc.agent, true)
			if len(events) != len(tc.want) {
				t.Fatalf("events = %v, want %d commands", events, len(tc.want))
			}
			for i, event := range events {
				got := hookCommand(tc.exe, tc.goos, tc.agent, enforceCommandArgs(tc.agent, event))
				if got != tc.want[i] {
					t.Errorf("command = %q, want %q", got, tc.want[i])
				}
			}
		})
	}
}

func TestRunEnforceEndToEnd(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}

	newInstaller := func(enforce bool, out *bytes.Buffer) *Installer {
		return &Installer{
			GOOS:                "darwin",
			Privilege:           func() error { return nil },
			ResolveExe:          func() (string, error) { return macExe, nil },
			ResolveUser:         func() (*TargetUser, error) { return user, nil },
			ProvisionIdentity:   func() (string, error) { return "/Library/Application Support/obot/obot-sentry", nil },
			ResolveDestinations: tempDestinations(machineRoot),
			Enforce:             enforce,
			Out:                 out,
		}
	}

	claudeFile := filepath.Join(home, ".claude/settings.json")
	codexFile := filepath.Join(machineRoot, "etc/codex/requirements.toml")
	cursorFile := filepath.Join(machineRoot, "Cursor/hooks.json")
	vscodeFile := filepath.Join(home, ".copilot/hooks/obot-sentry.json")

	var out bytes.Buffer
	if err := newInstaller(true, &out).Run(context.Background()); err != nil {
		t.Fatalf("enforcing run: %v", err)
	}
	assertNoSecrets(t, out.String())

	for _, tc := range []struct {
		path string
		want []string
	}{
		{claudeFile, []string{`"PreToolUse"`, "enforce --agent claude-code --event PreToolUse --managed-by obot-sentry"}},
		{codexFile, []string{"[[hooks.PreToolUse]]", "enforce --agent codex --event PreToolUse --managed-by obot-sentry"}},
		{cursorFile, []string{
			`"beforeMCPExecution"`, `"preToolUse"`,
			"enforce --agent cursor --event beforeMCPExecution --managed-by obot-sentry",
			"enforce --agent cursor --event preToolUse --managed-by obot-sentry",
		}},
	} {
		data, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("read %s: %v", tc.path, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s missing %q:\n%s", tc.path, want, data)
			}
		}
	}

	// The Cursor file must carry both failClosed values: false on the audit
	// entries, true on the enforcement ones.
	cursor, err := os.ReadFile(cursorFile)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(cursor), `"failClosed": true`); n != 2 {
		t.Errorf("failClosed true appears %d times, want 2:\n%s", n, cursor)
	}
	if n := strings.Count(string(cursor), `"failClosed": false`); n != 2 {
		t.Errorf("failClosed false appears %d times, want 2:\n%s", n, cursor)
	}

	// VS Code is out of enforcement scope: its file carries the audit entry only.
	vscode, err := os.ReadFile(vscodeFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(vscode), "enforce --agent") || strings.Contains(string(vscode), "PreToolUse\": [\n") {
		t.Errorf("the VS Code hook file gained an enforcement entry:\n%s", vscode)
	}
	if !strings.Contains(string(vscode), "audit submit --agent vscode") {
		t.Errorf("the VS Code audit entry did not survive:\n%s", vscode)
	}
	settings, err := os.ReadFile(filepath.Join(home, "Library/Application Support/Code/User/settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), "chat.hookFilesLocations") || strings.Contains(string(settings), "enforce") {
		t.Errorf("the VS Code settings merge changed under --enforce:\n%s", settings)
	}

	// A second enforcing run changes nothing.
	var again bytes.Buffer
	if err := newInstaller(true, &again).Run(context.Background()); err != nil {
		t.Fatalf("second enforcing run: %v", err)
	}
	if !strings.Contains(again.String(), string(StatusUnchanged)) {
		t.Errorf("second enforcing run was not idempotent:\n%s", again.String())
	}

	// And a run without enforcement leaves the pre-tool entries alone: this is
	// what makes turning enforcement off safe, since the backend allows
	// unconditionally.
	before := map[string][]byte{}
	for _, p := range []string{claudeFile, codexFile, cursorFile, vscodeFile} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		before[p] = data
	}
	var auditOnly bytes.Buffer
	if err := newInstaller(false, &auditOnly).Run(context.Background()); err != nil {
		t.Fatalf("audit-only run: %v", err)
	}
	for p, want := range before {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("an audit-only run changed %s:\n--- before ---\n%s\n--- after ---\n%s", p, want, got)
		}
	}
}
