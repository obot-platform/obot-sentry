package hookinstall

import (
	"path/filepath"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

func TestResolvePathUserAndMachine(t *testing.T) {
	u := &TargetUser{HomeDir: filepath.FromSlash("/home/alice")}

	userDest := Destination{Scope: ScopeUser, Rel: ".claude/settings.json", Label: "Claude Code"}
	got, err := userDest.ResolvePath(u)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/alice", filepath.FromSlash(".claude/settings.json")); got != want {
		t.Fatalf("user path = %q, want %q", got, want)
	}

	machineDest := Destination{Scope: ScopeMachine, Abs: "/etc/codex/requirements.toml", Label: "Codex"}
	got, err = machineDest.ResolvePath(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/etc/codex/requirements.toml" {
		t.Fatalf("machine path = %q", got)
	}

	if _, err := userDest.ResolvePath(nil); err == nil {
		t.Fatal("expected a user-scoped resolve without a target user to fail")
	}
}

func TestDestinationsModel(t *testing.T) {
	t.Run("darwin", func(t *testing.T) {
		dests := Destinations("darwin")
		want := []Destination{
			{Agent: localagent.ClaudeCode, Label: "Claude Code", Scope: ScopeUser, Format: FormatJSON, Rel: ".claude/settings.json"},
			{Agent: localagent.Codex, Label: "Codex", Scope: ScopeMachine, Format: FormatTOML, Abs: "/etc/codex/requirements.toml"},
			{Agent: localagent.VSCode, Label: "Visual Studio Code", Scope: ScopeUser, Format: FormatJSON, Rel: ".copilot/hooks/obot-sentry.json"},
			{Agent: localagent.Cursor, Label: "Cursor", Scope: ScopeMachine, Format: FormatJSON, Abs: "/Library/Application Support/Cursor/hooks.json"},
			{Agent: localagent.VSCode, Label: "VS Code settings", Scope: ScopeUser, Format: FormatJSONC, Rel: "Library/Application Support/Code/User/settings.json"},
		}
		assertDestinations(t, dests, want)
	})
	t.Run("windows", func(t *testing.T) {
		t.Setenv("ProgramData", `C:\ProgramData`)
		dests := Destinations("windows")
		want := []Destination{
			{Agent: localagent.ClaudeCode, Label: "Claude Code", Scope: ScopeUser, Format: FormatJSON, Rel: ".claude/settings.json"},
			{Agent: localagent.Codex, Label: "Codex", Scope: ScopeMachine, Format: FormatTOML, Abs: `C:\ProgramData\OpenAI\Codex\requirements.toml`},
			{Agent: localagent.VSCode, Label: "Visual Studio Code", Scope: ScopeUser, Format: FormatJSON, Rel: ".copilot/hooks/obot-sentry.json"},
			{Agent: localagent.Cursor, Label: "Cursor", Scope: ScopeMachine, Format: FormatJSON, Abs: `C:\ProgramData\Cursor\hooks.json`},
			{Agent: localagent.VSCode, Label: "VS Code settings", Scope: ScopeUser, Format: FormatJSONC, Rel: "AppData/Roaming/Code/User/settings.json"},
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
