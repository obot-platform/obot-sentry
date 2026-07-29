package enforce

import (
	"fmt"
	"path/filepath"
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
		f.machinePath(claudeManagedMCPDarwin),
		filepath.Join(project, ".mcp.json"),
		f.homePath(".claude.json"),
	}
	if got := consultedPaths(res); !slices.Equal(got, want) {
		t.Fatalf("consulted\n%v\nwant\n%v\n%s", got, want, resolveTrace(res))
	}

	// Once it exists, the per-root project section is consulted between the
	// project .mcp.json and the global table.
	f.write(f.homePath(".claude.json"), `{"projects":{`+quote(project)+`:{"mcpServers":{}}}}`)
	res = Resolve(f.Env, claudeCodeReq("myserver", project))
	want = []string{
		f.machinePath(claudeManagedMCPDarwin),
		filepath.Join(project, ".mcp.json"),
		f.homePath(".claude.json"), // projects[<project>]
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
	f.write(f.machinePath(claudeManagedMCPDarwin), `{"mcpServers":{"github":{"url":"https://managed.example.com/sse"}}}`)
	res := Resolve(f.Env, claudeCodeReq("myserver", project))
	assertUnresolved(t, res, "managed MCP configuration, which cannot be overridden")
	if len(res.Trace) != 1 {
		t.Fatalf("resolution continued past the managed config:\n%s", resolveTrace(res))
	}

	// A managed file that does list it wins.
	f.write(f.machinePath(claudeManagedMCPDarwin), `{"mcpServers":{"myserver":{"url":"https://managed.example.com/sse"}}}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", project)), "https://managed.example.com/sse")
}

func TestClaudeCodeManagedConfigWindows(t *testing.T) {
	f := newFixture(t, "windows")
	programFiles := f.mkdir(f.path("ProgramFiles"))
	f.setenv("PROGRAMFILES", programFiles)
	f.write(filepath.Join(programFiles, "ClaudeCode", "managed-mcp.json"),
		`{"mcpServers":{"myserver":{"url":"https://managed.example.com/sse"}}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", f.path("proj"))), "https://managed.example.com/sse")
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

// TestClaudeCodeDepthOrder is the load-bearing ordering test: the deepest
// declaration wins, and within one directory the project file outranks that
// directory's projects{} entry.
func TestClaudeCodeDepthOrder(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	cwd := f.mkdir(filepath.Join(project, "a", "b"))

	f.write(filepath.Join(cwd, ".mcp.json"), `{"mcpServers":{"myserver":{"url":"https://cwd-file.example.com/sse"}}}`)
	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(cwd)+`:{"mcpServers":{"myserver":{"url":"https://cwd-projects.example.com/sse"}}},
		`+quote(project)+`:{"mcpServers":{"myserver":{"url":"https://project-projects.example.com/sse"}}}
	}}`)

	// cwd's own project file is the most specific source there is.
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", cwd)), "https://cwd-file.example.com/sse")

	// With it silent, cwd's projects{} entry answers ahead of the shallower one.
	f.write(filepath.Join(cwd, ".mcp.json"), `{"mcpServers":{}}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", cwd)), "https://cwd-projects.example.com/sse")

	// And with that gone too, the enclosing project answers.
	f.write(f.homePath(".claude.json"), `{"projects":{
		`+quote(project)+`:{"mcpServers":{"myserver":{"url":"https://project-projects.example.com/sse"}}}
	}}`)
	assertURL(t, Resolve(f.Env, claudeCodeReq("myserver", cwd)), "https://project-projects.example.com/sse")
}

// TestClaudeCodeProjectFileAboveCWD covers a call made from a subdirectory of the
// project: the project file is above cwd, and it is still the live one.
func TestClaudeCodeProjectFileAboveCWD(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	cwd := f.mkdir(filepath.Join(project, "a", "b"))
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{"myserver":{"url":"https://project.example.com/sse"}}}`)

	res := Resolve(f.Env, claudeCodeReq("myserver", cwd))
	assertURL(t, res, "https://project.example.com/sse")

	// Only the nearest project file is a scope, and only one is ever live for a
	// session, so no other .mcp.json is consulted.
	var files int
	for _, step := range res.Trace {
		if filepath.Base(step.Path) == ".mcp.json" {
			files++
		}
	}
	if files != 1 {
		t.Fatalf("consulted %d project files, want 1\n%s", files, resolveTrace(res))
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
	if res.Trace[1].Note != "unreadable or malformed" {
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

// claudeProjectScopes runs the project walk against a fixture's ~/.claude.json.
func claudeProjectScopes(t *testing.T, f *fixture, cwd string) []scope {
	t.Helper()
	claudePath := f.homePath(".claude.json")
	var claude claudeJSON
	res := loadJSON(claudePath, &claude)
	return projectScopes(f.Env, cwd, claudePath, claude, res, 1)
}

func TestAncestorsNearestFirst(t *testing.T) {
	got := ancestors(filepath.Join("/", "a", "b", "c"))
	want := []string{
		filepath.Join("/", "a", "b", "c"),
		filepath.Join("/", "a", "b"),
		filepath.Join("/", "a"),
		string(filepath.Separator),
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

// TestAncestorsIsBounded keeps a pathological path from turning into a scope per
// level.
func TestAncestorsIsBounded(t *testing.T) {
	deep := string(filepath.Separator) + strings.Repeat("a"+string(filepath.Separator), maxProjectDepth*2)
	if got := ancestors(deep); len(got) != maxProjectDepth {
		t.Fatalf("ancestors returned %d entries, want %d", len(got), maxProjectDepth)
	}
}

// TestProjectScopesRanksByDepth pins the ordering contract: a deeper directory
// outranks a shallower one, and within one directory the project file outranks
// that directory's projects{} entry.
func TestProjectScopesRanksByDepth(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	cwd := f.mkdir(filepath.Join(project, "a", "b"))
	f.write(filepath.Join(cwd, ".mcp.json"), `{"mcpServers":{}}`)
	f.write(f.homePath(".claude.json"), projectKeysJSON(project, cwd))

	scopes := claudeProjectScopes(t, f, cwd)
	want := []string{
		filepath.Join(cwd, ".mcp.json") + "  mcpServers",
		f.homePath(".claude.json") + `  projects["` + cwd + `"].mcpServers`,
		f.homePath(".claude.json") + `  projects["` + project + `"].mcpServers`,
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

// TestProjectScopesTakesOnlyTheNearestProjectFile covers the rule that keeps a
// stale .mcp.json higher up from speaking for a project: one project root is live
// per session, so one project file is a scope.
func TestProjectScopesTakesOnlyTheNearestProjectFile(t *testing.T) {
	f := newFixture(t, "darwin")
	outer := f.mkdir(f.path("outer"))
	inner := f.mkdir(filepath.Join(outer, "inner"))
	f.write(filepath.Join(outer, ".mcp.json"), `{"mcpServers":{}}`)
	f.write(filepath.Join(inner, ".mcp.json"), `{"mcpServers":{}}`)

	got := scopeKeys(claudeProjectScopes(t, f, inner))
	want := []string{filepath.Join(inner, ".mcp.json") + "  mcpServers"}
	if !slices.Equal(got, want) {
		t.Fatalf("scopes\n%v\nwant\n%v", got, want)
	}
}

// TestProjectScopesFindsTheProjectFileAboveCWD covers a call from a subdirectory:
// the project file is not in cwd, and it is still the live one.
func TestProjectScopesFindsTheProjectFileAboveCWD(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{}}`)

	got := scopeKeys(claudeProjectScopes(t, f, f.mkdir(filepath.Join(project, "a", "b"))))
	want := []string{filepath.Join(project, ".mcp.json") + "  mcpServers"}
	if !slices.Equal(got, want) {
		t.Fatalf("scopes\n%v\nwant\n%v", got, want)
	}
}

// TestProjectScopesNamesTheAbsentProjectFile covers the diagnostic: with no
// project file anywhere, the path an operator expects to be read is still named,
// so a trace says it is absent rather than omitting it.
func TestProjectScopesNamesTheAbsentProjectFile(t *testing.T) {
	f := newFixture(t, "darwin")
	cwd := f.mkdir(f.path("proj"))

	got := scopeKeys(claudeProjectScopes(t, f, cwd))
	want := []string{filepath.Join(cwd, ".mcp.json") + "  mcpServers"}
	if !slices.Equal(got, want) {
		t.Fatalf("scopes\n%v\nwant\n%v", got, want)
	}
}

// TestProjectScopesIgnoresUnrelatedProjectKeys covers the filter: a configured
// project that is not on cwd's path governs nothing here.
func TestProjectScopesIgnoresUnrelatedProjectKeys(t *testing.T) {
	f := newFixture(t, "darwin")
	cwd := f.mkdir(f.path("here"))
	f.write(f.homePath(".claude.json"), projectKeysJSON(f.path("elsewhere"), f.path("here-too")))

	for _, s := range claudeProjectScopes(t, f, cwd) {
		if strings.Contains(s.key, "projects[") {
			t.Fatalf("consulted an unrelated project: %s", s.key)
		}
	}
}

// TestProjectScopesDirectoryIsNotAProjectFile covers a .mcp.json that is a
// directory: it cannot be read, so it must not displace the search for a real one
// further up.
func TestProjectScopesDirectoryIsNotAProjectFile(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	cwd := f.mkdir(filepath.Join(project, "sub"))
	f.mkdir(filepath.Join(cwd, ".mcp.json"))
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{}}`)

	got := scopeKeys(claudeProjectScopes(t, f, cwd))
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

	first := claudeProjectScopes(t, f, cwd)
	for range 20 {
		if got := scopeKeys(claudeProjectScopes(t, f, cwd)); !slices.Equal(got, scopeKeys(first)) {
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

	darwin := Env{GOOS: "darwin"}
	if got := darwin.comparableDir("/Users/Dev/Proj"); got != "/Users/Dev/Proj" {
		t.Fatalf("comparableDir = %q, want the path unchanged", got)
	}
	if got := darwin.comparableDir("  /Users/Dev/Proj/  "); got != "/Users/Dev/Proj" {
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
