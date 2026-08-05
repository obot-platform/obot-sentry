package hookinstall

import (
	"slices"
	"strings"
	"testing"
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
            "command": "& \"C:\\Program Files\\Obot\\obot-sentry\\obot-sentry.exe\" audit submit --agent claude-code --phase post-tool --managed-by obot-sentry",
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
            "command": "& \"C:\\Program Files\\Obot\\obot-sentry\\obot-sentry.exe\" audit submit --agent claude-code --phase failure --managed-by obot-sentry",
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
		{"claude darwin", desiredClaude(macExe, "darwin", false), claudeDarwinGolden},
		{"claude windows", desiredClaude(winExe, "windows", false), claudeWindowsGolden},
		{"cursor darwin", desiredCursor(macExe, "darwin", false), cursorDarwinGolden},
		{"cursor windows", desiredCursor(winExe, "windows", false), cursorWindowsGolden},
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
		got := desiredCodex(macExe, "darwin", false)
		if len(got.Features) == 0 {
			t.Fatal("expected pinned [features] values")
		}
		for _, pin := range got.Features {
			switch pin.Key {
			case codexFeatureHooks:
				if !pin.Value {
					t.Error("expected [features].hooks = true; without it no hook fires")
				}
			case codexFeatureNonPrefixedMCPToolName:
				if pin.Value {
					t.Error("expected [features].non_prefixed_mcp_tool_names = false; " +
						"with it on, MCP calls lose the mcp__ prefix and are judged as built-in tools")
				}
			default:
				t.Errorf("unexpected pinned feature %q", pin.Key)
			}
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
		got := desiredCodex(winExe, "windows", false)
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

func TestDesiredVSCodeHookLocationsAreDerived(t *testing.T) {
	claudeDefaults := map[string]bool{}
	for _, loc := range vscodeDefaultHookLocations {
		if strings.HasPrefix(loc.Source, claudeHookSourcePrefix) {
			claudeDefaults[loc.Path] = false
		}
	}
	if len(claudeDefaults) == 0 {
		t.Fatal("no Claude-owned locations in the VS Code default table; the transcription is wrong")
	}

	for _, sv := range desiredVSCodeHookLocations() {
		switch {
		case sv.Key == vscodeOwnHookLocation:
			if !sv.Value {
				t.Errorf("our own hook location %q must be enabled", sv.Key)
			}
		case sv.Value:
			t.Errorf("wrote %q as enabled, but only our own hook location may be enabled", sv.Key)
		default:
			if _, ok := claudeDefaults[sv.Key]; !ok {
				t.Errorf("disabled %q, which is not a Claude-owned VS Code default", sv.Key)
				continue
			}
			claudeDefaults[sv.Key] = true
		}
	}

	for path, covered := range claudeDefaults {
		if !covered {
			t.Errorf("Claude-owned default %q was not disabled", path)
		}
	}
}

func TestDesiredVSCodeHookLocationsLeaveGitHubHooksAlone(t *testing.T) {
	const githubHooks = ".github/hooks"

	if !slices.ContainsFunc(vscodeDefaultHookLocations, func(loc vscodeHookLocation) bool {
		return loc.Path == githubHooks
	}) {
		t.Fatalf("%q is missing from the VS Code default table, so this test proves nothing", githubHooks)
	}
	for _, sv := range desiredVSCodeHookLocations() {
		if sv.Key == githubHooks {
			t.Fatalf("%q must not be written; it is Copilot's own hook location", githubHooks)
		}
	}
}
