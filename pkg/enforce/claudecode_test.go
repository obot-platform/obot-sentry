package enforce

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

// claudeCodeReq builds a Claude Code resolve request for a server Claude Code
// reported as loaded from source.
func claudeCodeReq(source, serverName, cwd string) ResolveRequest {
	return ResolveRequest{Agent: localagent.ClaudeCode, ServerName: serverName, ServerSource: source, CWD: cwd, ProjectDir: cwd}
}

// TestClaudeCodeReadsOnlyTheReportedSource covers the resolver's contract: Claude
// Code reports the scope it loaded the server from, and only that scope is read.
// A definition elsewhere, however it would rank, is not the one the session runs.
func TestClaudeCodeReadsOnlyTheReportedSource(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{"myserver":{"type":"http","url":"https://project.example.com/mcp"}}}`)
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"myserver":{"type":"http","url":"https://user.example.com/mcp"}},`+
		`"projects":{`+quote(project)+`:{"mcpServers":{"myserver":{"type":"http","url":"https://local.example.com/mcp"}}}}}`)
	f.write(f.Env.managedMCPPath(), `{"mcpServers":{"myserver":{"type":"http","url":"https://enterprise.example.com/mcp"}}}`)
	f.write(f.Env.claudeManagedPath(claudeManagedSettings), `{"managedMcpServers":{"myserver":`+managedServer("https://managed.example.com/mcp")+`}}`)

	for source, want := range map[string]string{
		claudeSourceProject:    "https://project.example.com/mcp",
		claudeSourceUser:       "https://user.example.com/mcp",
		claudeSourceLocal:      "https://local.example.com/mcp",
		claudeSourceEnterprise: "https://enterprise.example.com/mcp",
		claudeSourceManaged:    "https://managed.example.com/mcp",
	} {
		res := Resolve(f.Env, claudeCodeReq(source, "myserver", project))
		assertURL(t, res, want)
		if len(res.Trace) != 1 {
			t.Errorf("source %s consulted %d places, want only its own:\n%s", source, len(res.Trace), resolveTrace(res))
		}
	}
}

// TestClaudeCodeClaudeJSONOnlyForItsScopes covers .claude.json, which holds only
// the user and local scopes and can run to tens of megabytes: a project, plugin
// or managed server neither reads it nor depends on it being readable.
func TestClaudeCodeClaudeJSONOnlyForItsScopes(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{"myserver":{"type":"http","url":"https://project.example.com/mcp"}}}`)
	f.write(f.homePath(".claude.json"), `{"projects":`)

	for _, source := range []string{claudeSourceProject, claudeSourcePlugin, claudeSourceEnterprise, claudeSourceManaged} {
		loader := newConfigLoader()
		resolve(t.Context(), loader, f.Env, claudeCodeReq(source, "myserver", project), &tracer{})
		if len(loader.claudeDocs) != 0 {
			t.Errorf("a %s server parsed .claude.json", source)
		}
	}
	assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceProject, "myserver", project)), "https://project.example.com/mcp")
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq(claudeSourceUser, "myserver", project)), "could not be read")
}

// TestClaudeCodeSourceIsRequired covers a call without Claude Code's mcp_server
// field, which names neither the server's key nor its scope. Without it the
// resolver would be back to guessing both from the tool name.
func TestClaudeCodeSourceIsRequired(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"myserver":{"type":"http","url":"https://user.example.com/mcp"}}}`)
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq("", "myserver", f.path("proj"))), "update Claude Code")
}

// TestClaudeCodeUnreadableSources covers servers whose definitions the hook
// cannot see: --mcp-config and SDK servers, agent definitions, and any source
// Claude Code adds later. A file definition of the same name is not the one that
// runs, so none is reported.
func TestClaudeCodeUnreadableSources(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{"myserver":{"type":"http","url":"https://allowed.example.com/mcp"}}}`)
	for source, want := range map[string]string{
		"dynamic":  "--mcp-config",
		"sdk":      "the SDK host",
		"agent":    "an agent definition",
		"newscope": `a source the hook does not recognize ("newscope")`,
	} {
		res := Resolve(f.Env, claudeCodeReq(source, "myserver", project))
		assertUnresolved(t, res, want)
		if len(res.Trace) != 0 {
			t.Errorf("source %s consulted configuration:\n%s", source, resolveTrace(res))
		}
	}
}

// TestClaudeCodeNameIsExact covers the configuration key Claude Code reports. It
// is the key as written, so it is matched exactly: "my.server" and "my_server"
// share a tool namespace and are still different servers.
func TestClaudeCodeNameIsExact(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{
		"my.server":{"type":"http","url":"https://dotted.example.com/mcp"},
		"my_server":{"type":"http","url":"https://underscored.example.com/mcp"}
	}}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceProject, "my.server", project)), "https://dotted.example.com/mcp")
	assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceProject, "my_server", project)), "https://underscored.example.com/mcp")
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq(claudeSourceProject, "My.Server", project)), "did not find it there")
}

// TestClaudeCodeConnector covers claude.ai account connectors, configured in the
// account rather than on this machine. The display name Claude Code reports is
// the connector's whole identity; no local file is read for it.
func TestClaudeCodeConnector(t *testing.T) {
	f := newFixture(t, "darwin")
	res := Resolve(f.Env, claudeCodeReq(claudeSourceClaudeAI, "claude.ai Linear", f.path("proj")))
	if res.Unresolved || res.Identity.Connector != "claude.ai Linear" || res.ServerName != "claude.ai Linear" {
		t.Fatalf("Connector = %q, ServerName = %q (unresolved=%v), want the display name\n%s",
			res.Identity.Connector, res.ServerName, res.Unresolved, resolveTrace(res))
	}
}

// TestClaudeCodeBuiltinServerIsReportedByName covers Claude Code's built-in MCP
// servers, reported by name for the built-in toggle. Each is matched by its exact
// name and the one source it reports: computer-use and claude-in-chrome are
// dynamic-scope stdio servers, and the desktop host supplies Claude Preview,
// Claude Browser and workspace as SDK servers.
func TestClaudeCodeBuiltinServerIsReportedByName(t *testing.T) {
	f := newFixture(t, "darwin")
	for name, source := range claudeBuiltinSources {
		res := Resolve(f.Env, claudeCodeReq(source, name, f.path("proj")))
		if res.Unresolved || res.ServerName != name || res.Identity != emptyIdentity {
			t.Errorf("%s from %s: ServerName = %q, Identity = %+v (unresolved=%v, %s), want the built-in by name",
				name, source, res.ServerName, res.Identity, res.Unresolved, res.Reason)
		}
	}

	// From another source, or spelled another way, the name is not the built-in.
	for _, tc := range []struct{ source, name, reason string }{
		{"sdk", "computer-use", "the SDK host"},
		{"dynamic", "Claude Preview", "--mcp-config"},
		{"agent", "workspace", "an agent definition"},
		{"sdk", "Claude_Browser", "the SDK host"},
		{"newscope", "Claude Preview", "does not recognize"},
	} {
		assertUnresolved(t, Resolve(f.Env, claudeCodeReq(tc.source, tc.name, f.path("proj"))), tc.reason)
	}
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"Claude Preview":{"type":"http","url":"https://user.example.com/mcp"}}}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceUser, "Claude Preview", f.path("proj"))), "https://user.example.com/mcp")
}

// TestClaudeCodeBuiltinSourcesMatchTheBuiltinTable keeps the per-source table in
// step with builtinAgentMCPServers, the list that must agree with obot.
func TestClaudeCodeBuiltinSourcesMatchTheBuiltinTable(t *testing.T) {
	table := builtinAgentMCPServers[localagent.ClaudeCode]
	if len(table) != len(claudeBuiltinSources) {
		t.Fatalf("builtinAgentMCPServers has %d Claude Code entries, claudeBuiltinSources %d", len(table), len(claudeBuiltinSources))
	}
	for name := range table {
		if _, ok := claudeBuiltinSources[name]; !ok {
			t.Errorf("built-in %q has no source in claudeBuiltinSources", name)
		}
	}
}

// TestClaudeCodeManagedMCPConfig covers managed-mcp.json, which Claude Code
// reports as the enterprise source.
func TestClaudeCodeManagedMCPConfig(t *testing.T) {
	f := newFixture(t, "darwin")
	req := claudeCodeReq(claudeSourceEnterprise, "myserver", f.path("proj"))
	f.write(f.Env.managedMCPPath(), `{"mcpServers":{"myserver":{"type":"http","url":"https://managed.example.com/mcp"}}}`)
	assertURL(t, Resolve(f.Env, req), "https://managed.example.com/mcp")

	// Claude Code loads nothing from one it cannot parse, or one over its 2 MiB cap.
	f.write(f.Env.managedMCPPath(), `{"mcpServers":{"myserver":{"type":"http","url":"https://managed.example.com/mcp"},}}`)
	assertUnresolved(t, Resolve(f.Env, req), "did not find it there")
	f.write(f.Env.managedMCPPath(), padJSON(t, `{"mcpServers":{"myserver":{"type":"http","url":"https://managed.example.com/mcp"}}}`, claudeMCPConfigMaxBytes+1))
	assertUnresolved(t, Resolve(f.Env, req), "did not find it there")
}

// TestClaudeCodeManagedConfigWindows covers the Windows managed directory, which
// Claude Code hardcodes. %PROGRAMFILES% is inherited from the agent and Claude
// Code ignores it; were the resolver to follow it, a user could plant a
// managed-mcp.json that vouches for a server the session never loaded.
func TestClaudeCodeManagedConfigWindows(t *testing.T) {
	f := newFixture(t, "windows")
	req := claudeCodeReq(claudeSourceEnterprise, "myserver", f.path("proj"))
	f.write(f.machinePath("C:/Program Files/ClaudeCode/managed-mcp.json"),
		`{"mcpServers":{"myserver":{"type":"http","url":"https://managed.example.com/sse"}}}`)
	assertURL(t, Resolve(f.Env, req), "https://managed.example.com/sse")

	planted := f.mkdir(f.path("planted"))
	f.setenv("PROGRAMFILES", planted)
	f.write(filepath.Join(planted, "ClaudeCode", "managed-mcp.json"),
		`{"mcpServers":{"myserver":{"type":"http","url":"https://allowed.example.com/sse"}}}`)
	res := Resolve(f.Env, req)
	assertURL(t, res, "https://managed.example.com/sse")
	for _, path := range consultedPaths(res) {
		if strings.HasPrefix(path, planted) {
			t.Fatalf("consulted %s under %%PROGRAMFILES%%:\n%s", path, resolveTrace(res))
		}
	}
}

// managedServer renders a managedMcpServers entry Claude Code accepts.
func managedServer(url string) string {
	return `{"type":"http","url":"` + url + `"}`
}

// TestClaudeCodeManagedSettingsRejectedEntries covers the entries Claude Code
// ignores in managedMcpServers: anything but an "http" or "sse" server at an
// https URL. An ignored entry is not a server Claude Code loaded from there.
func TestClaudeCodeManagedSettingsRejectedEntries(t *testing.T) {
	for name, entry := range map[string]string{
		"plain http":      `{"type":"http","url":"http://managed.example.com/sse"}`,
		"no type":         `{"url":"https://managed.example.com/sse"}`,
		"ws type":         `{"type":"ws","url":"wss://managed.example.com/sse"}`,
		"uppercase type":  `{"type":"HTTP","url":"https://managed.example.com/sse"}`,
		"stdio":           `{"command":"npx","args":["-y","managed-mcp@1.0.0"]}`,
		"url and command": `{"type":"http","url":"https://managed.example.com/sse","command":"npx"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "darwin")
			f.write(f.Env.claudeManagedPath(claudeManagedSettings), `{"managedMcpServers":{"myserver":`+entry+`}}`)
			assertUnresolved(t, Resolve(f.Env, claudeCodeReq(claudeSourceManaged, "myserver", f.path("proj"))), "did not find it there")
		})
	}

	// The scheme is matched without regard to case, as Claude Code matches it.
	f := newFixture(t, "darwin")
	f.write(f.Env.claudeManagedPath(claudeManagedSettings),
		`{"managedMcpServers":{"myserver":{"type":"sse","url":"HTTPS://managed.example.com/sse"}}}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceManaged, "myserver", f.path("proj"))), "https://managed.example.com/sse")
}

// TestClaudeCodeManagedSettingsDropIns covers managed-settings.d as Claude Code
// layers it: managed-settings.json first, then drop-ins in byte order of file
// name, a later one overriding an earlier one. An entry a later file defines in a
// form Claude Code rejects leaves the earlier definition in force.
func TestClaudeCodeManagedSettingsDropIns(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	managed := func(name string) ResolveRequest { return claudeCodeReq(claudeSourceManaged, name, project) }
	dropIn := func(file, server, url string) {
		f.write(f.Env.claudeManagedPath(claudeManagedDropInDir, file),
			`{"managedMcpServers":{"`+server+`":`+managedServer(url)+`}}`)
	}
	f.write(f.Env.claudeManagedPath(claudeManagedSettings),
		`{"managedMcpServers":{"base":`+managedServer("https://base.example.com/sse")+`,`+
			`"kept":`+managedServer("https://kept.example.com/sse")+`}}`)
	dropIn("10.json", "base", "https://ten.example.com/sse")
	dropIn("9.json", "base", "https://nine.example.com/sse") // "9" sorts after "10"
	dropIn("B.json", "case", "https://upper.example.com/sse")
	dropIn("a.json", "case", "https://lower.example.com/sse") // "a" sorts after "B"
	f.write(f.Env.claudeManagedPath(claudeManagedDropInDir, "z.json"),
		`{"managedMcpServers":{"kept":{"type":"http","url":"http://rejected.example.com/sse"}}}`)

	assertURL(t, Resolve(f.Env, managed("base")), "https://nine.example.com/sse")
	assertURL(t, Resolve(f.Env, managed("case")), "https://lower.example.com/sse")
	assertURL(t, Resolve(f.Env, managed("kept")), "https://kept.example.com/sse")

	// Only files directly in the directory whose names end in a lowercase ".json"
	// and do not start with a dot are drop-ins.
	for _, file := range []string{".hidden.json", "x.txt", "U.JSON", filepath.Join("sub", "s.json")} {
		dropIn(file, "ignored", "https://ignored.example.com/sse")
	}
	assertUnresolved(t, Resolve(f.Env, managed("ignored")), "did not find it there")
}

// TestClaudeCodeManagedSettingsIncomplete covers a managed-settings.d the
// resolver cannot read in full: an unread drop-in can override a read one.
func TestClaudeCodeManagedSettingsIncomplete(t *testing.T) {
	f := newFixture(t, "darwin")
	for i := range maxClaudeManagedDropIns + 1 {
		f.write(f.Env.claudeManagedPath(claudeManagedDropInDir, fmt.Sprintf("%04d.json", i)), `{}`)
	}
	f.write(f.Env.claudeManagedPath(claudeManagedDropInDir, "0000.json"),
		`{"managedMcpServers":{"early":`+managedServer("https://early.example.com/sse")+`}}`)
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq(claudeSourceManaged, "early", f.path("proj"))), "could not be read in full")
}

// TestClaudeCodeManagedSettingsUnreadableDropIns covers a drop-in location
// Claude Code cannot read either. It skips it silently and loads everything else,
// so the resolver does the same.
func TestClaudeCodeManagedSettingsUnreadableDropIns(t *testing.T) {
	settings := `{"managedMcpServers":{"myserver":` + managedServer("https://settings.example.com/sse") + `}}`

	t.Run("not a directory", func(t *testing.T) {
		f := newFixture(t, "darwin")
		f.write(f.Env.claudeManagedPath(claudeManagedSettings), settings)
		f.write(f.Env.claudeManagedPath(claudeManagedDropInDir), "not a directory")
		assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceManaged, "myserver", f.path("proj"))), "https://settings.example.com/sse")
	})

	t.Run("permission denied", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs POSIX permissions enforced against the test process")
		}
		f := newFixture(t, "darwin")
		f.write(f.Env.claudeManagedPath(claudeManagedSettings), settings)
		dir := f.Env.claudeManagedPath(claudeManagedDropInDir)
		f.write(filepath.Join(dir, "10.json"), `{"managedMcpServers":{"myserver":`+managedServer("https://hidden.example.com/sse")+`}}`)
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceManaged, "myserver", f.path("proj"))), "https://settings.example.com/sse")
	})
}

// padJSON pads a JSON document with trailing whitespace to exactly size bytes.
func padJSON(t *testing.T, doc string, size int) string {
	t.Helper()
	if len(doc) > size {
		t.Fatalf("document is already %d bytes, over %d", len(doc), size)
	}
	return doc + strings.Repeat(" ", size-len(doc))
}

// TestClaudeCodeProjectFileSizeCap covers Claude Code's 2 MiB cap on .mcp.json:
// it skips a larger file, so a server in it is not one it loaded from there.
func TestClaudeCodeProjectFileSizeCap(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	req := claudeCodeReq(claudeSourceProject, "myserver", project)
	doc := `{"mcpServers":{"myserver":{"type":"http","url":"https://project.example.com/mcp"}}}`

	f.write(filepath.Join(project, ".mcp.json"), padJSON(t, doc, claudeMCPConfigMaxBytes))
	assertURL(t, Resolve(f.Env, req), "https://project.example.com/mcp")

	f.write(filepath.Join(project, ".mcp.json"), padJSON(t, doc, claudeMCPConfigMaxBytes+1))
	assertUnresolved(t, Resolve(f.Env, req), "did not find it there")
}

// nestedJSON is a JSON array nested depth levels deep.
func nestedJSON(depth int) string {
	return strings.Repeat("[", depth) + strings.Repeat("]", depth)
}

// TestClaudeCodeUndecodableFileDoesNotFallThrough covers a file Claude Code may
// have loaded that the hook cannot decode. encoding/json refuses nesting past
// 10000 levels, which JSON.parse accepts, so Claude Code loads such a .mcp.json
// (verified 500000 deep). Skipping it would let a shallower file's definition
// answer for the one Claude Code runs. Only a file JSON.parse also rejects may
// be skipped.
func TestClaudeCodeUndecodableFileDoesNotFallThrough(t *testing.T) {
	f := newFixture(t, "darwin")
	repo := f.mkdir(f.path("repo"))
	sub := f.mkdir(filepath.Join(repo, "sub"))
	f.write(filepath.Join(repo, ".mcp.json"), `{"mcpServers":{"probe":{"type":"http","url":"https://allowed.example.com/mcp"}}}`)
	req := claudeCodeReq(claudeSourceProject, "probe", sub)
	deep := func(depth int) string {
		return `{"mcpServers":{"probe":{"command":"/tmp/real"}},"junk":` + nestedJSON(depth) + `}`
	}

	f.write(filepath.Join(sub, ".mcp.json"), deep(9000))
	assertCommand(t, Resolve(f.Env, req), "/tmp/real")

	for name, doc := range map[string]string{
		"nested past encoding/json's limit": deep(10001),
		"not an object":                     `[]`,
		"mcpServers not an object":          `{"mcpServers":[]}`,
	} {
		f.write(filepath.Join(sub, ".mcp.json"), doc)
		if res := Resolve(f.Env, req); !res.Unresolved || !strings.Contains(res.Reason, "cannot decode") {
			t.Errorf("%s: want unresolved as undecodable, got unresolved=%v reason=%q\n%s", name, res.Unresolved, res.Reason, resolveTrace(res))
		}
	}

	// A file JSON.parse rejects is skipped by Claude Code too.
	f.write(filepath.Join(sub, ".mcp.json"), `{"mcpServers":{"probe":{"command":"/tmp/real"},}}`)
	assertURL(t, Resolve(f.Env, req), "https://allowed.example.com/mcp")

	// A null mcpServers defines no server, so the file above it answers.
	f.write(filepath.Join(sub, ".mcp.json"), `{"mcpServers":null,"probe":{"command":"/tmp/real"}}`)
	assertURL(t, Resolve(f.Env, req), "https://allowed.example.com/mcp")

	// A managed-settings.d drop-in that cannot be decoded may override the base
	// file's definition.
	f.write(f.Env.claudeManagedPath(claudeManagedSettings), `{"managedMcpServers":{"myserver":`+managedServer("https://base.example.com/sse")+`}}`)
	managed := claudeCodeReq(claudeSourceManaged, "myserver", sub)
	assertURL(t, Resolve(f.Env, managed), "https://base.example.com/sse")
	f.write(f.Env.claudeManagedPath(claudeManagedDropInDir, "10.json"),
		`{"managedMcpServers":{"myserver":`+managedServer("https://override.example.com/sse")+`},"junk":`+nestedJSON(10001)+`}`)
	assertUnresolved(t, Resolve(f.Env, managed), "cannot decode")
}

// TestClaudeCodeProjectDirKeepsWhitespace covers a project directory whose name
// ends in a space, which macOS allows. It is not its sibling without the space,
// so the sibling's .mcp.json must not answer for it.
func TestClaudeCodeProjectDirKeepsWhitespace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows strips trailing spaces from a path's final name")
	}
	f := newFixture(t, "darwin")
	sibling := f.mkdir(f.path("work", "app"))
	spaced := f.mkdir(f.path("work", "app "))
	f.write(filepath.Join(sibling, ".mcp.json"), `{"mcpServers":{"probe":{"type":"http","url":"https://allowed.example.com/mcp"}}}`)
	req := claudeCodeReq(claudeSourceProject, "probe", spaced)
	assertUnresolved(t, Resolve(f.Env, req), "did not find it there")

	f.write(filepath.Join(spaced, ".mcp.json"), `{"mcpServers":{"probe":{"command":"/tmp/real"}}}`)
	assertCommand(t, Resolve(f.Env, req), "/tmp/real")

	// Nor does the sibling's projects{} entry.
	f.write(f.homePath(".claude.json"), `{"projects":{`+quote(sibling)+`:{"mcpServers":{"local":{"type":"http","url":"https://allowed.example.com/mcp"}}}}}`)
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq(claudeSourceLocal, "local", spaced)), "did not find it there")
}

// TestClaudeCodeClaudeJSONMustBeRead covers .claude.json, which holds the local
// and user scopes. Claude Code reads it whole however large it grows, so the
// resolver reads past its general limit, and says so when it cannot read it.
func TestClaudeCodeClaudeJSONMustBeRead(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	req := claudeCodeReq(claudeSourceLocal, "myserver", project)
	local := `{"projects":{` + quote(project) + `:{"mcpServers":{"myserver":{"command":"/opt/tools/real-server"}}}}}`

	f.write(f.homePath(".claude.json"), padJSON(t, local, maxConfigBytes+1))
	assertCommand(t, Resolve(f.Env, req), "/opt/tools/real-server")

	f.write(f.homePath(".claude.json"), `{"projects":`)
	assertUnresolved(t, Resolve(f.Env, req), "could not be read")
	req.ServerSource = claudeSourceUser
	assertUnresolved(t, Resolve(f.Env, req), "could not be read")

	f.write(f.homePath(".claude.json"), local)
	if err := os.Truncate(f.homePath(".claude.json"), claudeJSONMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	assertUnresolved(t, Resolve(f.Env, req), "could not be read")
}

// TestClaudeCodeConfigDirLeavesUserAndLocalUnresolved covers a hook whose
// environment sets CLAUDE_CONFIG_DIR. Claude Code reads .claude.json from the
// directory it was started with, and its settings can change the variable for
// its hooks afterwards, so the hook cannot tell which .claude.json the session
// read: neither one may identify a user or local server. The other sources are
// read from places the variable does not decide, or that follow it.
func TestClaudeCodeConfigDirLeavesUserAndLocalUnresolved(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	configDir := f.path("alt-claude")
	claudeJSON := func(url string) string {
		return `{"mcpServers":{"myserver":{"type":"http","url":"` + url + `"}},` +
			`"projects":{` + quote(project) + `:{"mcpServers":{"localserver":{"type":"http","url":"` + url + `"}}}}}`
	}
	user := claudeCodeReq(claudeSourceUser, "myserver", project)
	local := claudeCodeReq(claudeSourceLocal, "localserver", project)
	f.write(f.homePath(".claude.json"), claudeJSON("https://home.example.com/mcp"))
	assertURL(t, Resolve(f.Env, user), "https://home.example.com/mcp")
	assertURL(t, Resolve(f.Env, local), "https://home.example.com/mcp")

	f.setenv(claudeConfigDirEnv, configDir)
	f.write(filepath.Join(configDir, ".claude.json"), claudeJSON("https://alt.example.com/mcp"))
	for _, req := range []ResolveRequest{user, local} {
		res := Resolve(f.Env, req)
		assertUnresolved(t, res, "CLAUDE_CONFIG_DIR is set ("+strconv.Quote(configDir)+")")
		assertUnresolved(t, res, "its "+req.ServerSource+" MCP servers")
		assertUnresolved(t, res, "start Claude Code without CLAUDE_CONFIG_DIR")
		if len(res.Trace) != 0 {
			t.Errorf("%s: read a .claude.json although neither can be trusted:\n%s", req.ServerSource, resolveTrace(res))
		}
	}

	const elsewhere = "https://elsewhere.example.com/mcp"
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{"proj":{"type":"http","url":"`+elsewhere+`"}}}`)
	f.write(f.Env.managedMCPPath(), `{"mcpServers":{"ent":{"type":"http","url":"`+elsewhere+`"}}}`)
	f.write(f.Env.claudeManagedPath(claudeManagedSettings), `{"managedMcpServers":{"mgd":`+managedServer(elsewhere)+`}}`)
	for source, name := range map[string]string{claudeSourceProject: "proj", claudeSourceEnterprise: "ent", claudeSourceManaged: "mgd"} {
		t.Run(source, func(t *testing.T) { assertURL(t, Resolve(f.Env, claudeCodeReq(source, name, project)), elsewhere) })
	}
	if res := Resolve(f.Env, claudeCodeReq("dynamic", "computer-use", project)); res.Unresolved {
		t.Errorf("a built-in server was unresolved: %s", res.Reason)
	}
}

// TestClaudeCodeConfigDirIsUsedVerbatim covers values that only differ from
// another directory, or from unset, by whitespace. Claude Code does not trim the
// variable, so trimming it here would read a plugin registry the session does
// not.
func TestClaudeCodeConfigDirIsUsedVerbatim(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	req := pluginReq("tool", "myserver", project)
	install := func(configDir, url string) {
		dir := f.mkdir(filepath.Join(configDir, "plugins", "cache", "mkt", "tool", "1.0.0"))
		f.write(filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"myserver":{"type":"http","url":"`+url+`"}}}`)
		f.pluginRegistry(configDir, `{"version":2,"plugins":{"tool@mkt":[{"scope":"user","installPath":`+quote(dir)+`}]}}`)
	}
	install(f.homePath(".claude"), "https://home.example.com/mcp")
	install(f.path("alt"), "https://alt.example.com/mcp")
	assertURL(t, Resolve(f.Env, req), "https://home.example.com/mcp")

	// Windows strips trailing spaces from a path's final name, so a directory
	// named "alt " cannot exist there.
	if runtime.GOOS != "windows" {
		install(f.path("alt "), "https://alt-space.example.com/mcp")
		f.setenv(claudeConfigDirEnv, f.path("alt "))
		assertURL(t, Resolve(f.Env, req), "https://alt-space.example.com/mcp")
	}

	// Leading whitespace makes an absolute directory relative. Trimmed, it
	// would read the "alt" registry, which this session does not.
	f.setenv(claudeConfigDirEnv, " "+f.path("alt"))
	assertUnresolved(t, Resolve(f.Env, req), "relative path")

	// Only spaces is a relative directory name to Claude Code, not unset, so it
	// must not fall back to the home directory.
	f.setenv(claudeConfigDirEnv, " ")
	assertUnresolved(t, Resolve(f.Env, req), "relative path")
}

// TestClaudeCodeRelativeConfigDirIsDenied covers a relative CLAUDE_CONFIG_DIR.
// Claude Code resolves it against its launch directory, which the hook cannot
// know, so nothing is resolved and the reason says what to change.
func TestClaudeCodeRelativeConfigDirIsDenied(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	f.write(filepath.Join(project, "alt-claude", ".claude.json"), `{"mcpServers":{"myserver":{"type":"http","url":"https://alt.example.com/sse"}}}`)

	f.setenv(claudeConfigDirEnv, "./alt-claude")
	res := Resolve(f.Env, claudeCodeReq(claudeSourceUser, "myserver", project))
	assertUnresolved(t, res, `CLAUDE_CONFIG_DIR is set to a relative path ("./alt-claude")`)
	assertUnresolved(t, res, "set it to an absolute path")
	if len(res.Trace) != 0 {
		t.Fatalf("resolution consulted configuration despite a relative %s:\n%s", claudeConfigDirEnv, resolveTrace(res))
	}
}

// TestClaudeCodeProjectsKeySpelling covers the two spellings of one directory.
// projects{} is keyed by the exact path string Claude Code wrote, so a key and a
// payload cwd that differ only by a trailing separator still have to meet.
func TestClaudeCodeProjectsKeySpelling(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	withSlash := project + string(filepath.Separator)
	local := func(cwd string) ResolveRequest { return claudeCodeReq(claudeSourceLocal, "myserver", cwd) }

	t.Run("key carries the separator", func(t *testing.T) {
		f.write(f.homePath(".claude.json"), `{"projects":{`+quote(withSlash)+`:{"mcpServers":{"myserver":{"type":"http","url":"https://raw.example.com/sse"}}}}}`)
		assertURL(t, Resolve(f.Env, local(withSlash)), "https://raw.example.com/sse")
		assertURL(t, Resolve(f.Env, local(project)), "https://raw.example.com/sse")
	})

	t.Run("cwd carries the separator", func(t *testing.T) {
		f.write(f.homePath(".claude.json"), `{"projects":{`+quote(project)+`:{"mcpServers":{"myserver":{"type":"http","url":"https://clean.example.com/sse"}}}}}`)
		assertURL(t, Resolve(f.Env, local(withSlash)), "https://clean.example.com/sse")
	})
}

// TestClaudeCodeProjectFilePrecedence covers the merge Claude Code performs over
// .mcp.json files, pinned against Claude Code itself: every file from the launch
// directory up is live, and a deeper file overrides a shallower one.
func TestClaudeCodeProjectFilePrecedence(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	cwd := f.mkdir(filepath.Join(project, "a", "b"))
	req := func(name string) ResolveRequest { return claudeCodeReq(claudeSourceProject, name, cwd) }
	f.write(filepath.Join(cwd, ".mcp.json"), `{"mcpServers":{"myserver":{"type":"http","url":"https://cwd-file.example.com/sse"}}}`)
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{
		"myserver":{"type":"http","url":"https://project-file.example.com/sse"},
		"outeronly":{"type":"http","url":"https://outer.example.com/sse"}
	}}`)

	assertURL(t, Resolve(f.Env, req("myserver")), "https://cwd-file.example.com/sse")
	assertURL(t, Resolve(f.Env, req("outeronly")), "https://outer.example.com/sse")

	// A file silent on the name defers to the one above it.
	f.write(filepath.Join(cwd, ".mcp.json"), `{"mcpServers":{}}`)
	assertURL(t, Resolve(f.Env, req("myserver")), "https://project-file.example.com/sse")
}

// TestClaudeCodeLocalScopeKeyAliases covers two projects{} keys spelling the
// session's root differently. Claude Code reads one of them, and which one
// cannot be reproduced reliably, so they must agree on a server.
func TestClaudeCodeLocalScopeKeyAliases(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	withSlash := project + string(filepath.Separator)
	req := claudeCodeReq(claudeSourceLocal, "x", project)

	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(project)+`:{"mcpServers":{"x":{"type":"http","url":"https://same.example.com/mcp"}}},
		`+quote(withSlash)+`:{"mcpServers":{"x":{"type":"http","url":"https://same.example.com/mcp"}}}
	}}`)
	assertURL(t, Resolve(f.Env, req), "https://same.example.com/mcp")

	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(project)+`:{"mcpServers":{"x":{"type":"http","url":"https://allowed.example.com/mcp"}}},
		`+quote(withSlash)+`:{"mcpServers":{"x":{"command":"/tmp/evil"}}}
	}}`)
	assertUnresolved(t, Resolve(f.Env, req), "conflicting definitions")

	// An alias that does not define the server leaves the other's definition.
	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(project)+`:{"mcpServers":{"x":{"type":"http","url":"https://only.example.com/mcp"}}},
		`+quote(withSlash)+`:{"mcpServers":{}}
	}}`)
	assertURL(t, Resolve(f.Env, req), "https://only.example.com/mcp")
}

// TestClaudeCodeLocalScopeIsOneKey covers which projects{} entry is live. Claude
// Code looks up exactly one key; an entry for a parent directory governs nothing,
// so resolving through it would name a server the session never loaded.
func TestClaudeCodeLocalScopeIsOneKey(t *testing.T) {
	f := newFixture(t, "darwin")
	parent := f.mkdir(f.path("parent"))
	cwd := f.mkdir(filepath.Join(parent, "child"))
	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(parent)+`:{"mcpServers":{"myserver":{"type":"http","url":"https://parent.example.com/sse"}}}
	}}`)

	assertUnresolved(t, Resolve(f.Env, claudeCodeReq(claudeSourceLocal, "myserver", cwd)), "did not find it there")
	assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceLocal, "myserver", parent)), "https://parent.example.com/sse")
}

// TestClaudeCodeLocalScopeIsKeyedByGitRoot covers a session launched below its
// repository root: Claude Code keys local configuration by the git root.
func TestClaudeCodeLocalScopeIsKeyedByGitRoot(t *testing.T) {
	f := newFixture(t, "darwin")
	repo := f.mkdir(f.path("repo"))
	f.mkdir(filepath.Join(repo, ".git"))
	cwd := f.mkdir(filepath.Join(repo, "pkg", "sub"))
	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(repo)+`:{"mcpServers":{"myserver":{"type":"http","url":"https://repo.example.com/sse"}}},
		`+quote(cwd)+`:{"mcpServers":{"myserver":{"type":"http","url":"https://sub.example.com/sse"}}}
	}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceLocal, "myserver", cwd)), "https://repo.example.com/sse")
}

// linkWorktree lays out a linked worktree at worktree for the repository whose
// common git directory is common, as git worktree add does: a .git file naming
// <common>/worktrees/<name>, which holds a commondir and a gitdir backlink.
func (f *fixture) linkWorktree(common, worktree string) {
	f.t.Helper()
	gitDir := f.mkdir(filepath.Join(common, "worktrees", filepath.Base(worktree)))
	f.write(filepath.Join(gitDir, "commondir"), "../..\n")
	f.write(filepath.Join(gitDir, "gitdir"), filepath.Join(worktree, ".git")+"\n")
	f.write(filepath.Join(f.mkdir(worktree), ".git"), "gitdir: "+gitDir+"\n")
}

// TestClaudeCodeWorktreeSharesMainCheckoutConfig covers a linked worktree, which
// lives outside its repository's tree and still loads the main checkout's local
// configuration. A submodule, by contrast, is keyed by its own directory.
func TestClaudeCodeWorktreeSharesMainCheckoutConfig(t *testing.T) {
	f := newFixture(t, "darwin")
	main := f.mkdir(f.path("main"))
	f.linkWorktree(filepath.Join(main, ".git"), f.path("feature"))

	sub := f.mkdir(filepath.Join(main, "vendor", "lib"))
	f.mkdir(filepath.Join(main, ".git", "modules", "lib"))
	f.write(filepath.Join(sub, ".git"), "gitdir: ../../.git/modules/lib\n")

	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(main)+`:{"mcpServers":{"myserver":{"type":"http","url":"https://main.example.com/sse"}}},
		`+quote(sub)+`:{"mcpServers":{"myserver":{"type":"http","url":"https://submodule.example.com/sse"}}}
	}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceLocal, "myserver", f.path("feature"))), "https://main.example.com/sse")
	assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceLocal, "myserver", sub)), "https://submodule.example.com/sse")
}

// TestClaudeCodeBareRepositoryWorktree covers a worktree of a bare repository,
// which Claude Code keys by the repository directory itself.
func TestClaudeCodeBareRepositoryWorktree(t *testing.T) {
	f := newFixture(t, "darwin")
	bare := f.mkdir(f.path("app.git"))
	worktree := filepath.Join(bare, "main")
	f.linkWorktree(bare, worktree)
	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(bare)+`:{"mcpServers":{"myserver":{"type":"http","url":"https://bare.example.com/sse"}}},
		`+quote(worktree)+`:{"mcpServers":{"other":{"type":"http","url":"https://worktree.example.com/sse"}}}
	}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceLocal, "myserver", worktree)), "https://bare.example.com/sse")
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq(claudeSourceLocal, "other", worktree)), "did not find it there")
}

// TestClaudeCodeForgedWorktreeKeysItself covers a .git file that names another
// repository's commondir without being one of its worktrees, as a tree copied out
// of an archive can. Claude Code keys such a directory by itself; borrowing the
// named repository's entry would report its servers for a session that never
// loaded them.
func TestClaudeCodeForgedWorktreeKeysItself(t *testing.T) {
	f := newFixture(t, "darwin")
	trusted := f.mkdir(f.path("trusted"))
	f.linkWorktree(filepath.Join(trusted, ".git"), f.path("real-worktree"))
	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(trusted)+`:{"mcpServers":{"linear":{"type":"http","url":"https://allowed.example.com/sse"}}}
	}}`)

	t.Run("git dir outside worktrees", func(t *testing.T) {
		evil := f.mkdir(f.path("evil"))
		gitDir := f.mkdir(filepath.Join(evil, "g"))
		f.write(filepath.Join(gitDir, "commondir"), filepath.Join(trusted, ".git")+"\n")
		f.write(filepath.Join(gitDir, "gitdir"), filepath.Join(evil, ".git")+"\n")
		f.write(filepath.Join(evil, ".git"), "gitdir: "+gitDir+"\n")

		assertUnresolved(t, Resolve(f.Env, claudeCodeReq(claudeSourceLocal, "linear", evil)), "did not find it there")
	})

	t.Run("backlink to another worktree", func(t *testing.T) {
		evil := f.mkdir(f.path("evil2"))
		f.write(filepath.Join(evil, ".git"), "gitdir: "+filepath.Join(trusted, ".git", "worktrees", "real-worktree")+"\n")

		assertUnresolved(t, Resolve(f.Env, claudeCodeReq(claudeSourceLocal, "linear", evil)), "did not find it there")
	})
}

// TestClaudeCodeSymlinkedGitDir covers a checkout whose .git is a symlink to its
// git directory, as the repo tool makes them. Claude Code follows the link and
// keys the session by the checkout; skipping it would walk past the checkout and
// read a subdirectory's or an outer repository's entry instead.
func TestClaudeCodeSymlinkedGitDir(t *testing.T) {
	f := newFixture(t, "darwin")
	gitDir := f.mkdir(f.path("gitdirs", "checkout"))
	checkout := f.mkdir(f.path("checkout"))
	if err := os.Symlink(gitDir, filepath.Join(checkout, ".git")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	cwd := f.mkdir(filepath.Join(checkout, "sub"))
	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(checkout)+`:{"mcpServers":{"myserver":{"type":"http","url":"https://checkout.example.com/sse"}}},
		`+quote(cwd)+`:{"mcpServers":{"subonly":{"type":"http","url":"https://sub.example.com/sse"}}}
	}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceLocal, "myserver", cwd)), "https://checkout.example.com/sse")
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq(claudeSourceLocal, "subonly", cwd)), "did not find it there")
}

// TestClaudeCodeProjectDir covers a session whose cwd is not the directory it
// loaded its configuration for: one that cd'd into a nested repository, or one
// resumed elsewhere that cd'd into the directory it first started in. Claude
// Code keeps the configuration of the directory it was started or resumed in,
// which CLAUDE_PROJECT_DIR names, so that directory is read and cwd is not.
func TestClaudeCodeProjectDir(t *testing.T) {
	f := newFixture(t, "darwin")
	workspace := f.mkdir(f.path("workspace"))
	nested := f.mkdir(filepath.Join(workspace, "nested.repo"))
	f.mkdir(filepath.Join(nested, ".git"))
	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(workspace)+`:{"mcpServers":{"local":{"type":"http","url":"https://workspace-local.example.com/sse"}}},
		`+quote(nested)+`:{"mcpServers":{"local":{"type":"http","url":"https://nested-local.example.com/sse"}}}
	}}`)
	f.write(filepath.Join(nested, ".mcp.json"), `{"mcpServers":{"probe":{"type":"http","url":"https://allowed.example.com/mcp"}}}`)
	resumed := f.mkdir(f.path("elsewhere"))
	f.write(filepath.Join(resumed, ".mcp.json"), `{"mcpServers":{"probe":{"command":"/tmp/real"}}}`)

	req := claudeCodeReq(claudeSourceLocal, "local", nested)
	req.ProjectDir = workspace
	assertURL(t, Resolve(f.Env, req), "https://workspace-local.example.com/sse")

	req = claudeCodeReq(claudeSourceProject, "probe", nested)
	assertURL(t, Resolve(f.Env, req), "https://allowed.example.com/mcp")
	req.ProjectDir = resumed
	assertCommand(t, Resolve(f.Env, req), "/tmp/real")

	// Without a usable project directory nothing is trusted, cwd included.
	for _, dir := range []string{"", "relative/dir", "/" + strings.Repeat("x", maxWorkingDirBytes)} {
		req.ProjectDir = dir
		assertUnresolved(t, Resolve(f.Env, req), ClaudeProjectDirEnv)
	}
}

// TestClaudeCodeNonGitProject covers a project tree that is not a checkout.
func TestClaudeCodeNonGitProject(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("loose"))
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{"myserver":{"type":"http","url":"https://loose.example.com/sse"}}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceProject, "myserver", filepath.Join(project, "sub"))), "https://loose.example.com/sse")
}

// TestClaudeCodeMalformedFilesAreSkipped covers a project file that cannot be
// parsed, which Claude Code skips: the trace records it as such, and a shallower
// file still answers.
func TestClaudeCodeMalformedFilesAreSkipped(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	cwd := f.mkdir(filepath.Join(project, "sub"))
	f.write(filepath.Join(cwd, ".mcp.json"), `{"mcpServers": {`)
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{"myserver":{"type":"http","url":"https://outer.example.com/sse"}}}`)

	res := Resolve(f.Env, claudeCodeReq(claudeSourceProject, "myserver", cwd))
	assertURL(t, res, "https://outer.example.com/sse")
	i := slices.Index(consultedPaths(res), filepath.Join(cwd, ".mcp.json"))
	if i < 0 || res.Trace[i].Note != "unreadable or malformed" {
		t.Fatalf("expected the malformed file to be recorded as such:\n%s", resolveTrace(res))
	}
}

// TestClaudeCodeStrictJSON covers configuration carrying comments or a trailing
// comma. Claude Code parses its files with JSON.parse and skips one it cannot;
// reading past the error would report a server the session never loaded.
func TestClaudeCodeStrictJSON(t *testing.T) {
	for name, body := range map[string]string{
		"trailing comma": `{"mcpServers":{"myserver":{"type":"http","url":"https://decoy.example.com/mcp"},}}`,
		"line comment":   "{\"mcpServers\":{ // decoy\n\"myserver\":{\"type\":\"http\",\"url\":\"https://decoy.example.com/mcp\"}}}",
		"block comment":  `{"mcpServers":{/* decoy */"myserver":{"type":"http","url":"https://decoy.example.com/mcp"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "darwin")
			project := f.mkdir(f.path("proj"))
			f.write(filepath.Join(project, ".mcp.json"), body)
			assertUnresolved(t, Resolve(f.Env, claudeCodeReq(claudeSourceProject, "myserver", project)), "did not find it there")
		})
	}
}

// TestClaudeCodeStdioResolution covers an entry that launches a package runner,
// including the executable being reported alongside an unresolved package.
func TestClaudeCodeStdioResolution(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	req := claudeCodeReq(claudeSourceProject, "myserver", project)

	f.write(filepath.Join(project, ".mcp.json"),
		`{"mcpServers":{"myserver":{"command":"npx","args":["-y","myserver-mcp@1.2.3","--token","secret"]}}}`)
	res := Resolve(f.Env, req)
	assertPackage(t, res, "npm", "myserver-mcp", "1.2.3")
	if res.Identity.Command != "npx" {
		t.Fatalf("Command = %q, want the bare executable", res.Identity.Command)
	}

	f.write(filepath.Join(project, ".mcp.json"),
		`{"mcpServers":{"myserver":{"command":"node","args":["/srv/server.js","--token","secret"]}}}`)
	res = Resolve(f.Env, req)
	assertUnresolved(t, res, `stdio command "node" is not a supported package runner`)
	if res.Identity.Command != "node" {
		t.Fatalf("Command = %q, want the executable recorded even on an unresolved call", res.Identity.Command)
	}
	if res.Identity.Package != nil {
		t.Fatalf("Package = %+v, want none", res.Identity.Package)
	}
}

// TestClaudeCodeHostnameIsLeftToTheBackend covers hostname entries: the backend
// derives the hostname from the URL for both the log row and the matcher, so the
// device does not populate it.
func TestClaudeCodeHostnameIsLeftToTheBackend(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{"g":{"type":"http","url":"https://gitmcp.io/obot/x"}}}`)

	res := Resolve(f.Env, claudeCodeReq(claudeSourceProject, "g", project))
	if res.Identity.Hostname != "" {
		t.Fatalf("Hostname = %q, want it left for the backend to derive", res.Identity.Hostname)
	}
}

// quote renders s as a JSON string, so a Windows path's separators survive into
// the fixture config.
func quote(s string) string {
	return string(mustJSON(s))
}

// projectKeysJSON renders a ~/.claude.json projects{} block with an empty servers
// table per key, for tests that care only about which keys become scopes.
func projectKeysJSON(keys ...string) string {
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, quote(key)+`:{"mcpServers":{}}`)
	}
	return `{"projects":{` + strings.Join(entries, ",") + `}}`
}

// scopeKeys lists the scopes a project walk produced, as path + location.
func scopeKeys(scopes []scope) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, s.path+"  "+s.key)
	}
	return out
}

// localScopesFrom returns the local scope for a session started in cwd, against
// a fixture's ~/.claude.json.
func localScopesFrom(t *testing.T, f *fixture, cwd string) []scope {
	t.Helper()
	claudePath := f.homePath(".claude.json")
	var claude claudeJSON
	res := loadJSON(claudePath, &claude)
	loader := newConfigLoader()
	project, _ := claudeProjectFor(t.Context(), loader, cwd)
	return claudeLocalScopes(f.Env, project, claudePath, claude, res)
}

// projectFileScopesFrom returns the .mcp.json scopes for a session started in cwd.
func projectFileScopesFrom(t *testing.T, f *fixture, cwd string) []scope {
	t.Helper()
	loader := newConfigLoader()
	project, _ := claudeProjectFor(t.Context(), loader, cwd)
	return claudeProjectFileScopes(loader, project)
}

func TestAncestorsNearestFirst(t *testing.T) {
	root := hostRoot(t)
	got := ancestors(filepath.Join(root, "a", "b", "c"))
	want := []string{
		filepath.Join(root, "a", "b", "c"),
		filepath.Join(root, "a", "b"),
		filepath.Join(root, "a"),
		root,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ancestors = %v, want %v", got, want)
	}
}

func TestAncestorsEmptyCWD(t *testing.T) {
	if got := ancestors("   "); got != nil {
		t.Fatalf("ancestors = %v, want nil", got)
	}
}

func TestAncestorsRejectsRelativeCWD(t *testing.T) {
	if got := ancestors(filepath.Join("project", "subdir")); got != nil {
		t.Fatalf("ancestors = %v, want nil for a relative cwd", got)
	}
}

// TestClaudeCodeDeepProjectDir covers a session started far below its
// repository: Claude Code walks all the way up for the git root and for .mcp.json
// files, so the resolver must too.
func TestClaudeCodeDeepProjectDir(t *testing.T) {
	f := newFixture(t, "darwin")
	repo := f.mkdir(f.path("repo"))
	f.mkdir(filepath.Join(repo, ".git"))
	cwd := f.mkdir(filepath.Join(repo, strings.Repeat("d"+string(filepath.Separator), 60)))
	f.write(filepath.Join(repo, ".mcp.json"), `{"mcpServers":{"p":{"type":"http","url":"https://repo-file.example.com/mcp"}}}`)
	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(repo)+`:{"mcpServers":{"x":{"command":"/tmp/repo"}}},
		`+quote(cwd)+`:{"mcpServers":{"x":{"type":"http","url":"https://allowed.example.com/mcp"}}}
	}}`)

	assertCommand(t, Resolve(f.Env, claudeCodeReq(claudeSourceLocal, "x", cwd)), "/tmp/repo")
	assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceProject, "p", cwd)), "https://repo-file.example.com/mcp")
}

// TestProjectFileScopesOrder pins the ordering contract: every project file from
// the project directory up, deepest first, with consecutive ranks.
func TestProjectFileScopesOrder(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	cwd := f.mkdir(filepath.Join(project, "a", "b"))
	f.write(filepath.Join(cwd, ".mcp.json"), `{"mcpServers":{}}`)
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{}}`)

	scopes := projectFileScopesFrom(t, f, cwd)
	want := []string{
		filepath.Join(cwd, ".mcp.json") + "  mcpServers",
		filepath.Join(project, ".mcp.json") + "  mcpServers",
	}
	if got := scopeKeys(scopes); !slices.Equal(got, want) {
		t.Fatalf("scopes\n%v\nwant\n%v", got, want)
	}
	for i, s := range scopes {
		if s.rank != i {
			t.Fatalf("scope %d has rank %d: ranks must be consecutive", i, s.rank)
		}
	}
}

// TestProjectFileScopesNamesTheAbsentProjectFile covers the diagnostic: with no
// project file anywhere, the path an operator expects to be read is still named,
// so a trace says it is absent rather than omitting it.
func TestProjectFileScopesNamesTheAbsentProjectFile(t *testing.T) {
	f := newFixture(t, "darwin")
	cwd := f.mkdir(f.path("proj"))

	got := scopeKeys(projectFileScopesFrom(t, f, cwd))
	want := []string{filepath.Join(cwd, ".mcp.json") + "  mcpServers"}
	if !slices.Equal(got, want) {
		t.Fatalf("scopes\n%v\nwant\n%v", got, want)
	}
}

// TestProjectFileScopesNeverReadTheRootProjectFile covers a session launched at
// the filesystem root. Claude Code reads no .mcp.json there, so the absent-file
// placeholder must not name one: as a scope it would be loaded.
func TestProjectFileScopesNeverReadTheRootProjectFile(t *testing.T) {
	root := hostRoot(t)
	if scopes := claudeProjectFileScopes(newConfigLoader(), claudeProject{projectDir: root, root: root}); len(scopes) != 0 {
		t.Fatalf("root launch produced project file scopes: %v", scopeKeys(scopes))
	}
}

// TestProjectFileScopesDirectoryIsNotAProjectFile covers a .mcp.json that is a
// directory: it cannot be read, so it is not a scope, and the real one further up
// still is.
func TestProjectFileScopesDirectoryIsNotAProjectFile(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	cwd := f.mkdir(filepath.Join(project, "sub"))
	f.mkdir(filepath.Join(cwd, ".mcp.json"))
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{}}`)

	got := scopeKeys(projectFileScopesFrom(t, f, cwd))
	want := []string{filepath.Join(project, ".mcp.json") + "  mcpServers"}
	if !slices.Equal(got, want) {
		t.Fatalf("scopes\n%v\nwant\n%v", got, want)
	}
}

// TestLocalScopesIgnoresUnrelatedProjectKeys covers the filter: a configured
// project that is not this session's governs nothing here.
func TestLocalScopesIgnoresUnrelatedProjectKeys(t *testing.T) {
	f := newFixture(t, "darwin")
	cwd := f.mkdir(f.path("here"))
	f.write(f.homePath(".claude.json"), projectKeysJSON(f.path("elsewhere"), f.path("here-too"), f.Root))

	for _, s := range localScopesFrom(t, f, cwd) {
		if strings.Contains(s.key, "projects[") {
			t.Fatalf("consulted an unrelated project: %s", s.key)
		}
	}
}

// TestLocalScopesStableAcrossSpellings covers two projects{} keys naming one
// directory. Map order is not stable, so the key reported has to be chosen rather
// than whichever came out first.
func TestLocalScopesStableAcrossSpellings(t *testing.T) {
	f := newFixture(t, "darwin")
	cwd := f.mkdir(f.path("proj"))
	f.write(f.homePath(".claude.json"), projectKeysJSON(cwd, cwd+string(filepath.Separator)))

	first := scopeKeys(localScopesFrom(t, f, cwd))
	for range 20 {
		if got := scopeKeys(localScopesFrom(t, f, cwd)); !slices.Equal(got, first) {
			t.Fatalf("scopes vary across runs:\n%v\n%v", got, first)
		}
	}
}

// TestComparableDirCaseFoldsOnWindows covers matching a projects{} key written by
// an earlier launch that spelled the same directory in another case.
func TestComparableDirCaseFoldsOnWindows(t *testing.T) {
	windows := Env{GOOS: "windows"}
	if got, want := windows.comparableDir(`C:\Users\Dev\Proj`), `c:\users\dev\proj`; got != want {
		t.Fatalf("comparableDir = %q, want %q", got, want)
	}

	// Elsewhere the same case is a different directory. The path is written in the
	// host's form, since comparableDir cleans with the host's separator.
	darwin := Env{GOOS: "darwin"}
	mixed := filepath.Join(hostRoot(t), "Users", "Dev", "Proj")
	if got := darwin.comparableDir(mixed); got != mixed {
		t.Fatalf("comparableDir = %q, want the path unchanged", got)
	}
	if got := darwin.comparableDir(mixed + string(filepath.Separator)); got != mixed {
		t.Fatalf("comparableDir = %q, want it cleaned", got)
	}
	// Whitespace is part of a directory's name.
	if got := darwin.comparableDir(mixed + " "); got != mixed+" " {
		t.Fatalf("comparableDir = %q, want the trailing space kept", got)
	}
}
