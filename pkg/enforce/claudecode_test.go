package enforce

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

// claudeCodeReq builds a Claude Code resolve request.
func claudeCodeReq(serverName, cwd string) ResolveRequest {
	return ResolveRequest{Agent: localagent.ClaudeCode, ServerName: serverName, CWD: cwd}
}

// TestClaudeCodeFileOrder pins the order of sources for a server that is in none
// of them. The order is the contract: a server defined in two places must resolve
// to the one the agent would have used.
func TestClaudeCodeFileOrder(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")

	// With no ~/.claude.json there is no per-project section to consult, so the
	// file appears once, for the global servers table.
	res := Resolve(f.Env, claudeCodeReq("myserver", project))
	assertUnresolved(t, res, `MCP server "myserver" was not found in any Claude Code MCP configuration`)

	want := []string{
		f.Env.managedMCPPath(),
		f.Env.claudeManagedPath(claudeManagedSettings),
		filepath.Join(project, ".mcp.json"),
		f.homePath(".claude.json"),
	}
	if got := consultedPaths(res); !slices.Equal(got, want) {
		t.Fatalf("consulted\n%v\nwant\n%v\n%s", got, want, resolveTrace(res))
	}

	// Once it exists, the project's local section is consulted ahead of the project
	// .mcp.json: Claude Code lets local configuration override the shared file.
	f.write(f.homePath(".claude.json"), `{"projects":{`+quote(project)+`:{"mcpServers":{}}}}`)
	res = Resolve(f.Env, claudeCodeReq("myserver", project))
	want = []string{
		f.Env.managedMCPPath(),
		f.Env.claudeManagedPath(claudeManagedSettings),
		f.homePath(".claude.json"), // projects[<project>]
		filepath.Join(project, ".mcp.json"),
		f.homePath(".claude.json"), // mcpServers
	}
	if got := consultedPaths(res); !slices.Equal(got, want) {
		t.Fatalf("consulted\n%v\nwant\n%v\n%s", got, want, resolveTrace(res))
	}
	if key, want := res.Trace[2].Key, fmt.Sprintf("projects[%q].mcpServers", project); key != want {
		t.Fatalf("trace step 2 key = %q, want %q", key, want)
	}
}

// TestClaudeCodeManagedConfigIsExclusive covers the one place the resolver takes a
// position on whether the agent would have run a server. Claude Code documents
// the managed MCP set as something users cannot override, so a managed file that
// exists and lacks the server ends resolution instead of falling through.
func TestClaudeCodeManagedConfigIsExclusive(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"myserver":{"url":"https://user.example.com/sse"}}}`)

	// With no managed file the user config answers.
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "https://user.example.com/sse")

	// A managed file that lists a different server stops resolution: the user
	// entry is not consulted at all.
	f.write(f.Env.managedMCPPath(), `{"mcpServers":{"github":{"url":"https://managed.example.com/sse"}}}`)
	res := Resolve(f.Env, claudeCodeReq("myserver", project))
	assertUnresolved(t, res, "managed MCP configuration, which cannot be overridden")
	if slices.Contains(consultedPaths(res), f.homePath(".claude.json")) {
		t.Fatalf("resolution continued past the managed config:\n%s", resolveTrace(res))
	}

	// A managed file that does list it wins.
	f.write(f.Env.managedMCPPath(), `{"mcpServers":{"myserver":{"url":"https://managed.example.com/sse"}}}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "https://managed.example.com/sse")

	// One that cannot be parsed still locks. Claude Code keeps a broken managed
	// config in exclusive control rather than letting user configuration through.
	f.write(f.Env.managedMCPPath(), `{"mcpServers":`)
	res = Resolve(f.Env, claudeCodeReq("myserver", project))
	assertUnresolved(t, res, "managed MCP configuration, which cannot be overridden")
	if slices.Contains(consultedPaths(res), f.homePath(".claude.json")) {
		t.Fatalf("resolution continued past an unreadable managed config:\n%s", resolveTrace(res))
	}
}

// TestClaudeCodeManagedConfigWindows covers the Windows managed directory, which
// Claude Code hardcodes. %PROGRAMFILES% is inherited from the agent and Claude
// Code ignores it; were the resolver to follow it, a user could plant a
// managed-mcp.json that vouches for a server the session never loaded.
func TestClaudeCodeManagedConfigWindows(t *testing.T) {
	f := newFixture(t, "windows")
	project := f.path("proj")
	f.write(f.machinePath("C:/Program Files/ClaudeCode/managed-mcp.json"),
		`{"mcpServers":{"myserver":{"url":"https://managed.example.com/sse"}}}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "https://managed.example.com/sse")

	planted := f.mkdir(f.path("planted"))
	f.setenv("PROGRAMFILES", planted)
	f.write(filepath.Join(planted, "ClaudeCode", "managed-mcp.json"),
		`{"mcpServers":{"myserver":{"url":"https://allowed.example.com/sse"}}}`)
	res := Resolve(f.Env, claudeCodeReq("myserver", project))
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

// TestClaudeCodeManagedSettingsServers covers managedMcpServers, the managed
// settings key that delivers servers. It outranks user configuration, joins
// managed-mcp.json's set under its lock, and yields to managed-mcp.json on a
// shared name.
func TestClaudeCodeManagedSettingsServers(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"myserver":{"url":"https://user.example.com/sse"}}}`)
	f.write(f.Env.claudeManagedPath(claudeManagedSettings),
		`{"managedMcpServers":{"myserver":`+managedServer("https://settings.example.com/sse")+`}}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "https://settings.example.com/sse")

	f.write(f.Env.managedMCPPath(), `{"mcpServers":{"other":{"url":"https://managed.example.com/sse"}}}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "https://settings.example.com/sse")

	f.write(f.Env.managedMCPPath(), `{"mcpServers":{"myserver":{"url":"https://managed.example.com/sse"}}}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "https://managed.example.com/sse")
}

// TestClaudeCodeManagedSettingsRejectedEntries covers the entries Claude Code
// ignores in managedMcpServers: anything but an "http" or "sse" server at an
// https URL. An ignored entry overrides nothing, so the name falls through to the
// next definition, which is the one the session runs.
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
			f.write(f.homePath(".claude.json"), `{"mcpServers":{"myserver":{"url":"https://user.example.com/sse"}}}`)
			f.write(f.Env.claudeManagedPath(claudeManagedSettings), `{"managedMcpServers":{"myserver":`+entry+`}}`)
			assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", f.path("proj"))), "https://user.example.com/sse")
		})
	}

	// The scheme is matched without regard to case, as Claude Code matches it.
	f := newFixture(t, "darwin")
	f.write(f.Env.claudeManagedPath(claudeManagedSettings),
		`{"managedMcpServers":{"myserver":{"type":"sse","url":"HTTPS://managed.example.com/sse"}}}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", f.path("proj"))), "https://managed.example.com/sse")
}

// TestClaudeCodeManagedSettingsDropIns covers managed-settings.d as Claude Code
// layers it: managed-settings.json first, then drop-ins in byte order of file
// name, a later one overriding an earlier one. An entry a later file defines in a
// form Claude Code rejects leaves the earlier definition in force.
func TestClaudeCodeManagedSettingsDropIns(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
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

	assertURL(t, Resolve(f.Env, claudeCodeReq("base", project)), "https://nine.example.com/sse")
	assertURL(t, Resolve(f.Env, claudeCodeReq("case", project)), "https://lower.example.com/sse")
	assertURL(t, Resolve(f.Env, claudeCodeReq("kept", project)), "https://kept.example.com/sse")

	// Only files directly in the directory whose names end in a lowercase ".json"
	// and do not start with a dot are drop-ins.
	for _, file := range []string{".hidden.json", "x.txt", "U.JSON", filepath.Join("sub", "s.json")} {
		dropIn(file, "ignored", "https://ignored.example.com/sse")
	}
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq("ignored", project)), "was not found")
}

// TestClaudeCodeManagedSettingsIncomplete covers a managed-settings.d the
// resolver cannot read in full. Managed servers outrank all others, and an unread
// drop-in can override a read one, so nothing below managed-mcp.json can answer.
func TestClaudeCodeManagedSettingsIncomplete(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"myserver":{"url":"https://user.example.com/sse"}}}`)
	for i := range maxClaudeManagedDropIns + 1 {
		f.write(f.Env.claudeManagedPath(claudeManagedDropInDir, fmt.Sprintf("%04d.json", i)), `{}`)
	}
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "could not be read in full")

	// Including a name a read drop-in defines: an unread one could redefine it.
	f.write(f.Env.claudeManagedPath(claudeManagedDropInDir, "0000.json"),
		`{"managedMcpServers":{"early":`+managedServer("https://early.example.com/sse")+`}}`)
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq("early", project)), "could not be read in full")

	// managed-mcp.json outranks every drop-in, so its answer stands.
	f.write(f.Env.managedMCPPath(), `{"mcpServers":{"myserver":{"url":"https://managed.example.com/sse"}}}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "https://managed.example.com/sse")
}

// TestClaudeCodeManagedSettingsUnreadableDropIns covers a drop-in location
// Claude Code cannot read either. It skips it silently and loads everything else,
// so the resolver does the same rather than denying every call.
func TestClaudeCodeManagedSettingsUnreadableDropIns(t *testing.T) {
	userServer := `{"mcpServers":{"myserver":{"url":"https://user.example.com/sse"}}}`

	t.Run("not a directory", func(t *testing.T) {
		f := newFixture(t, "darwin")
		f.write(f.homePath(".claude.json"), userServer)
		f.write(f.Env.claudeManagedPath(claudeManagedDropInDir), "not a directory")
		assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", f.path("proj"))), "https://user.example.com/sse")
	})

	t.Run("permission denied", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs POSIX permissions enforced against the test process")
		}
		f := newFixture(t, "darwin")
		f.write(f.homePath(".claude.json"), userServer)
		dir := f.Env.claudeManagedPath(claudeManagedDropInDir)
		f.write(filepath.Join(dir, "10.json"), `{"managedMcpServers":{"myserver":`+managedServer("https://hidden.example.com/sse")+`}}`)
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", f.path("proj"))), "https://user.example.com/sse")
	})
}

// TestClaudeCodeConfigDirRelocatesClaudeJSON covers a session started with
// CLAUDE_CONFIG_DIR. Claude Code then reads .claude.json from that directory and
// ignores the one in the home directory, so the resolver has to as well: the
// home file can hold an allowed server while the session runs a different one
// under the same name.
func TestClaudeCodeConfigDirRelocatesClaudeJSON(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	configDir := f.path("alt-claude")
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"myserver":{"url":"https://home.example.com/sse"}}}`)

	// Unset, the home file answers.
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "https://home.example.com/sse")

	// Set, the relocated file answers and the home file is never consulted.
	f.setenv(claudeConfigDirEnv, configDir)
	f.write(filepath.Join(configDir, ".claude.json"), `{"mcpServers":{"myserver":{"url":"https://alt.example.com/sse"}}}`)
	res := Resolve(f.Env, claudeCodeReq("myserver", project))
	assertURL(t, res, "https://alt.example.com/sse")
	if slices.Contains(consultedPaths(res), f.homePath(".claude.json")) {
		t.Fatalf("consulted the home .claude.json despite %s:\n%s", claudeConfigDirEnv, resolveTrace(res))
	}

	// A server only the home file defines is not one this session can run.
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"homeonly":{"url":"https://home.example.com/sse"}}}`)
	res = Resolve(f.Env, claudeCodeReq("homeonly", project))
	assertUnresolved(t, res, `MCP server "homeonly" was not found in any Claude Code MCP configuration`)
	want := []string{
		f.Env.managedMCPPath(),
		f.Env.claudeManagedPath(claudeManagedSettings),
		filepath.Join(project, ".mcp.json"),
		filepath.Join(configDir, ".claude.json"),
	}
	if got := consultedPaths(res); !slices.Equal(got, want) {
		t.Fatalf("consulted\n%v\nwant\n%v\n%s", got, want, resolveTrace(res))
	}
}

// TestClaudeCodeConfigDirRelocatesProjectsAndConnectors covers the two other
// things the resolver reads from .claude.json, which move with it.
func TestClaudeCodeConfigDirRelocatesProjectsAndConnectors(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	configDir := f.path("alt-claude")
	f.setenv(claudeConfigDirEnv, configDir)
	f.write(filepath.Join(configDir, ".claude.json"), `{`+
		`"projects":{`+quote(project)+`:{"mcpServers":{"myserver":{"url":"https://project.example.com/sse"}}}},`+
		`"claudeAiMcpEverConnected":["claude.ai Linear"]}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "https://project.example.com/sse")

	res := Resolve(f.Env, claudeCodeReq("claude_ai_Linear", project))
	if res.Identity.Connector != "claude.ai Linear" {
		t.Fatalf("connector = %q, want %q\n%s", res.Identity.Connector, "claude.ai Linear", resolveTrace(res))
	}
}

// TestClaudeCodeConfigDirIsUsedVerbatim covers values that only differ from
// another directory, or from unset, by whitespace. Claude Code does not trim the
// variable, so trimming it here would read a file the session does not.
func TestClaudeCodeConfigDirIsUsedVerbatim(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"myserver":{"url":"https://home.example.com/sse"}}}`)
	f.write(filepath.Join(f.path("alt"), ".claude.json"), `{"mcpServers":{"myserver":{"url":"https://alt.example.com/sse"}}}`)
	f.write(filepath.Join(f.path("alt "), ".claude.json"), `{"mcpServers":{"myserver":{"url":"https://alt-space.example.com/sse"}}}`)

	f.setenv(claudeConfigDirEnv, f.path("alt "))
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "https://alt-space.example.com/sse")

	// Only spaces is a relative directory name to Claude Code, not unset, so it
	// must not fall back to the home file.
	f.setenv(claudeConfigDirEnv, " ")
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "relative path")
}

// TestClaudeCodeRelativeConfigDirIsDenied covers a relative CLAUDE_CONFIG_DIR.
// Claude Code resolves it against its launch directory, which the hook cannot
// know, so nothing is resolved and the reason says what to change.
func TestClaudeCodeRelativeConfigDirIsDenied(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"myserver":{"url":"https://home.example.com/sse"}}}`)
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{"myserver":{"url":"https://project.example.com/sse"}}}`)
	f.write(filepath.Join(project, "alt-claude", ".claude.json"), `{"mcpServers":{"myserver":{"url":"https://alt.example.com/sse"}}}`)

	f.setenv(claudeConfigDirEnv, "./alt-claude")
	res := Resolve(f.Env, claudeCodeReq("myserver", project))
	assertUnresolved(t, res, `CLAUDE_CONFIG_DIR is set to a relative path ("./alt-claude")`)
	assertUnresolved(t, res, "set it to an absolute path")
	if len(res.Trace) != 0 {
		t.Fatalf("resolution consulted configuration despite a relative %s:\n%s", claudeConfigDirEnv, resolveTrace(res))
	}
}

// TestClaudeCodeProjectBeatsGlobal covers the ordinary precedence case.
func TestClaudeCodeProjectBeatsGlobal(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{"myserver":{"url":"https://project.example.com/sse"}}}`)
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"myserver":{"url":"https://global.example.com/sse"}}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "https://project.example.com/sse")
}

// TestClaudeCodeProjectsKeySpelling covers the two spellings of one directory.
// projects{} is keyed by the exact path string Claude Code wrote, so a key and a
// payload cwd that differ only by a trailing separator still have to meet.
func TestClaudeCodeProjectsKeySpelling(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	withSlash := project + string(filepath.Separator)

	t.Run("key carries the separator", func(t *testing.T) {
		f.write(f.homePath(".claude.json"), `{"projects":{`+quote(withSlash)+`:{"mcpServers":{"myserver":{"url":"https://raw.example.com/sse"}}}}}`)
		assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", withSlash)), "https://raw.example.com/sse")
		assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "https://raw.example.com/sse")
	})

	t.Run("cwd carries the separator", func(t *testing.T) {
		f.write(f.homePath(".claude.json"), `{"projects":{`+quote(project)+`:{"mcpServers":{"myserver":{"url":"https://clean.example.com/sse"}}}}}`)
		assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", withSlash)), "https://clean.example.com/sse")
	})
}

// TestClaudeCodeProjectPrecedence is the load-bearing ordering test, pinned
// against Claude Code itself: the project's local section outranks every
// .mcp.json, and among those a deeper file outranks a shallower one.
func TestClaudeCodeProjectPrecedence(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	cwd := f.mkdir(filepath.Join(project, "a", "b"))

	f.write(filepath.Join(cwd, ".mcp.json"), `{"mcpServers":{"myserver":{"url":"https://cwd-file.example.com/sse"}}}`)
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{"myserver":{"url":"https://project-file.example.com/sse"}}}`)
	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(cwd)+`:{"mcpServers":{"myserver":{"url":"https://local.example.com/sse"}}}
	}}`)

	// Local configuration overrides the shared project file, however deep it is.
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", cwd)), "https://local.example.com/sse")

	// Without it, the deepest project file answers.
	f.write(f.homePath(".claude.json"), `{}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", cwd)), "https://cwd-file.example.com/sse")

	// And a project file silent on the name defers to the one above it.
	f.write(filepath.Join(cwd, ".mcp.json"), `{"mcpServers":{}}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", cwd)), "https://project-file.example.com/sse")
}

// TestClaudeCodeEveryProjectFileIsLive covers the merge Claude Code performs over
// .mcp.json files: one in the launch directory does not hide one above it, so a
// server only the outer file declares still runs and must still resolve.
func TestClaudeCodeEveryProjectFileIsLive(t *testing.T) {
	f := newFixture(t, "darwin")
	outer := f.mkdir(f.path("outer"))
	inner := f.mkdir(filepath.Join(outer, "inner"))
	f.write(filepath.Join(outer, ".mcp.json"), `{"mcpServers":{"outeronly":{"url":"https://outer.example.com/sse"}}}`)
	f.write(filepath.Join(inner, ".mcp.json"), `{"mcpServers":{"inneronly":{"url":"https://inner.example.com/sse"}}}`)
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"outeronly":{"url":"https://user.example.com/sse"}}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("inneronly", inner)), "https://inner.example.com/sse")
	// The outer file, not the user table beneath it: reading only the nearest file
	// would report the user definition for a call the outer file's server served.
	assertURL(t, Resolve(f.Env, claudeCodeReq("outeronly", inner)), "https://outer.example.com/sse")
}

// TestClaudeCodeProjectFileAboveCWD covers a call made from a subdirectory of the
// project: the project file is above cwd, and it is still the live one.
func TestClaudeCodeProjectFileAboveCWD(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	cwd := f.mkdir(filepath.Join(project, "a", "b"))
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{"myserver":{"url":"https://project.example.com/sse"}}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", cwd)), "https://project.example.com/sse")
}

// TestClaudeCodeLocalScopeIsOneKey covers which projects{} entry is live. Claude
// Code looks up exactly one key; an entry for a parent directory governs nothing,
// so resolving through it would name a server the session never loaded.
func TestClaudeCodeLocalScopeIsOneKey(t *testing.T) {
	f := newFixture(t, "darwin")
	parent := f.mkdir(f.path("parent"))
	cwd := f.mkdir(filepath.Join(parent, "child"))
	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(parent)+`:{"mcpServers":{"myserver":{"url":"https://parent.example.com/sse"}}}
	}}`)

	res := Resolve(f.Env, claudeCodeReq("myserver", cwd))
	assertUnresolved(t, res, "was not found in any Claude Code MCP configuration")
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", parent)), "https://parent.example.com/sse")
}

// TestClaudeCodeLocalScopeIsKeyedByGitRoot covers a session launched below its
// repository root: Claude Code keys local configuration by the git root.
func TestClaudeCodeLocalScopeIsKeyedByGitRoot(t *testing.T) {
	f := newFixture(t, "darwin")
	repo := f.mkdir(f.path("repo"))
	f.mkdir(filepath.Join(repo, ".git"))
	cwd := f.mkdir(filepath.Join(repo, "pkg", "sub"))
	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(repo)+`:{"mcpServers":{"myserver":{"url":"https://repo.example.com/sse"}}},
		`+quote(cwd)+`:{"mcpServers":{"myserver":{"url":"https://sub.example.com/sse"}}}
	}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", cwd)), "https://repo.example.com/sse")
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
		`+quote(main)+`:{"mcpServers":{"myserver":{"url":"https://main.example.com/sse"}}},
		`+quote(sub)+`:{"mcpServers":{"myserver":{"url":"https://submodule.example.com/sse"}}}
	}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", f.path("feature"))), "https://main.example.com/sse")
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", sub)), "https://submodule.example.com/sse")
}

// TestClaudeCodeBareRepositoryWorktree covers a worktree of a bare repository,
// which Claude Code keys by the repository directory itself.
func TestClaudeCodeBareRepositoryWorktree(t *testing.T) {
	f := newFixture(t, "darwin")
	bare := f.mkdir(f.path("app.git"))
	worktree := filepath.Join(bare, "main")
	f.linkWorktree(bare, worktree)
	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(bare)+`:{"mcpServers":{"myserver":{"url":"https://bare.example.com/sse"}}},
		`+quote(worktree)+`:{"mcpServers":{"other":{"url":"https://worktree.example.com/sse"}}}
	}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", worktree)), "https://bare.example.com/sse")
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq("other", worktree)), "was not found")
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
		`+quote(trusted)+`:{"mcpServers":{"linear":{"url":"https://allowed.example.com/sse"}}}
	}}`)

	t.Run("git dir outside worktrees", func(t *testing.T) {
		evil := f.mkdir(f.path("evil"))
		gitDir := f.mkdir(filepath.Join(evil, "g"))
		f.write(filepath.Join(gitDir, "commondir"), filepath.Join(trusted, ".git")+"\n")
		f.write(filepath.Join(gitDir, "gitdir"), filepath.Join(evil, ".git")+"\n")
		f.write(filepath.Join(evil, ".git"), "gitdir: "+gitDir+"\n")
		f.write(filepath.Join(evil, ".mcp.json"), `{"mcpServers":{"linear":{"url":"https://evil.example.com/sse"}}}`)

		assertURL(t, Resolve(f.Env, claudeCodeReq("linear", evil)), "https://evil.example.com/sse")
	})

	t.Run("backlink to another worktree", func(t *testing.T) {
		evil := f.mkdir(f.path("evil2"))
		f.write(filepath.Join(evil, ".git"), "gitdir: "+filepath.Join(trusted, ".git", "worktrees", "real-worktree")+"\n")
		f.write(filepath.Join(evil, ".mcp.json"), `{"mcpServers":{"linear":{"url":"https://evil.example.com/sse"}}}`)

		assertURL(t, Resolve(f.Env, claudeCodeReq("linear", evil)), "https://evil.example.com/sse")
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
		`+quote(checkout)+`:{"mcpServers":{"myserver":{"url":"https://checkout.example.com/sse"}}},
		`+quote(cwd)+`:{"mcpServers":{"subonly":{"url":"https://sub.example.com/sse"}}}
	}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", cwd)), "https://checkout.example.com/sse")
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq("subonly", cwd)), "was not found")
}

// TestClaudeCodeLaunchDirFromTranscript covers a session whose cwd has moved since
// launch. Claude Code keeps the configuration it loaded for the launch directory,
// which the transcript path names; resolving from cwd would read a nested
// repository's files the session never loaded.
func TestClaudeCodeLaunchDirFromTranscript(t *testing.T) {
	f := newFixture(t, "darwin")
	workspace := f.mkdir(f.path("workspace"))
	nested := f.mkdir(filepath.Join(workspace, "nested.repo"))
	f.mkdir(filepath.Join(nested, ".git"))
	f.write(filepath.Join(workspace, ".mcp.json"), `{"mcpServers":{"myserver":{"url":"https://workspace.example.com/sse"}}}`)
	f.write(filepath.Join(nested, ".mcp.json"), `{"mcpServers":{"myserver":{"url":"https://nested.example.com/sse"}}}`)
	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(workspace)+`:{"mcpServers":{"local":{"url":"https://workspace-local.example.com/sse"}}},
		`+quote(nested)+`:{"mcpServers":{"local":{"url":"https://nested-local.example.com/sse"}}}
	}}`)
	transcript := f.homePath(".claude", "projects", claudeProjectSlug(workspace), "0b0e.jsonl")

	req := claudeCodeReq("myserver", nested)
	assertURL(t, Resolve(f.Env, req), "https://nested.example.com/sse") // no transcript: cwd stands in
	req.TranscriptPath = transcript
	assertURL(t, Resolve(f.Env, req), "https://workspace.example.com/sse")

	req.ServerName = "local"
	assertURL(t, Resolve(f.Env, req), "https://workspace-local.example.com/sse")

	// A subagent's transcript sits below its session's directory.
	req.TranscriptPath = f.homePath(".claude", "projects", claudeProjectSlug(workspace), "0b0e", "subagents", "agent-1.jsonl")
	assertURL(t, Resolve(f.Env, req), "https://workspace-local.example.com/sse")

	// A transcript naming no ancestor of cwd is ignored rather than trusted.
	req.TranscriptPath = f.homePath(".claude", "projects", "-somewhere-else", "0b0e.jsonl")
	assertURL(t, Resolve(f.Env, req), "https://nested-local.example.com/sse")
}

// TestClaudeProjectSlug pins the transcript directory name against values
// computed by Claude Code's own JavaScript, including the truncation hash and a
// character outside the BMP, which counts as two code units.
func TestClaudeProjectSlug(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/Users/dev/my.project", "-Users-dev-my-project"},
		{`C:\Users\Dev\Proj`, "C--Users-Dev-Proj"},
		{"/Users/dev/" + strings.Repeat("a", 230),
			"-Users-dev-" + strings.Repeat("a", 189) + "-4qs2i8"},
		{"/Users/dev/" + strings.Repeat("b", 195) + "/\u00e9\U0001F642x",
			"-Users-dev-" + strings.Repeat("b", 189) + "-6b4uvt"},
	} {
		if got := claudeProjectSlug(tc.path); got != tc.want {
			t.Errorf("claudeProjectSlug(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestClaudeCodeNonGitProject covers a project tree that is not a checkout.
func TestClaudeCodeNonGitProject(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("loose"))
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{"myserver":{"url":"https://loose.example.com/sse"}}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", filepath.Join(project, "sub"))), "https://loose.example.com/sse")
}

// TestClaudeCodeMalformedFilesAreSkipped covers the tolerance rule: a file that
// cannot be parsed is skipped rather than fatal, and resolution continues.
func TestClaudeCodeMalformedFilesAreSkipped(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers": {`)
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"myserver":{"url":"https://global.example.com/sse"}}}`)

	res := Resolve(f.Env, claudeCodeReq("myserver", project))
	assertURL(t, res, "https://global.example.com/sse")
	i := slices.Index(consultedPaths(res), filepath.Join(project, ".mcp.json"))
	if i < 0 || res.Trace[i].Note != "unreadable or malformed" {
		t.Fatalf("expected the malformed file to be recorded as such:\n%s", resolveTrace(res))
	}
}

// TestClaudeCodeJSONCTolerance covers a config carrying comments and a trailing
// comma. Refusing to parse one would turn a working server into a deny.
func TestClaudeCodeJSONCTolerance(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	f.write(filepath.Join(project, ".mcp.json"), `{
		// the issue tracker
		"mcpServers": {
			"myserver": {"url": "https://myserver.example.com/sse"},
		}
	}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "https://myserver.example.com/sse")
}

// TestClaudeCodeStdioResolution covers an entry that launches a package runner,
// including the executable being reported alongside an unresolved package.
func TestClaudeCodeStdioResolution(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")

	f.write(filepath.Join(project, ".mcp.json"),
		`{"mcpServers":{"myserver":{"command":"npx","args":["-y","myserver-mcp@1.2.3","--token","secret"]}}}`)
	res := Resolve(f.Env, claudeCodeReq("myserver", project))
	assertPackage(t, res, "npm", "myserver-mcp", "1.2.3")
	if res.Identity.Command != "npx" {
		t.Fatalf("Command = %q, want the bare executable", res.Identity.Command)
	}

	f.write(filepath.Join(project, ".mcp.json"),
		`{"mcpServers":{"myserver":{"command":"node","args":["/srv/server.js","--token","secret"]}}}`)
	res = Resolve(f.Env, claudeCodeReq("myserver", project))
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
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{"g":{"url":"https://gitmcp.io/obot/x"}}}`)

	res := Resolve(f.Env, claudeCodeReq("g", project))
	if res.Identity.Hostname != "" {
		t.Fatalf("Hostname = %q, want it left for the backend to derive", res.Identity.Hostname)
	}
}

// quote renders s as a JSON string, so a Windows path's separators survive into
// the fixture config.
func quote(s string) string {
	return string(mustJSON(s))
}

// TestClaudeAIConnectorMatch covers claude.ai account connectors. They have no
// local URL and no local command, so the display name recorded in ~/.claude.json
// is the entire local evidence — the device attests which connector was targeted
// and Obot decides whether it is permitted.
func TestClaudeAIConnectorMatch(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	f.write(f.homePath(".claude.json"),
		`{"claudeAiMcpEverConnected":["claude.ai MyServer","claude.ai Notion 2"]}`)

	res := Resolve(f.Env, claudeCodeReq("claude_ai_MyServer", project))
	if res.Unresolved || res.Identity.Connector != "claude.ai MyServer" {
		t.Fatalf("Connector = %q (unresolved=%v), want %q\n%s",
			res.Identity.Connector, res.Unresolved, "claude.ai MyServer", resolveTrace(res))
	}
	// The reported name is the display name, not the namespace form that found it:
	// a connector matched an entry, so the same rule as every other agent applies.
	// claude_ai_MyServer appears in no configuration and in no allowlist entry, so an
	// administrator copying it out of a decision-log row could never make it match.
	if res.ServerName != "claude.ai MyServer" {
		t.Errorf("ServerName = %q, want the connector display name", res.ServerName)
	}

	res = Resolve(f.Env, claudeCodeReq("claude_ai_Notion_2", project))
	if res.Identity.Connector != "claude.ai Notion 2" || res.ServerName != "claude.ai Notion 2" {
		t.Fatalf("Connector = %q, ServerName = %q, want %q",
			res.Identity.Connector, res.ServerName, "claude.ai Notion 2")
	}

	// An unlisted connector is a genuine "we do not know what this is".
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq("claude_ai_Unknown", project)),
		"was not found in any Claude Code MCP configuration")
}

func TestClaudeAIConnectorDuplicateDisplayName(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	f.write(f.homePath(".claude.json"),
		`{"claudeAiMcpEverConnected":["claude.ai MyServer","claude.ai MyServer (2)"]}`)

	res := Resolve(f.Env, claudeCodeReq("claude_ai_MyServer_2", project))
	if res.Unresolved {
		t.Fatalf("the renamed connector did not resolve\n%s", resolveTrace(res))
	}
	if res.Identity.Connector != "claude.ai MyServer (2)" {
		t.Fatalf("Connector = %q, want %q — resolving to the un-suffixed connector "+
			"attributes the call to the wrong upstream",
			res.Identity.Connector, "claude.ai MyServer (2)")
	}
	// And the first one is still reachable by its own name.
	res = Resolve(f.Env, claudeCodeReq("claude_ai_MyServer", project))
	if res.Identity.Connector != "claude.ai MyServer" {
		t.Fatalf("Connector = %q, want %q", res.Identity.Connector, "claude.ai MyServer")
	}
}

// TestClaudeAIConnectorPrefixIsLoadBearing encodes both sides of the one mistake
// that breaks every connector match. The claude_ai_ prefix in a reported name IS
// the "claude.ai " in the stored display name, folded — so stripping it, which
// looks like an obvious cleanup, makes the comparison fail.
func TestClaudeAIConnectorPrefixIsLoadBearing(t *testing.T) {
	const (
		hint       = "claude_ai_MyServer"
		storedName = "claude.ai MyServer"
	)
	if got := formClaudeCode(storedName); got != hint {
		t.Fatalf("formClaudeCode(%q) = %q, want %q", storedName, got, hint)
	}
	stripped := strings.TrimPrefix(hint, claudeAIConnectorPrefix)
	if formClaudeCode(storedName) == stripped {
		t.Fatalf("%q must NOT match %q; the prefix is load-bearing", stripped, storedName)
	}
}

// TestClaudeAIConnectorOnlyForItsNamespace covers that a non-connector server name
// never reaches the connector list.
func TestClaudeAIConnectorOnlyForItsNamespace(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	f.write(f.homePath(".claude.json"), `{"claudeAiMcpEverConnected":["myserver"]}`)

	res := Resolve(f.Env, claudeCodeReq("myserver", project))
	assertUnresolved(t, res, "was not found in any Claude Code MCP configuration")
	if res.Identity.Connector != "" {
		t.Fatalf("Connector = %q, want empty for a non-connector namespace", res.Identity.Connector)
	}
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

// projectScopesFrom runs the project walk against a fixture's ~/.claude.json for a
// session launched in cwd.
func projectScopesFrom(t *testing.T, f *fixture, cwd string) []scope {
	t.Helper()
	claudePath := f.homePath(".claude.json")
	var claude claudeJSON
	res := loadJSON(claudePath, &claude)
	loader := newConfigLoader()
	project := claudeProjectFor(t.Context(), loader, f.Env, cwd, "")
	return claudeProjectScopes(loader, f.Env, project, claudePath, claude, res, 1)
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

// TestAncestorsIsBounded keeps a pathological path from turning into a scope per
// level.
func TestAncestorsIsBounded(t *testing.T) {
	deep := hostRoot(t) + strings.Repeat("a"+string(filepath.Separator), maxProjectDepth*2)
	if got := ancestors(deep); len(got) != maxProjectDepth {
		t.Fatalf("ancestors returned %d entries, want %d", len(got), maxProjectDepth)
	}
}

// TestProjectScopesOrder pins the ordering contract: the local section first,
// then every project file from the launch directory up, with consecutive ranks.
func TestProjectScopesOrder(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	cwd := f.mkdir(filepath.Join(project, "a", "b"))
	f.write(filepath.Join(cwd, ".mcp.json"), `{"mcpServers":{}}`)
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{}}`)
	f.write(f.homePath(".claude.json"), projectKeysJSON(project, cwd))

	scopes := projectScopesFrom(t, f, cwd)
	// The projects{} key is quoted the way the trace renders it, which is the way
	// the file spells it: a Windows path's backslashes come back escaped.
	want := []string{
		f.homePath(".claude.json") + fmt.Sprintf(`  projects[%q].mcpServers`, cwd),
		filepath.Join(cwd, ".mcp.json") + "  mcpServers",
		filepath.Join(project, ".mcp.json") + "  mcpServers",
	}
	if got := scopeKeys(scopes); !slices.Equal(got, want) {
		t.Fatalf("scopes\n%v\nwant\n%v", got, want)
	}
	for i, s := range scopes {
		if s.rank != i+1 {
			t.Fatalf("scope %d has rank %d, want %d: ranks must be consecutive from the one given", i, s.rank, i+1)
		}
	}
}

// TestProjectScopesNamesTheAbsentProjectFile covers the diagnostic: with no
// project file anywhere, the path an operator expects to be read is still named,
// so a trace says it is absent rather than omitting it.
func TestProjectScopesNamesTheAbsentProjectFile(t *testing.T) {
	f := newFixture(t, "darwin")
	cwd := f.mkdir(f.path("proj"))

	got := scopeKeys(projectScopesFrom(t, f, cwd))
	want := []string{filepath.Join(cwd, ".mcp.json") + "  mcpServers"}
	if !slices.Equal(got, want) {
		t.Fatalf("scopes\n%v\nwant\n%v", got, want)
	}
}

// TestProjectScopesNeverReadTheRootProjectFile covers a session launched at the
// filesystem root. Claude Code reads no .mcp.json there, so the absent-file
// placeholder must not name one: as a scope it would be loaded.
func TestProjectScopesNeverReadTheRootProjectFile(t *testing.T) {
	f := newFixture(t, "darwin")
	root := hostRoot(t)
	project := claudeProject{launchDir: root, root: root}
	for _, s := range claudeProjectScopes(newConfigLoader(), f.Env, project, f.homePath(".claude.json"), claudeJSON{}, loadAbsent, 1) {
		if filepath.Base(s.path) == ".mcp.json" {
			t.Fatalf("root launch produced a project file scope: %s", s.path)
		}
	}
}

// TestProjectScopesIgnoresUnrelatedProjectKeys covers the filter: a configured
// project that is not this session's governs nothing here.
func TestProjectScopesIgnoresUnrelatedProjectKeys(t *testing.T) {
	f := newFixture(t, "darwin")
	cwd := f.mkdir(f.path("here"))
	f.write(f.homePath(".claude.json"), projectKeysJSON(f.path("elsewhere"), f.path("here-too"), f.Root))

	for _, s := range projectScopesFrom(t, f, cwd) {
		if strings.Contains(s.key, "projects[") {
			t.Fatalf("consulted an unrelated project: %s", s.key)
		}
	}
}

// TestProjectScopesDirectoryIsNotAProjectFile covers a .mcp.json that is a
// directory: it cannot be read, so it is not a scope, and the real one further up
// still is.
func TestProjectScopesDirectoryIsNotAProjectFile(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	cwd := f.mkdir(filepath.Join(project, "sub"))
	f.mkdir(filepath.Join(cwd, ".mcp.json"))
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{}}`)

	got := scopeKeys(projectScopesFrom(t, f, cwd))
	want := []string{filepath.Join(project, ".mcp.json") + "  mcpServers"}
	if !slices.Equal(got, want) {
		t.Fatalf("scopes\n%v\nwant\n%v", got, want)
	}
}

// TestProjectScopesStableAcrossSpellings covers two projects{} keys naming one
// directory. Map order is not stable, so the key reported has to be chosen rather
// than whichever came out first.
func TestProjectScopesStableAcrossSpellings(t *testing.T) {
	f := newFixture(t, "darwin")
	cwd := f.mkdir(f.path("proj"))
	f.write(f.homePath(".claude.json"), projectKeysJSON(cwd, cwd+string(filepath.Separator)))

	first := projectScopesFrom(t, f, cwd)
	for range 20 {
		if got := scopeKeys(projectScopesFrom(t, f, cwd)); !slices.Equal(got, scopeKeys(first)) {
			t.Fatalf("scopes vary across runs:\n%v\n%v", got, scopeKeys(first))
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
	if got := darwin.comparableDir("  " + mixed + string(filepath.Separator) + "  "); got != mixed {
		t.Fatalf("comparableDir = %q, want it trimmed and cleaned", got)
	}
	if got := darwin.comparableDir("   "); got != "" {
		t.Fatalf("comparableDir = %q, want empty", got)
	}
}

// TestClaudeCodeBuiltinServerIsReportedByName covers the one agent whose built-in
// MCP servers are matched by name, reached through its own resolver.
func TestClaudeCodeBuiltinServerIsReportedByName(t *testing.T) {
	f := newFixture(t, "darwin")

	res := Resolve(f.Env, claudeCodeReq("Claude_Preview", f.path("proj")))
	if res.Unresolved {
		t.Fatalf("built-in server reported as unresolved (%s)\n%s", res.Reason, resolveTrace(res))
	}
	if res.ServerName != "Claude_Preview" {
		t.Fatalf("ServerName = %q, want the name as it arrived", res.ServerName)
	}
	if res.Identity != emptyIdentity {
		t.Fatalf("Identity = %+v, want empty", res.Identity)
	}

	// A configured server of the same name still wins, because it has a real
	// identity to report. Claude Code will not let this happen — that guarantee is
	// what makes name matching sound here — but the ordering is what makes the
	// built-in check safe at its single call site.
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"Claude_Preview":{"url":"https://user.example.com/sse"}}}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq("Claude_Preview", f.path("proj"))), "https://user.example.com/sse")
}
