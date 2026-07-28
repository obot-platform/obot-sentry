package enforce

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
	"github.com/obot-platform/obot-sentry/pkg/toolkind"
	"github.com/obot-platform/obot/apiclient/types"
)

// loadPayload reads a captured hook payload from testdata.
func loadPayload(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "payloads", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestNormalizeGolden covers the captured payloads per (agent, event), against a
// fixture machine holding the MCP configuration those payloads were captured
// against.
func TestNormalizeGolden(t *testing.T) {
	cases := []struct {
		name    string
		agent   localagent.Agent
		event   Event
		payload string
		want    types.EnforcementDecisionRequest
	}{
		{
			name:    "claude code mcp call",
			agent:   localagent.ClaudeCode,
			event:   EventPreToolUse,
			payload: "claude-code-pretooluse-mcp.json",
			want: types.EnforcementDecisionRequest{
				Agent:      "claude_code",
				Tool:       "echo",
				Kind:       "mcp",
				ServerName: "probe-npx-stdio",
				Server: types.EnforcementDecisionServer{
					Package: &types.AllowlistServerPackage{Source: "npm", Name: "@modelcontextprotocol/server-everything"},
					Command: "npx",
				},
			},
		},
		{
			name:    "claude code built-in tool",
			agent:   localagent.ClaudeCode,
			event:   EventPreToolUse,
			payload: "claude-code-pretooluse-read.json",
			want: types.EnforcementDecisionRequest{
				Agent: "claude_code",
				Tool:  "Read",
				Kind:  "read",
			},
		},
		{
			// Codex folds punctuation in the server segment: the tool name is
			// mcp__probe_npx_stdio__echo against a config key of probe-npx-stdio. The
			// reported name is the key, which is the one an administrator can copy.
			name:    "codex mcp call with an underscored server hint",
			agent:   localagent.Codex,
			event:   EventPreToolUse,
			payload: "codex-pretooluse-mcp.json",
			want: types.EnforcementDecisionRequest{
				Agent:      "codex",
				Tool:       "echo",
				Kind:       "mcp",
				ServerName: "probe-npx-stdio",
				Server: types.EnforcementDecisionServer{
					Package: &types.AllowlistServerPackage{Source: "npm", Name: "@modelcontextprotocol/server-everything"},
					Command: "npx",
				},
			},
		},
		{
			name:    "codex built-in tool",
			agent:   localagent.Codex,
			event:   EventPreToolUse,
			payload: "codex-pretooluse-bash.json",
			want: types.EnforcementDecisionRequest{
				Agent: "codex",
				Tool:  "Bash",
				Kind:  "shell",
			},
		},
		{
			name:    "cursor mcp call by display name",
			agent:   localagent.Cursor,
			event:   EventCursorBeforeMCPExecution,
			payload: "cursor-beforemcpexecution.json",
			want: types.EnforcementDecisionRequest{
				Agent:      "cursor",
				Tool:       "echo",
				Kind:       "mcp",
				ServerName: "probe-npx-stdio",
				Server: types.EnforcementDecisionServer{
					Package: &types.AllowlistServerPackage{Source: "npm", Name: "@modelcontextprotocol/server-everything"},
					Command: "npx",
				},
			},
		},
		{
			name:    "cursor mcp call with a user-prefixed display name",
			agent:   localagent.Cursor,
			event:   EventCursorBeforeMCPExecution,
			payload: "cursor-beforemcpexecution-user-prefix.json",
			want: types.EnforcementDecisionRequest{
				Agent:      "cursor",
				Tool:       "get_current_time",
				Kind:       "mcp",
				ServerName: "probe-uvx-stdio",
				Server: types.EnforcementDecisionServer{
					Package: &types.AllowlistServerPackage{Source: "pypi", Name: "mcp-server-time"},
					Command: "uvx",
				},
			},
		},
		{
			// A Streamable HTTP server. Cursor sends no url even for this — the payload
			// carries a display name and nothing else — so the URL comes out of
			// mcp.json through the ordinary lookup.
			name:    "cursor mcp call to an http server",
			agent:   localagent.Cursor,
			event:   EventCursorBeforeMCPExecution,
			payload: "cursor-beforemcpexecution-http.json",
			want: types.EnforcementDecisionRequest{
				Agent:      "cursor",
				Tool:       "echo",
				Kind:       "mcp",
				ServerName: "probe-http",
				Server:     types.EnforcementDecisionServer{URL: "http://127.0.0.1:3001/mcp"},
			},
		},
		{
			name:    "cursor built-in tool",
			agent:   localagent.Cursor,
			event:   EventCursorPreToolUse,
			payload: "cursor-pretooluse-shell.json",
			want: types.EnforcementDecisionRequest{
				Agent: "cursor",
				Tool:  "Shell",
				Kind:  "shell",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := normalizeFixture(t)
			call, err := normalizeCall(env, tc.agent, tc.event, loadPayload(t, tc.payload))
			if err != nil {
				t.Fatal(err)
			}
			if call.Skip {
				t.Fatal("call was skipped, want a decision request")
			}
			assertRequest(t, call.Request, tc.want)
		})
	}
}

// normalizeFixture builds a machine carrying the MCP configuration the payloads
// were captured against: the three probe servers, in the files each agent reads.
//
// The payloads report the capture machine's absolute cwd and workspace roots, so
// the home-relative sources are redirected here and the project-relative ones
// simply miss — which is also what happened on the capture machine for Claude Code,
// whose projects[<root>].mcpServers was empty.
func normalizeFixture(t *testing.T) Env {
	t.Helper()
	f := newFixture(t, "darwin")
	f.write(f.homePath(".claude.json"), `{"mcpServers":{
		"probe-npx-stdio": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-everything"]},
		"probe-uvx-stdio": {"command": "uvx", "args": ["mcp-server-time"]},
		"probe-http":      {"url": "http://127.0.0.1:3001/mcp"}
	}}`)
	f.write(f.homePath(".codex", "config.toml"), `
[mcp_servers.probe-npx-stdio]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-everything"]

[mcp_servers.probe-uvx-stdio]
command = "uvx"
args = ["mcp-server-time"]

[mcp_servers.probe-http]
url = "http://127.0.0.1:3001/mcp"
`)
	f.write(f.homePath(".cursor", "mcp.json"), `{"mcpServers":{
		"probe-npx-stdio": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-everything"]},
		"probe-uvx-stdio": {"command": "uvx", "args": ["mcp-server-time"]},
		"probe-http":      {"url": "http://127.0.0.1:3001/mcp"}
	}}`)
	return f.Env
}

// TestMCPSplits covers the readings of an mcp__ tool name, shortest server first.
func TestMCPSplits(t *testing.T) {
	cases := []struct {
		rest string
		want []mcpSplit
	}{
		{"linear__search", []mcpSplit{{"linear", "search"}}},
		{"probe_npx_stdio__echo", []mcpSplit{{"probe_npx_stdio", "echo"}}},
		// The server half contains the delimiter, so the name reads two ways.
		{"dot__dot__echo", []mcpSplit{{"dot", "dot__echo"}, {"dot__dot", "echo"}}},
		// A tool half containing "__" adds a reading too.
		{"srv__a__b", []mcpSplit{{"srv", "a__b"}, {"srv__a", "b"}}},
		// Nothing to split.
		{"linear", nil},
		{"", nil},
		// A leading or trailing delimiter yields no non-empty pair.
		{"__echo", nil},
		{"linear__", nil},
	}

	for _, tc := range cases {
		t.Run(tc.rest, func(t *testing.T) {
			got := mcpSplits(tc.rest)
			if len(got) != len(tc.want) {
				t.Fatalf("mcpSplits(%q) = %+v, want %+v", tc.rest, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("mcpSplits(%q)[%d] = %+v, want %+v", tc.rest, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestNormalizeAmbiguousServerSplitIsUnresolved is the regression test for a
// fail-OPEN bypass.
//
// "__" is both the delimiter in mcp__<server>__<tool> and a legal sequence inside a
// namespace, so a server whose namespace contains it makes the name ambiguous. A
// Codex key of "dot..dot" is namespaced "dot__dot", and a call to its echo arrives
// as "mcp__dot__dot__echo" — which reads as either (dot, dot__echo) or (dot__dot,
// echo). Taking the leftmost, as both agents' own parsers do, reports server "dot".
//
// If "dot" is also configured and allowlisted, that is a permitted server reported
// for a call that went elsewhere, and ~/.codex/config.toml is user-writable. So when
// more than one reading names a configured server the answer has to be that we
// cannot tell, exactly as for a name declared in two scopes at once.
func TestNormalizeAmbiguousServerSplitIsUnresolved(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".codex", "config.toml"), `
[mcp_servers.dot]
url = "https://allowlisted.example.com/sse"

[mcp_servers."dot..dot"]
command = "npx"
args = ["-y", "some-package"]
`)

	call, err := normalizeCall(f.Env, localagent.Codex, EventPreToolUse,
		[]byte(`{"tool_name":"mcp__dot__dot__echo","cwd":"/Users/dev/proj"}`))
	if err != nil {
		t.Fatal(err)
	}

	if !call.Request.Unresolved {
		t.Fatalf("resolved to server %q (%+v); an ambiguous split must not silently pick the leftmost reading",
			call.Request.ServerName, call.Request.Server)
	}
	if call.Request.Server.URL != "" {
		t.Errorf("reported URL %q — the allowlisted server must not answer for a call that may have gone elsewhere",
			call.Request.Server.URL)
	}
	for _, want := range []string{"divides into an MCP server and a tool in more than one way", "dot", "dot..dot"} {
		if !strings.Contains(call.Request.UnresolvedReason, want) {
			t.Errorf("reason is missing %q: %s", want, call.Request.UnresolvedReason)
		}
	}
}

// TestNormalizeUnambiguousSplitStillResolves is the other half: the ambiguity check
// must not cost the ordinary case. Only one reading here names a configured server,
// so it wins outright even though the name divides two ways.
func TestNormalizeUnambiguousSplitStillResolves(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".codex", "config.toml"), `
[mcp_servers."dot..dot"]
url = "https://dotted.example.com/sse"
`)

	call, err := normalizeCall(f.Env, localagent.Codex, EventPreToolUse,
		[]byte(`{"tool_name":"mcp__dot__dot__echo","cwd":"/Users/dev/proj"}`))
	if err != nil {
		t.Fatal(err)
	}
	if call.Request.Unresolved {
		t.Fatalf("did not resolve: %s", call.Request.UnresolvedReason)
	}
	if call.Request.ServerName != "dot..dot" {
		t.Errorf("ServerName = %q, want the configuration key %q", call.Request.ServerName, "dot..dot")
	}
	// The tool half comes from the reading that won, not from the leftmost one.
	if call.Request.Tool != "echo" {
		t.Errorf("Tool = %q, want %q from the matching reading", call.Request.Tool, "echo")
	}
	if call.Request.Server.URL != "https://dotted.example.com/sse" {
		t.Errorf("URL = %q, want the dotted server's", call.Request.Server.URL)
	}
}

// TestNormalizeSkipsCursorMCPPreToolUse covers the one path that needs no decision:
// beforeMCPExecution already decided the call. Both events do fire for an MCP call,
// preToolUse first, so this is exercised by real bytes.
func TestNormalizeSkipsCursorMCPPreToolUse(t *testing.T) {
	env := normalizeFixture(t)
	call, err := normalizeCall(env, localagent.Cursor, EventCursorPreToolUse, loadPayload(t, "cursor-pretooluse-mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !call.Skip {
		t.Fatalf("call = %+v, want Skip", call)
	}
	if call.Request != (types.EnforcementDecisionRequest{}) {
		t.Fatalf("a skipped call built a request: %+v", call.Request)
	}
	if len(call.Trace) != 0 {
		t.Fatalf("a skipped call consulted config files: %v", call.Trace)
	}
}

// TestNormalizeNeverCarriesToolInput covers the scope boundary. Tool parameters are
// never inspected, so they can never be logged — and neither can the other payload
// fields we deliberately do not read, above all Cursor's user_email.
func TestNormalizeNeverCarriesToolInput(t *testing.T) {
	env := normalizeFixture(t)
	cases := []struct {
		agent   localagent.Agent
		event   Event
		payload string
	}{
		{localagent.ClaudeCode, EventPreToolUse, "claude-code-pretooluse-mcp.json"},
		{localagent.ClaudeCode, EventPreToolUse, "claude-code-pretooluse-read.json"},
		{localagent.Codex, EventPreToolUse, "codex-pretooluse-mcp.json"},
		{localagent.Codex, EventPreToolUse, "codex-pretooluse-bash.json"},
		{localagent.Cursor, EventCursorBeforeMCPExecution, "cursor-beforemcpexecution.json"},
		{localagent.Cursor, EventCursorBeforeMCPExecution, "cursor-beforemcpexecution-http.json"},
		{localagent.Cursor, EventCursorPreToolUse, "cursor-pretooluse-shell.json"},
	}

	for _, tc := range cases {
		call, err := normalizeCall(env, tc.agent, tc.event, loadPayload(t, tc.payload))
		if err != nil {
			t.Fatalf("%s: %v", tc.payload, err)
		}
		encoded, err := json.Marshal(call.Request)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{
			"tool_input", "toolInput",
			// Values that appear only inside the captured tool_input.
			"hello-stdio-npx", "hello-http", "hello.txt", "sed -n", "mcp-server-time --help", "timezone",
			// Fields we read nothing from.
			"user_email", "@", "transcript", "session", "composer",
		} {
			// The npm scope in a package name is the one legitimate "@".
			if forbidden == "@" && strings.Contains(string(encoded), "@modelcontextprotocol/server-everything") {
				continue
			}
			if strings.Contains(string(encoded), forbidden) {
				t.Errorf("%s request carried %q: %s", tc.payload, forbidden, encoded)
			}
		}
	}
}

// TestNormalizeCursorKindMap covers the closed set of Cursor preToolUse tool names.
func TestNormalizeCursorKindMap(t *testing.T) {
	env := normalizeFixture(t)
	want := map[string]string{
		"Shell":  "shell",
		"Read":   "read",
		"Grep":   "read",
		"Write":  "write",
		"Delete": "write",
		"Task":   "task",
	}
	for toolName, kind := range want {
		raw := []byte(`{"tool_name":` + string(mustJSON(toolName)) + `,"workspace_roots":["/Users/dev/probe-workspace"]}`)
		call, err := normalizeCall(env, localagent.Cursor, EventCursorPreToolUse, raw)
		if err != nil {
			t.Fatalf("%s: %v", toolName, err)
		}
		if call.Request.Kind != kind {
			t.Errorf("%s kind = %q, want %q", toolName, call.Request.Kind, kind)
		}
		if call.Request.ServerName != "" || call.Request.Server != (types.EnforcementDecisionServer{}) {
			t.Errorf("%s carried a server: %+v", toolName, call.Request)
		}
		if call.Request.Unresolved {
			t.Errorf("%s was marked unresolved; a non-MCP call has nothing to resolve", toolName)
		}
	}
}

// TestNormalizeUnresolvedMCPCall covers the label an unidentified target carries. It
// is a normal decision request plus two extra fields; there is no local-deny path.
func TestNormalizeUnresolvedMCPCall(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".claude.json"),
		`{"mcpServers":{"probe-npx-stdio":{"command":"node","args":["/srv/server.js","--token","hunter2"]}}}`)

	call, err := normalizeCall(f.Env, localagent.ClaudeCode, EventPreToolUse, loadPayload(t, "claude-code-pretooluse-mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !call.Request.Unresolved {
		t.Fatalf("request = %+v, want unresolved", call.Request)
	}
	if !strings.Contains(call.Request.UnresolvedReason, `stdio command "node" is not a supported package runner`) {
		t.Errorf("reason = %q, want it to name the runner", call.Request.UnresolvedReason)
	}
	// As much identity as we do have travels with the flag, so the decision-log row
	// names something an administrator can act on.
	if call.Request.ServerName != "probe-npx-stdio" {
		t.Errorf("ServerName = %q, want it populated on an unresolved call", call.Request.ServerName)
	}
	if call.Request.Server.Command != "node" {
		t.Errorf("Command = %q, want the bare executable", call.Request.Server.Command)
	}
	encoded := string(mustJSON(call.Request))
	if strings.Contains(encoded, "hunter2") || strings.Contains(encoded, "/srv/server.js") {
		t.Errorf("arguments crossed the wire: %s", encoded)
	}
}

// TestNormalizeMissingToolNameIsAnError covers a payload we cannot understand at
// all, which the caller turns into a deny.
func TestNormalizeMissingToolNameIsAnError(t *testing.T) {
	env := normalizeFixture(t)
	cases := []struct {
		agent localagent.Agent
		event Event
		raw   string
	}{
		{localagent.ClaudeCode, EventPreToolUse, `{"cwd":"/Users/dev/probe-workspace"}`},
		{localagent.ClaudeCode, EventPreToolUse, `{"tool_name":"   "}`},
		{localagent.ClaudeCode, EventPreToolUse, `not json`},
		{localagent.Cursor, EventCursorPreToolUse, `{}`},
		{localagent.Cursor, EventCursorBeforeMCPExecution, `{"mcp_server_name":"probe-http"}`},
	}
	for _, tc := range cases {
		if _, err := normalizeCall(env, tc.agent, tc.event, []byte(tc.raw)); err == nil {
			t.Errorf("normalizeCall(%s, %s, %s) succeeded, want an error", tc.agent, tc.event, tc.raw)
		}
	}
}

// TestNormalizeStripsBOM covers Cursor on Windows, which prefixes hook stdin with a
// UTF-8 byte-order mark that encoding/json rejects.
func TestNormalizeStripsBOM(t *testing.T) {
	env := normalizeFixture(t)
	raw := append([]byte{0xef, 0xbb, 0xbf}, loadPayload(t, "cursor-pretooluse-shell.json")...)

	call, err := normalizeCall(env, localagent.Cursor, EventCursorPreToolUse, raw)
	if err != nil {
		t.Fatal(err)
	}
	if call.Request.Tool != "Shell" {
		t.Fatalf("Tool = %q, want Shell", call.Request.Tool)
	}
}

// assertRequest compares two decision requests field by field, so a failure names
// what differs.
func assertRequest(t *testing.T, got, want types.EnforcementDecisionRequest) {
	t.Helper()
	if got.Agent != want.Agent || got.Tool != want.Tool || got.Kind != want.Kind || got.ServerName != want.ServerName {
		t.Errorf("agent/tool/kind/server = %q/%q/%q/%q, want %q/%q/%q/%q",
			got.Agent, got.Tool, got.Kind, got.ServerName,
			want.Agent, want.Tool, want.Kind, want.ServerName)
	}
	if got.Unresolved != want.Unresolved || got.UnresolvedReason != want.UnresolvedReason {
		t.Errorf("unresolved = %v (%q), want %v (%q)",
			got.Unresolved, got.UnresolvedReason, want.Unresolved, want.UnresolvedReason)
	}
	if got.Server.URL != want.Server.URL || got.Server.Command != want.Server.Command ||
		got.Server.Hostname != want.Server.Hostname || got.Server.Connector != want.Server.Connector {
		t.Errorf("server = %+v, want %+v", got.Server, want.Server)
	}
	switch {
	case got.Server.Package == nil && want.Server.Package == nil:
	case got.Server.Package == nil || want.Server.Package == nil:
		t.Errorf("package = %+v, want %+v", got.Server.Package, want.Server.Package)
	case *got.Server.Package != *want.Server.Package:
		t.Errorf("package = %+v, want %+v", *got.Server.Package, *want.Server.Package)
	}
}

// TestNormalizeMCPCallWithNoServerHint covers an MCP-shaped tool name that names
// no server: a single-underscore or colon namespace, neither of which the supported
// agents use. It is still an MCP call, so it is reported as unidentified rather than
// classified as a built-in tool, which would gate it behind the wrong toggle.
func TestNormalizeMCPCallWithNoServerHint(t *testing.T) {
	env := normalizeFixture(t)
	for _, toolName := range []string{"mcp_linear_search", "mcp:search_issues"} {
		raw := []byte(`{"tool_name":` + string(mustJSON(toolName)) + `,"cwd":"/Users/dev/probe-workspace"}`)

		call, err := normalizeCall(env, localagent.ClaudeCode, EventPreToolUse, raw)
		if err != nil {
			t.Fatal(err)
		}
		if call.Request.Kind != "mcp" {
			t.Errorf("%s: Kind = %q, want mcp", toolName, call.Request.Kind)
		}
		if !call.Request.Unresolved {
			t.Errorf("%s: request = %+v, want unresolved", toolName, call.Request)
		}
		if !strings.Contains(call.Request.UnresolvedReason, "did not name an MCP server") {
			t.Errorf("%s: reason = %q", toolName, call.Request.UnresolvedReason)
		}
		if len(call.Trace) != 0 {
			t.Errorf("%s: trace = %v, want no config files consulted", toolName, call.Trace)
		}
	}
}

// TestNormalizeMCPCallWithNoToolHalf covers mcp__<server> with nothing after it.
// The whole remainder is the server, so the call resolves rather than being
// reported as unidentified.
func TestNormalizeMCPCallWithNoToolHalf(t *testing.T) {
	env := normalizeFixture(t)
	raw := []byte(`{"tool_name":"mcp__probe-http","cwd":"/Users/dev/probe-workspace"}`)

	call, err := normalizeCall(env, localagent.ClaudeCode, EventPreToolUse, raw)
	if err != nil {
		t.Fatal(err)
	}
	if call.Request.Unresolved {
		t.Fatalf("request = %+v, want it resolved", call.Request)
	}
	if call.Request.ServerName != "probe-http" {
		t.Errorf("ServerName = %q, want %q", call.Request.ServerName, "probe-http")
	}
	// With no tool half there is no tool within the server, so the name as it
	// arrived is reported; a tool-scoped allowlist entry cannot match it.
	if call.Request.Tool != "mcp__probe-http" {
		t.Errorf("Tool = %q, want the name as it arrived", call.Request.Tool)
	}
}

// TestNormalizeUnsupportedAgentIsUnresolved covers the defensive arm of the
// resolver dispatch. ParseAgent gates this in practice, so reaching it would be a
// programming error — and it must still fail closed rather than resolve.
func TestNormalizeUnsupportedAgentIsUnresolved(t *testing.T) {
	f := newFixture(t, "darwin")
	for _, agent := range []localagent.Agent{localagent.VSCode, localagent.Agent("goose")} {
		res := Resolve(f.Env, ResolveRequest{Agent: agent, ServerName: "linear", CWD: f.path("proj")})
		assertUnresolved(t, res, "unsupported agent")
		if len(res.Trace) != 0 {
			t.Errorf("%s: trace = %v, want no config files consulted", agent, res.Trace)
		}
	}
}

// TestClassifyPreTool covers the shared PreToolUse classifier. All three agents
// that fire it namespace MCP tools the same way, so all three yield a server hint —
// this is where enforcement diverges from pkg/audit, which reports no server for
// VS Code.
func TestClassifyPreTool(t *testing.T) {
	cases := []struct {
		toolName string
		kind     string
		tool     string
		server   string
	}{
		{"mcp__linear__search_issues", toolkind.KindMCP, "search_issues", "linear"},
		{"mcp__linear__nested__tool", toolkind.KindMCP, "nested__tool", "linear"},
		// Claude Code's plugin and account-connector namespaces are ordinary server
		// hints here; the resolver, not the classifier, recognizes them.
		{"mcp__plugin_tracker_linear__search", toolkind.KindMCP, "search", "plugin_tracker_linear"},
		{"mcp__claude_ai_Linear__search", toolkind.KindMCP, "search", "claude_ai_Linear"},
		// No tool half: the whole remainder is the server, and the tool reported is the
		// name as it arrived — which no tool-scoped allowlist entry can match, so it
		// fails closed.
		{"mcp__linear", toolkind.KindMCP, "mcp__linear", "linear"},
		// A single-underscore or colon namespace carries no server we can name. Still
		// an MCP call, so the resolver reports it as unidentified.
		{"mcp_linear_search", toolkind.KindMCP, "mcp_linear_search", ""},
		{"MCP:search_issues", toolkind.KindMCP, "MCP:search_issues", ""},

		{"Bash", toolkind.KindShell, "Bash", ""},
		{"run_in_terminal", toolkind.KindShell, "run_in_terminal", ""},
		{"Read", toolkind.KindRead, "Read", ""},
		{"Write", toolkind.KindWrite, "Write", ""},
		{"Task", toolkind.KindTask, "Task", ""},
		{"Glob", toolkind.KindGeneric, "Glob", ""},
	}

	for _, tc := range cases {
		got := classifyPreTool(tc.toolName)
		if got.Kind != tc.kind || got.Tool != tc.tool || got.ServerName != tc.server {
			t.Errorf("classifyPreTool(%q) = {%s %q %q}, want {%s %q %q}",
				tc.toolName, got.Kind, got.Tool, got.ServerName, tc.kind, tc.tool, tc.server)
		}
		if got.Skip {
			t.Errorf("classifyPreTool(%q) set Skip; only Cursor's preToolUse skips", tc.toolName)
		}
	}
}

// TestClassifyCursorPreTool covers Cursor's exact map over its documented closed
// set. It changes no verdict — all five non-MCP kinds are gated identically — but
// it makes the decision log's kind column exact instead of best-effort.
func TestClassifyCursorPreTool(t *testing.T) {
	cases := map[string]string{
		"Shell":  toolkind.KindShell,
		"Read":   toolkind.KindRead,
		"Grep":   toolkind.KindRead,
		"Write":  toolkind.KindWrite,
		"Delete": toolkind.KindWrite,
		"Task":   toolkind.KindTask,
	}
	for toolName, kind := range cases {
		got := classifyCursorPreTool(toolName)
		if got.Kind != kind || got.Tool != toolName || got.Skip {
			t.Errorf("classifyCursorPreTool(%q) = %+v, want kind %s", toolName, got, kind)
		}
	}

	// Grep and Delete are the two the substring heuristics get wrong, which is why
	// the exact map exists.
	if toolkind.Kind("Grep") == toolkind.KindRead {
		t.Error("Grep now classifies as read by heuristic; the exact map may be redundant")
	}
	if toolkind.Kind("Delete") == toolkind.KindWrite {
		t.Error("Delete now classifies as write by heuristic; the exact map may be redundant")
	}

	// A tool Cursor adds later still gets classified rather than dropped.
	if got := classifyCursorPreTool("SomethingNewShell"); got.Kind != toolkind.KindShell {
		t.Errorf("unknown Cursor tool = %+v, want the heuristic fallback", got)
	}
}

// TestClassifyCursorPreToolSkipsMCP covers the skip: beforeMCPExecution already
// decided that call, so deciding again would double-log, and this event's tool name
// carries no server hint to decide on.
func TestClassifyCursorPreToolSkipsMCP(t *testing.T) {
	got := classifyCursorPreTool("MCP:search_issues")
	if !got.Skip {
		t.Fatalf("classifyCursorPreTool(MCP:…) = %+v, want Skip", got)
	}
	if got.Tool != "search_issues" {
		t.Fatalf("Tool = %q, want the bare tool name", got.Tool)
	}
}

// TestClassifyCursorMCP covers beforeMCPExecution, whose tool name is the bare tool
// and whose server comes from the payload's display name.
func TestClassifyCursorMCP(t *testing.T) {
	got := classifyCursorMCP("search_issues", "linear")
	if got.Kind != toolkind.KindMCP || got.Tool != "search_issues" || got.ServerName != "linear" {
		t.Fatalf("classifyCursorMCP = %+v", got)
	}
}
