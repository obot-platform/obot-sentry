package enforce

import (
	"path/filepath"
	"testing"
)

// outerCommand is a .mcp.json defining myserver as a local command, the
// definition a skipped entry in a deeper .mcp.json must fall through to.
const outerCommand = `{"mcpServers":{"myserver":{"command":"/opt/tools/real-server"}}}`

// projectReq builds a request for a server Claude Code loaded from .mcp.json.
func projectReq(serverName, cwd string) ResolveRequest {
	return claudeCodeReq(claudeSourceProject, serverName, cwd)
}

// assertCommand fails unless the resolution names the given executable and no URL.
func assertCommand(t *testing.T, res Resolution, want string) {
	t.Helper()
	if res.Identity.URL != "" {
		t.Fatalf("URL = %q, want the command to identify the server\n%s", res.Identity.URL, resolveTrace(res))
	}
	if res.Identity.Command != want {
		t.Fatalf("Command = %q, want %q\n%s", res.Identity.Command, want, resolveTrace(res))
	}
}

// TestClaudeCodeEntryCommandAndURL covers an entry carrying both a command and a
// url. Claude Code runs the command unless the type names a remote transport, so
// reporting the url would let a local binary pass as an allowlisted endpoint.
func TestClaudeCodeEntryCommandAndURL(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{
		"notype": {"command":"/tmp/evil","url":"https://allowed.example.com/mcp"},
		"stdio":  {"type":"stdio","command":"/tmp/evil","url":"https://allowed.example.com/mcp"},
		"npx":    {"command":"npx","args":["-y","linear-mcp@1.2.3"],"url":"https://allowed.example.com/mcp"},
		"http":   {"type":"http","command":"/tmp/evil","url":"https://allowed.example.com/mcp"},
		"sse":    {"type":"sse","command":"/tmp/evil","url":"https://allowed.example.com/sse"},
		"ws":     {"type":"ws","command":"/tmp/evil","url":"wss://allowed.example.com/ws"}
	}}`)

	assertCommand(t, Resolve(f.Env, projectReq("notype", project)), "/tmp/evil")
	assertCommand(t, Resolve(f.Env, projectReq("stdio", project)), "/tmp/evil")
	assertPackage(t, Resolve(f.Env, projectReq("npx", project)), "npm", "linear-mcp", "1.2.3")
	assertURL(t, Resolve(f.Env, projectReq("http", project)), "https://allowed.example.com/mcp")
	assertURL(t, Resolve(f.Env, projectReq("sse", project)), "https://allowed.example.com/sse")
	assertURL(t, Resolve(f.Env, projectReq("ws", project)), "wss://allowed.example.com/ws")
}

// TestClaudeCodeSkippedEntriesFallThrough covers the entries Claude Code skips,
// each verified against Claude Code. A skipped entry identifies nothing and
// shadows nothing, so a shallower .mcp.json's definition is the one that runs.
func TestClaudeCodeSkippedEntriesFallThrough(t *testing.T) {
	for name, entry := range map[string]string{
		"url without type":    `{"url":"https://decoy.example.com/mcp"}`,
		"uppercase type":      `{"type":"STDIO","command":"/tmp/decoy"}`,
		"unknown type":        `{"type":"streamable","url":"https://decoy.example.com/mcp"}`,
		"ide type":            `{"type":"sse-ide","url":"http://127.0.0.1:9/decoy","ideName":"x"}`,
		"sdk type":            `{"type":"sdk","name":"decoy"}`,
		"proxy type":          `{"type":"claudeai-proxy","url":"https://decoy.example.com","id":"x"}`,
		"type not a string":   `{"type":7,"command":"/tmp/decoy"}`,
		"command not string":  `{"command":5}`,
		"empty command":       `{"command":""}`,
		"args not a list":     `{"command":"/tmp/decoy","args":"-y"}`,
		"remote without url":  `{"type":"http","command":"/tmp/decoy"}`,
		"capitalized URL":     `{"type":"http","URL":"https://decoy.example.com/mcp"}`,
		"capitalized Command": `{"Command":"/tmp/decoy","url":"https://decoy.example.com/mcp"}`,
		"not an object":       `"https://decoy.example.com/mcp"`,
		"empty type":          `{"type":"","command":"/tmp/decoy"}`,
		"null args":           `{"command":"/tmp/decoy","args":null}`,
		"null env":            `{"command":"/tmp/decoy","env":null}`,
		"null headers":        `{"type":"http","url":"https://decoy.example.com/mcp","headers":null}`,
		"env as a list":       `{"command":"/tmp/decoy","env":["A=1"]}`,
		"args as an object":   `{"command":"/tmp/decoy","args":{"0":"-y"}}`,
		"null in args":        `{"command":"npx","args":["-y","linear-mcp@1.2.3",null]}`,
		"number in args":      `{"command":"npx","args":["-y",1]}`,
		"null env value":      `{"command":"/tmp/decoy","env":{"A":null}}`,
		"null header value":   `{"type":"http","url":"https://decoy.example.com/mcp","headers":{"X":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "darwin")
			project := f.mkdir(f.path("proj"))
			cwd := f.mkdir(filepath.Join(project, "sub"))
			f.write(filepath.Join(project, ".mcp.json"), outerCommand)
			f.write(filepath.Join(cwd, ".mcp.json"), `{"mcpServers":{"myserver":`+entry+`}}`)
			assertCommand(t, Resolve(f.Env, projectReq("myserver", cwd)), "/opt/tools/real-server")
		})
	}
}

// TestClaudeCodeKeysAreExact covers decoys spelled in another case. Claude Code
// reads keys exactly, so "MCPServers" is never a server table, and a later
// variant does not overwrite the real one the way it would through a Go struct.
func TestClaudeCodeKeysAreExact(t *testing.T) {
	t.Run("project file with only a variant table", func(t *testing.T) {
		f := newFixture(t, "darwin")
		project := f.mkdir(f.path("proj"))
		cwd := f.mkdir(filepath.Join(project, "sub"))
		f.write(filepath.Join(project, ".mcp.json"), outerCommand)
		f.write(filepath.Join(cwd, ".mcp.json"),
			`{"MCPServers":{"myserver":{"type":"http","url":"https://decoy.example.com/mcp"}}}`)
		assertCommand(t, Resolve(f.Env, projectReq("myserver", cwd)), "/opt/tools/real-server")
	})

	t.Run("variant table after the real one", func(t *testing.T) {
		f := newFixture(t, "darwin")
		f.write(f.homePath(".claude.json"), `{"mcpServers":{"myserver":{"command":"/opt/tools/real-server"}},`+
			`"MCPServers":{"myserver":{"type":"http","url":"https://decoy.example.com/mcp"}}}`)
		assertCommand(t, Resolve(f.Env, claudeCodeReq(claudeSourceUser, "myserver", f.path("proj"))), "/opt/tools/real-server")
	})

	t.Run("variant project keys", func(t *testing.T) {
		f := newFixture(t, "darwin")
		project := f.mkdir(f.path("proj"))
		f.write(f.homePath(".claude.json"), `{`+
			`"Projects":{`+quote(project)+`:{"mcpServers":{"myserver":{"type":"http","url":"https://decoy.example.com/mcp"}}}},`+
			`"projects":{`+quote(project)+`:{"MCPServers":{"myserver":{"type":"http","url":"https://decoy.example.com/mcp"}}}}}`)
		assertUnresolved(t, Resolve(f.Env, claudeCodeReq(claudeSourceLocal, "myserver", project)), "did not find it there")
	})

	t.Run("variant managed settings key", func(t *testing.T) {
		f := newFixture(t, "darwin")
		f.write(f.Env.claudeManagedPath(claudeManagedSettings),
			`{"ManagedMcpServers":{"myserver":`+managedServer("https://decoy.example.com/mcp")+`}}`)
		assertUnresolved(t, Resolve(f.Env, claudeCodeReq(claudeSourceManaged, "myserver", f.path("proj"))), "did not find it there")
	})

	// A "Command" key is not "command", so it does not disqualify a managed entry
	// Claude Code accepts.
	t.Run("variant key on an accepted managed entry", func(t *testing.T) {
		f := newFixture(t, "darwin")
		f.write(f.Env.claudeManagedPath(claudeManagedSettings),
			`{"managedMcpServers":{"myserver":{"type":"http","url":"https://managed.example.com/mcp","Command":"x"}}}`)
		assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceManaged, "myserver", f.path("proj"))), "https://managed.example.com/mcp")
	})
}

// TestClaudeCodePluginEntriesValidatedPerSource covers a plugin whose manifest
// redefines a server in a form Claude Code skips. Claude Code validates each
// source before merging, so the .mcp.json definition stands.
func TestClaudeCodePluginEntriesValidatedPerSource(t *testing.T) {
	f := newFixture(t, "darwin")
	install := f.mkdir(f.path("plugins", "p"))
	f.write(filepath.Join(install, ".mcp.json"), `{"s":{"type":"http","url":"https://plugin.example.com/mcp"}}`)
	f.write(filepath.Join(install, ".claude-plugin", "plugin.json"),
		`{"name":"p","mcpServers":{"s":{"url":"https://decoy.example.com/mcp"}},"MCPServers":{"t":{"type":"http","url":"https://decoy.example.com/mcp"}}}`)
	f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"p@mkt":[`+
		`{"scope":"user","installPath":`+quote(install)+`,"InstallPath":`+quote(f.path("elsewhere"))+`}]}}`)

	assertURL(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "https://plugin.example.com/mcp")
	assertUnresolved(t, Resolve(f.Env, pluginReq("p", "t", f.path("proj"))), "did not find it there")
}
