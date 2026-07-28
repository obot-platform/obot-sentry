package enforce

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadJSONResults(t *testing.T) {
	f := newFixture(t, "darwin")

	var out map[string]any
	if got := loadJSON(f.path("missing.json"), &out); got != loadAbsent {
		t.Errorf("missing file = %v, want loadAbsent", got)
	}
	if got := loadJSON(f.write(f.path("bad.json"), `{`), &out); got != loadUnusable {
		t.Errorf("malformed file = %v, want loadUnusable", got)
	}
	if got := loadJSON(f.write(f.path("ok.json"), `{"a":1}`), &out); got != loadOK {
		t.Errorf("valid file = %v, want loadOK", got)
	}
	if got := loadJSON(f.mkdir(f.path("dir.json")), &out); got != loadUnusable {
		t.Errorf("directory = %v, want loadUnusable", got)
	}
}

func TestLoadJSONStripsBOM(t *testing.T) {
	f := newFixture(t, "darwin")
	path := f.write(f.path("bom.json"), "\xef\xbb\xbf"+`{"mcpServers":{"linear":{"url":"https://x.example.com/sse"}}}`)

	set, got := jsonServers(path)()
	if got != loadOK {
		t.Fatalf("load = %v, want loadOK", got)
	}
	if _, ok := set["linear"]; !ok {
		t.Fatal("the servers table did not decode")
	}
}

func TestLoadJSONRefusesOversizedFiles(t *testing.T) {
	f := newFixture(t, "darwin")
	path := f.write(f.path("huge.json"), `{"pad":"`+strings.Repeat("a", maxConfigBytes)+`"}`)

	var out map[string]any
	if got := loadJSON(path, &out); got != loadUnusable {
		t.Fatalf("oversized file = %v, want loadUnusable", got)
	}
}

func TestEnvPathFallsBackToTheConventionalLocation(t *testing.T) {
	f := newFixture(t, "windows")

	got := f.Env.envPath("ProgramData", `C:\ProgramData`, "OpenAI", "Codex", "managed_config.toml")
	want := filepath.Join(f.Env.MachineRoot, `C:\ProgramData`, "OpenAI", "Codex", "managed_config.toml")
	if got != want {
		t.Fatalf("envPath = %q, want %q", got, want)
	}

	f.setenv("ProgramData", f.path("ProgramData"))
	got = f.Env.envPath("ProgramData", `C:\ProgramData`, "OpenAI", "Codex", "managed_config.toml")
	if want := f.path("ProgramData", "OpenAI", "Codex", "managed_config.toml"); got != want {
		t.Fatalf("envPath = %q, want %q", got, want)
	}
}

func TestMachinePathIsAbsoluteInProduction(t *testing.T) {
	env := Env{Home: "/Users/dev", GOOS: "darwin"}
	if got := env.machinePath(claudeManagedMCPDarwin); got != claudeManagedMCPDarwin {
		t.Fatalf("machinePath = %q, want %q", got, claudeManagedMCPDarwin)
	}
	if got := env.machinePath("/etc/codex/managed_config.toml"); got != "/etc/codex/managed_config.toml" {
		t.Fatalf("machinePath = %q", got)
	}
}
