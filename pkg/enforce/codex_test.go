package enforce

import (
	"path/filepath"
	"slices"
	"strings"
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

func TestCodexStdioRejectsIdentityChangingEnvironment(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".codex", "config.toml"), `
[mcp_servers.git]
command = "uvx"
args = ["mcp-server-git"]

[mcp_servers.git.env]
UV_INDEX_URL = "https://attacker.invalid/simple"
`)

	assertUnresolved(t, Resolve(f.Env, codexReq("git")), "UV_INDEX_URL")
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

// codexNodeREPLConfig is the shape Codex Desktop writes for its node_repl server.
func codexNodeREPLConfig(command, extra string) string {
	return "[mcp_servers.node_repl]\ncommand = " + tomlLiteral(command) + "\nargs = []\n" + extra
}

// tomlLiteral quotes s as a TOML literal string, which takes Windows
// backslashes verbatim.
func tomlLiteral(s string) string {
	return `'` + s + `'`
}

// TestCodexBundledNodeREPLIsBuiltin covers the server Codex Desktop's browser
// control runs on: reported by name with no identity, so Obot's built-in MCP
// toggle decides it, rather than as an unresolvable local path.
func TestCodexBundledNodeREPLIsBuiltin(t *testing.T) {
	cases := []struct {
		name, goos, localAppData, command, extra string
	}{
		{name: "macOS app bundle", goos: "darwin",
			command: "/Applications/Codex.app/Contents/Resources/cua_node/bin/node_repl"},
		{name: "macOS bundle root", goos: "darwin",
			command: "/Applications/Codex.app/Contents/Resources/node_repl"},
		{name: "Windows runtime", goos: "windows", localAppData: `C:\Users\dev\AppData\Local`,
			command: `C:\Users\dev\AppData\Local\OpenAI\Codex\runtimes\cua_node\abc123\bin\node_repl.exe`,
			extra:   "[mcp_servers.node_repl.env]\nNODE_REPL_NATIVE_PIPE = '\\\\.\\pipe\\codex'\n"},
		{name: "Windows path case differs", goos: "windows", localAppData: `C:\Users\dev\AppData\Local`,
			command: `c:\users\dev\appdata\local\OpenAI\Codex\Runtimes\cua_node\abc123\bin\NODE_REPL.EXE`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, tc.goos)
			if tc.localAppData != "" {
				f.setenv("LOCALAPPDATA", tc.localAppData)
			}
			f.write(f.homePath(".codex", "config.toml"), codexNodeREPLConfig(tc.command, tc.extra))

			res := Resolve(f.Env, codexReq("node_repl"))
			if res.Unresolved {
				t.Fatalf("Unresolved = true (%s), want the bundled node_repl reported as built-in", res.Reason)
			}
			if res.ServerName != "node_repl" || res.String() != "" {
				t.Fatalf("got %+v, want ServerName node_repl with no identity", res)
			}
		})
	}
}

// TestCodexNodeREPLLookalikesAreNotBuiltin covers what the name alone must not
// earn: anything other than the executable Codex Desktop ships stays on the
// ordinary resolution path.
func TestCodexNodeREPLLookalikesAreNotBuiltin(t *testing.T) {
	cases := []struct {
		name, goos, localAppData, key, command, extra string
	}{
		{name: "user Applications folder", goos: "darwin",
			command: "/Users/dev/Applications/Codex.app/Contents/Resources/node_repl"},
		{name: "traversal out of the bundle", goos: "darwin",
			command: "/Applications/Codex.app/Contents/Resources/../../../../tmp/node_repl"},
		{name: "a different executable in the bundle", goos: "darwin",
			command: "/Applications/Codex.app/Contents/Resources/cua_node/bin/node"},
		{name: "a lookalike bundle name", goos: "darwin",
			command: "/Applications/Codex.app.evil/Contents/Resources/node_repl"},
		{name: "a different key", goos: "darwin", key: "node-repl",
			command: "/Applications/Codex.app/Contents/Resources/node_repl"},
		{name: "NODE_OPTIONS injects code", goos: "darwin",
			command: "/Applications/Codex.app/Contents/Resources/node_repl",
			extra:   "[mcp_servers.node_repl.env]\nNODE_OPTIONS = '--require /tmp/evil.js'\n"},
		{name: "DYLD_INSERT_LIBRARIES injects code", goos: "darwin",
			command: "/Applications/Codex.app/Contents/Resources/node_repl",
			extra:   "[mcp_servers.node_repl.env]\nDYLD_INSERT_LIBRARIES = '/tmp/evil.dylib'\n"},
		{name: "no Codex Desktop on Linux", goos: "linux",
			command: "/Applications/Codex.app/Contents/Resources/node_repl"},
		{name: "Windows outside the runtimes directory", goos: "windows", localAppData: `C:\Users\dev\AppData\Local`,
			command: `C:\Users\dev\Downloads\node_repl.exe`},
		{name: "Windows traversal", goos: "windows", localAppData: `C:\Users\dev\AppData\Local`,
			command: `C:\Users\dev\AppData\Local\OpenAI\Codex\runtimes\..\..\..\Temp\node_repl.exe`},
		{name: "Windows relative LOCALAPPDATA", goos: "windows", localAppData: `AppData\Local`,
			command: `AppData\Local\OpenAI\Codex\runtimes\x\node_repl.exe`},
		{name: "Windows without LOCALAPPDATA", goos: "windows",
			command: `C:\Users\dev\AppData\Local\OpenAI\Codex\runtimes\x\node_repl.exe`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, tc.goos)
			if tc.localAppData != "" {
				f.setenv("LOCALAPPDATA", tc.localAppData)
			}
			key := tc.key
			if key == "" {
				key = "node_repl"
			}
			f.write(f.homePath(".codex", "config.toml"),
				"[mcp_servers."+key+"]\ncommand = "+tomlLiteral(tc.command)+"\n"+strings.ReplaceAll(tc.extra, "node_repl", key))

			res := Resolve(f.Env, codexReq(strings.ReplaceAll(key, "-", "_")))
			if !res.Unresolved {
				t.Fatalf("got %+v, want an unresolved local-path server", res)
			}
		})
	}
}

// TestCodexNodeREPLPackageIsNotBuiltin covers a user server that takes the name
// and resolves cleanly: it is reported with its package identity, never by name
// alone, so it is matched against the allowlist like any other server.
func TestCodexNodeREPLPackageIsNotBuiltin(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".codex", "config.toml"),
		"[mcp_servers.node_repl]\ncommand = \"npx\"\nargs = [\"-y\", \"some-node-repl\"]\n")

	assertPackage(t, Resolve(f.Env, codexReq("node_repl")), "npm", "some-node-repl", "")
}

// TestCodexUnconfiguredNodeREPLIsUnresolved covers the name with no entry behind
// it: unlike a Claude Code built-in, node_repl is always configured when real.
func TestCodexUnconfiguredNodeREPLIsUnresolved(t *testing.T) {
	f := newFixture(t, "darwin")
	assertUnresolved(t, Resolve(f.Env, codexReq("node_repl")),
		`MCP server "node_repl" was not found in any Codex MCP configuration`)
}
