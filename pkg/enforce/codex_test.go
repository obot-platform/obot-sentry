package enforce

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

func codexReq(serverName string) ResolveRequest {
	return ResolveRequest{Agent: localagent.Codex, ServerName: serverName}
}

// TestCodexFileOrder pins the config order. Codex takes no project-scoped MCP
// configuration, so there is no candidate-root walk.
func TestCodexFileOrder(t *testing.T) {
	f := newFixture(t, "darwin")

	res := Resolve(f.Env, codexReq("linear"))
	assertUnresolved(t, res, `MCP server "linear" was not found in any Codex MCP configuration`)

	want := []string{
		f.homePath(".codex", "config.toml"),
		f.homePath(".codex", "managed_config.toml"),
		f.machinePath("/etc/codex/managed_config.toml"),
	}
	if got := consultedPaths(res); !slices.Equal(got, want) {
		t.Fatalf("consulted\n%v\nwant\n%v", got, want)
	}
}

// TestCodexWindowsManagedConfig covers the Windows managed config, which sits
// beside where obot-sentry writes the Codex hook.
func TestCodexWindowsManagedConfig(t *testing.T) {
	f := newFixture(t, "windows")
	programData := f.mkdir(f.path("ProgramData"))
	f.setenv("ProgramData", programData)
	managed := filepath.Join(programData, "OpenAI", "Codex", "managed_config.toml")
	f.write(managed, "[mcp_servers.linear]\nurl = \"https://managed.example.com/sse\"\n")

	res := Resolve(f.Env, codexReq("linear"))
	assertURL(t, res, "https://managed.example.com/sse")

	want := []string{
		f.homePath(".codex", "config.toml"),
		f.homePath(".codex", "managed_config.toml"),
		managed,
	}
	if got := consultedPaths(res); !slices.Equal(got, want) {
		t.Fatalf("consulted\n%v\nwant\n%v", got, want)
	}
}

// TestCodexUserConfigFirst covers the precedence between the three files.
func TestCodexUserConfigFirst(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".codex", "config.toml"), "[mcp_servers.linear]\nurl = \"https://user.example.com/sse\"\n")
	f.write(f.machinePath("/etc/codex/managed_config.toml"), "[mcp_servers.linear]\nurl = \"https://managed.example.com/sse\"\n")

	assertURL(t, Resolve(f.Env, codexReq("linear")), "https://user.example.com/sse")
}

func TestCodexNamespaceFormFallback(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".codex", "config.toml"),
		"[mcp_servers.\"My-Linear\"]\nurl = \"https://linear.example.com/sse\"\n")

	res := Resolve(f.Env, codexReq("My_Linear"))
	assertURL(t, res, "https://linear.example.com/sse")
	if last, want := res.Trace[len(res.Trace)-1], `matched as "My-Linear"`; last.Note != want {
		t.Fatalf("trace note = %q, want %q: the matched key is what an allowlist entry has to spell", last.Note, want)
	}
}

func TestCodexPreservesCase(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".codex", "config.toml"),
		"[mcp_servers.\"My-Linear\"]\nurl = \"https://linear.example.com/sse\"\n")

	assertUnresolved(t, Resolve(f.Env, codexReq("my_linear")),
		"was not found in any Codex MCP configuration")
}

// TestCodexStdio covers a package-runner entry read out of TOML.
func TestCodexStdio(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".codex", "config.toml"),
		"[mcp_servers.git]\ncommand = \"uvx\"\nargs = [\"mcp-server-git\"]\n")

	assertPackage(t, Resolve(f.Env, codexReq("git")), "pypi", "mcp-server-git", "")
}

// TestCodexMalformedTOMLIsSkipped covers the tolerance rule for TOML.
func TestCodexMalformedTOMLIsSkipped(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".codex", "config.toml"), "[mcp_servers.linear\nurl = ")
	f.write(f.homePath(".codex", "managed_config.toml"), "[mcp_servers.linear]\nurl = \"https://managed.example.com/sse\"\n")

	res := Resolve(f.Env, codexReq("linear"))
	assertURL(t, res, "https://managed.example.com/sse")
	if res.Trace[0].Note != "unreadable or malformed" {
		t.Fatalf("expected the malformed file to be recorded as such:\n%s", resolveTrace(res))
	}
}

func TestCodexReportsTheMatchedConfigKey(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".codex", "config.toml"),
		"[mcp_servers.\"probe-npx-stdio\"]\ncommand = \"npx\"\nargs = [\"-y\", \"@modelcontextprotocol/server-everything\"]\n")

	// The tool name was mcp__probe_npx_stdio__echo, so the hint is underscored.
	res := Resolve(f.Env, codexReq("probe_npx_stdio"))
	assertPackage(t, res, "npm", "@modelcontextprotocol/server-everything", "")
	if res.ServerName != "probe-npx-stdio" {
		t.Errorf("ServerName = %q, want the matched config key", res.ServerName)
	}
}

func TestCodexMatchedButUnresolvableReportsTheConfigKey(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".codex", "config.toml"),
		"[mcp_servers.\"codebase-memory-mcp\"]\ncommand = \"/Users/dev/.local/bin/codebase-memory-mcp\"\n")

	res := Resolve(f.Env, codexReq("codebase_memory_mcp"))
	assertUnresolved(t, res, "is a path, not a bare package runner")
	if res.ServerName != "codebase-memory-mcp" {
		t.Errorf("ServerName = %q, want the matched config key even though resolution failed", res.ServerName)
	}
	if res.Identity.Command != "/Users/dev/.local/bin/codebase-memory-mcp" {
		t.Errorf("Command = %q, want the bare executable", res.Identity.Command)
	}
}

func TestCodexHasNoBuiltinServers(t *testing.T) {
	f := newFixture(t, "darwin")

	res := Resolve(f.Env, codexReq("computer-use"))
	assertUnresolved(t, res, `MCP server "computer-use" was not found in any Codex MCP configuration`)

	// Configured, as it is in practice, it resolves through the ordinary path.
	f.write(f.homePath(".codex", "config.toml"),
		"[mcp_servers.\"computer-use\"]\nurl = \"https://user.example.com/sse\"\n")
	assertURL(t, Resolve(f.Env, codexReq("computer-use")), "https://user.example.com/sse")
}
