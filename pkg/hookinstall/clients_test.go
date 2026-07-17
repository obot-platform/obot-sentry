package hookinstall

import (
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/audit"
)

const (
	macExe = packagedDarwinExecutable
	winExe = packagedWindowsExecutable
)

// The golden documents below are the production desired state.
// They are byte-exact so the new-file serializer's
// formatting (two-space indent, trailing newline, literal `&` with no HTML
// escaping) is locked down.

const claudeDarwinGolden = `{
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
    ]
  }
}
`

const claudeWindowsGolden = `{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "\"C:\\Program Files\\Obot\\obot-sentry\\obot-sentry.exe\" audit submit --agent claude-code --phase post-tool --managed-by obot-sentry",
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
            "command": "\"C:\\Program Files\\Obot\\obot-sentry\\obot-sentry.exe\" audit submit --agent claude-code --phase failure --managed-by obot-sentry",
            "timeout": 30,
            "statusMessage": "Submitting Obot audit failure"
          }
        ]
      }
    ]
  }
}
`

const cursorDarwinGolden = `{
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
    ]
  }
}
`

const cursorWindowsGolden = `{
  "version": 1,
  "hooks": {
    "postToolUse": [
      {
        "type": "command",
        "command": "\"C:\\Program Files\\Obot\\obot-sentry\\obot-sentry.exe\" audit submit --agent cursor --phase post-tool --managed-by obot-sentry",
        "timeout": 30,
        "failClosed": false
      }
    ],
    "postToolUseFailure": [
      {
        "type": "command",
        "command": "\"C:\\Program Files\\Obot\\obot-sentry\\obot-sentry.exe\" audit submit --agent cursor --phase failure --managed-by obot-sentry",
        "timeout": 30,
        "failClosed": false
      }
    ]
  }
}
`

const vscodeDarwinGolden = `{
  "hooks": {
    "PostToolUse": [
      {
        "type": "command",
        "command": "/usr/local/bin/obot-sentry audit submit --agent vscode --phase post-tool --managed-by obot-sentry",
        "timeout": 30
      }
    ]
  }
}
`

// vscodeWindowsGolden keeps the PowerShell call operator (`&`) as a literal
// character, proving the new-file serializer does not HTML-escape it.
const vscodeWindowsGolden = `{
  "hooks": {
    "PostToolUse": [
      {
        "type": "command",
        "command": "& \"C:\\Program Files\\Obot\\obot-sentry\\obot-sentry.exe\" audit submit --agent vscode --phase post-tool --managed-by obot-sentry",
        "timeout": 30
      }
    ]
  }
}
`

func TestDesiredJSONDocumentsGolden(t *testing.T) {
	tests := []struct {
		name string
		doc  any
		want string
	}{
		{"claude darwin", desiredClaude(macExe, "darwin"), claudeDarwinGolden},
		{"claude windows", desiredClaude(winExe, "windows"), claudeWindowsGolden},
		{"cursor darwin", desiredCursor(macExe, "darwin"), cursorDarwinGolden},
		{"cursor windows", desiredCursor(winExe, "windows"), cursorWindowsGolden},
		{"vscode darwin", desiredVSCode(macExe, "darwin"), vscodeDarwinGolden},
		{"vscode windows", desiredVSCode(winExe, "windows"), vscodeWindowsGolden},
	}
	for _, tc := range tests {
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

// TestDesiredCodexValues asserts the Codex desired state on parsed values rather
// than serialized bytes: the TOML encoder normalizes quoting and key order, so
// the byte form is validated by the Codex writer.
func TestDesiredCodexValues(t *testing.T) {
	t.Run("darwin", func(t *testing.T) {
		got := desiredCodex(macExe, "darwin")
		if !got.FeaturesHooks {
			t.Fatal("expected [features].hooks = true")
		}
		if len(got.PostToolUse) != 1 || got.PostToolUse[0].Matcher != ".*" {
			t.Fatalf("unexpected PostToolUse groups: %#v", got.PostToolUse)
		}
		inner := got.PostToolUse[0].Hooks
		if len(inner) != 1 {
			t.Fatalf("expected one inner hook, got %#v", inner)
		}
		h := inner[0]
		wantCmd := "/usr/local/bin/obot-sentry audit submit --agent codex --phase post-tool --managed-by obot-sentry"
		if h.Type != "command" || h.Command != wantCmd || h.Timeout != 30 || h.StatusMessage != statusMessagePostTool {
			t.Fatalf("unexpected codex hook: %#v", h)
		}
		if h.CommandWindows != "" {
			t.Fatalf("darwin codex must not set command_windows, got %q", h.CommandWindows)
		}
	})
	t.Run("windows mirrors command_windows", func(t *testing.T) {
		got := desiredCodex(winExe, "windows")
		h := got.PostToolUse[0].Hooks[0]
		wantCmd := `& "C:\Program Files\Obot\obot-sentry\obot-sentry.exe" audit submit --agent codex --phase post-tool --managed-by obot-sentry`
		if h.Command != wantCmd {
			t.Fatalf("codex windows command = %q, want %q", h.Command, wantCmd)
		}
		if h.CommandWindows != wantCmd {
			t.Fatalf("codex windows command_windows = %q, want %q", h.CommandWindows, wantCmd)
		}
	})
}

func TestDesiredVSCodeHookLocations(t *testing.T) {
	got := desiredVSCodeHookLocations()
	want := []settingValue{
		{Key: "~/.copilot/hooks", Value: true},
		{Key: ".claude/settings.json", Value: false},
		{Key: ".claude/settings.local.json", Value: false},
		{Key: "~/.claude/settings.json", Value: false},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d locations, got %#v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("location[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
	// The Copilot hook directory must be enabled and all three Claude locations
	// disabled to prevent duplicate, mislabeled audit events.
	if !got[0].Value {
		t.Fatal("Copilot hooks location must be enabled")
	}
	for _, sv := range got[1:] {
		if sv.Value {
			t.Fatalf("Claude location %q must be disabled", sv.Key)
		}
	}
}

func TestDestinationsModel(t *testing.T) {
	t.Run("darwin", func(t *testing.T) {
		dests := Destinations("darwin")
		want := []Destination{
			{Agent: AgentClaudeCode, Label: "Claude Code", Scope: ScopeUser, Format: FormatJSON, Rel: ".claude/settings.json"},
			{Agent: AgentCodex, Label: "Codex", Scope: ScopeMachine, Format: FormatTOML, Abs: "/etc/codex/requirements.toml"},
			{Agent: AgentVSCode, Label: "Visual Studio Code", Scope: ScopeUser, Format: FormatJSON, Rel: ".copilot/hooks/obot-sentry.json"},
			{Agent: AgentCursor, Label: "Cursor", Scope: ScopeMachine, Format: FormatJSON, Abs: "/Library/Application Support/Cursor/hooks.json"},
			{Agent: AgentVSCode, Label: "VS Code settings", Scope: ScopeUser, Format: FormatJSONC, Rel: "Library/Application Support/Code/User/settings.json"},
		}
		assertDestinations(t, dests, want)
	})
	t.Run("windows", func(t *testing.T) {
		t.Setenv("ProgramData", `C:\ProgramData`)
		dests := Destinations("windows")
		want := []Destination{
			{Agent: AgentClaudeCode, Label: "Claude Code", Scope: ScopeUser, Format: FormatJSON, Rel: ".claude/settings.json"},
			{Agent: AgentCodex, Label: "Codex", Scope: ScopeMachine, Format: FormatTOML, Abs: `C:\ProgramData\OpenAI\Codex\requirements.toml`},
			{Agent: AgentVSCode, Label: "Visual Studio Code", Scope: ScopeUser, Format: FormatJSON, Rel: ".copilot/hooks/obot-sentry.json"},
			{Agent: AgentCursor, Label: "Cursor", Scope: ScopeMachine, Format: FormatJSON, Abs: `C:\ProgramData\Cursor\hooks.json`},
			{Agent: AgentVSCode, Label: "VS Code settings", Scope: ScopeUser, Format: FormatJSONC, Rel: "AppData/Roaming/Code/User/settings.json"},
		}
		assertDestinations(t, dests, want)
	})
	t.Run("unsupported returns nil", func(t *testing.T) {
		if dests := Destinations("linux"); dests != nil {
			t.Fatalf("expected nil destinations for linux, got %#v", dests)
		}
	})
}

func assertDestinations(t *testing.T, got, want []Destination) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected %d destinations, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("destination[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

// TestAgentStringsMatchAudit keeps the installer's agent identifiers in lockstep
// with the providers `obot-sentry audit submit` accepts; a drift would generate hook
// commands the runtime rejects.
func TestAgentStringsMatchAudit(t *testing.T) {
	pairs := []struct {
		install Agent
		audit   audit.Agent
	}{
		{AgentClaudeCode, audit.AgentClaudeCode},
		{AgentCodex, audit.AgentCodex},
		{AgentVSCode, audit.AgentVSCode},
		{AgentCursor, audit.AgentCursor},
	}
	for _, p := range pairs {
		if string(p.install) != string(p.audit) {
			t.Fatalf("agent identifier drift: hookinstall %q != audit %q", p.install, p.audit)
		}
	}
}
