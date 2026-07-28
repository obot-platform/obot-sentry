package enforce

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

// cursorReq builds a beforeMCPExecution resolution request. There is no cwd: Cursor
// sends none that is usable, and workspace_roots is the project context.
func cursorReq(displayName string, workspaceRoots ...string) ResolveRequest {
	return ResolveRequest{
		Agent:          localagent.Cursor,
		ServerName:     displayName,
		WorkspaceRoots: workspaceRoots,
	}
}

// TestCursorFileOrder pins the config order: each open workspace root, then the
// user-level file. There is no cwd rung.
func TestCursorFileOrder(t *testing.T) {
	f := newFixture(t, "darwin")
	wsA := f.mkdir(f.path("wsA"))
	wsB := f.mkdir(f.path("wsB"))

	res := Resolve(f.Env, cursorReq("linear", wsA, wsB))
	assertUnresolved(t, res, `MCP server "linear" was not found in any Cursor MCP configuration`)

	want := []string{
		filepath.Join(wsA, ".cursor", "mcp.json"),
		filepath.Join(wsB, ".cursor", "mcp.json"),
		f.homePath(".cursor", "mcp.json"),
	}
	if got := consultedPaths(res); !slices.Equal(got, want) {
		t.Fatalf("consulted\n%v\nwant\n%v", got, want)
	}
}

func TestCursorHTTPServerResolvesThroughTheLookup(t *testing.T) {
	f := newFixture(t, "darwin")
	ws := f.mkdir(f.path("ws"))
	f.write(filepath.Join(ws, ".cursor", "mcp.json"), `{"mcpServers":{"probe-http":{"url":"http://127.0.0.1:3001/mcp"}}}`)

	assertURL(t, Resolve(f.Env, cursorReq("probe-http", ws)), "http://127.0.0.1:3001/mcp")
}

// TestCursorLookupNameLadder covers the two display-name forms, in order.
func TestCursorLookupNameLadder(t *testing.T) {
	cases := []struct {
		name        string
		displayName string
		configKey   string
	}{
		{"raw display name", "linear", "linear"},
		{"user- prefix stripped", "user-linear", "linear"},
		{"mixed case verbatim", "My-Linear", "My-Linear"},
		{"dotted name verbatim", "dot.dot", "dot.dot"},
		{"name with a space verbatim", "space name", "space name"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "darwin")
			f.write(f.homePath(".cursor", "mcp.json"),
				`{"mcpServers":{`+quote(tc.configKey)+`:{"url":"https://linear.example.com/sse"}}}`)

			res := Resolve(f.Env, cursorReq(tc.displayName))
			assertURL(t, res, "https://linear.example.com/sse")
			// Whatever rung matched, the reported name is the key the user wrote.
			if res.ServerName != tc.configKey {
				t.Errorf("ServerName = %q, want the matched config key %q", res.ServerName, tc.configKey)
			}
		})
	}
}

func TestCursorPrefersTheUnprefixedNameOverAPrefixLookalike(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".cursor", "mcp.json"), `{"mcpServers":{
		"user-linear": {"url": "https://literal.example.com/sse"},
		"linear":      {"url": "https://stripped.example.com/sse"}
	}}`)

	assertURL(t, Resolve(f.Env, cursorReq("user-linear")), "https://literal.example.com/sse")
}

func TestCursorUndecodableEntryStillCountsAsADeclaration(t *testing.T) {
	f := newFixture(t, "darwin")
	workspace := f.mkdir(f.path("ws"))
	// The same key in both scopes; the project one has a string value rather than an
	// object, so it decodes to a zero entry.
	f.write(filepath.Join(workspace, ".cursor", "mcp.json"),
		`{"mcpServers":{"my-server":"not-an-object"}}`)
	f.write(f.homePath(".cursor", "mcp.json"),
		`{"mcpServers":{"my-server":{"url":"https://user-scope.example.com/sse"}}}`)

	res := Resolve(f.Env, cursorReq("my-server", workspace))
	assertUnresolved(t, res, "conflicting definitions in more than one Cursor configuration scope")
	if res.Identity.URL != "" {
		t.Errorf("Identity.URL = %q, want nothing: the user-scope entry must not answer for a name the project scope also declares", res.Identity.URL)
	}
}

// TestCursorWorkspaceBeatsUser covers precedence.
func TestCursorWorkspaceBeatsUser(t *testing.T) {
	f := newFixture(t, "darwin")
	ws := f.mkdir(f.path("ws"))
	f.write(filepath.Join(ws, ".cursor", "mcp.json"), `{"mcpServers":{"linear":{"url":"https://workspace.example.com/sse"}}}`)
	f.write(f.homePath(".cursor", "mcp.json"), `{"mcpServers":{"other":{"url":"https://user.example.com/sse"}}}`)

	assertURL(t, Resolve(f.Env, cursorReq("linear", ws)), "https://workspace.example.com/sse")
}

// TestCursorCollidingNameIsUnresolved covers the finding that makes first-match
// resolution unsafe. Two servers sharing a name across scopes run as distinct
// servers with distinct internal identifiers, and Cursor sends byte-identical
// payloads for calls to each — the same mcp_server_name and nothing else that
// distinguishes them. Resolving to whichever entry the search order reaches first
// would report an identity that did not execute, and since ~/.cursor/mcp.json is
// user-writable that is a self-service bypass: shadow an allowlisted project
// server's name in user scope, call the user-scope one, and the allowlisted
// identity is what gets reported.
func TestCursorCollidingNameIsUnresolved(t *testing.T) {
	f := newFixture(t, "darwin")
	ws := f.mkdir(f.path("ws"))
	project := f.write(filepath.Join(ws, ".cursor", "mcp.json"),
		`{"mcpServers":{"probe-uvx-stdio":{"command":"uvx","args":["mcp-server-time"]}}}`)
	user := f.write(f.homePath(".cursor", "mcp.json"),
		`{"mcpServers":{"probe-uvx-stdio":{"command":"npx","args":["-y","some-other-package"]}}}`)

	res := Resolve(f.Env, cursorReq("probe-uvx-stdio", ws))
	assertUnresolved(t, res, "conflicting definitions in more than one Cursor configuration scope")
	if res.ServerName != "probe-uvx-stdio" {
		t.Errorf("ServerName = %q, want the display name", res.ServerName)
	}
	if res.Identity != emptyIdentity {
		t.Errorf("Identity = %+v, want nothing reported: we do not know which server ran", res.Identity)
	}

	// Both matches are recorded, because two FOUND lines are the whole diagnostic
	// for this denial. This is the one place a trace holds more than one match.
	var matched []string
	for _, step := range res.Trace {
		if step.Matched {
			matched = append(matched, step.Path)
		}
	}
	if !slices.Equal(matched, []string{project, user}) {
		t.Errorf("matched steps = %v, want both scopes\n%s", matched, resolveTrace(res))
	}
}

// TestCursorSingleScopeResolves is the other half of the collision rule: a
// name declared once resolves exactly as before, even though every candidate file
// is consulted.
func TestCursorSingleScopeResolves(t *testing.T) {
	f := newFixture(t, "darwin")
	ws := f.mkdir(f.path("ws"))
	f.write(filepath.Join(ws, ".cursor", "mcp.json"),
		`{"mcpServers":{"probe-uvx-stdio":{"command":"uvx","args":["mcp-server-time"]}}}`)
	f.write(f.homePath(".cursor", "mcp.json"),
		`{"mcpServers":{"probe-npx-stdio":{"command":"npx","args":["-y","@modelcontextprotocol/server-everything"]}}}`)

	assertPackage(t, Resolve(f.Env, cursorReq("probe-uvx-stdio", ws)), "pypi", "mcp-server-time", "")
	assertPackage(t, Resolve(f.Env, cursorReq("probe-npx-stdio", ws)), "npm", "@modelcontextprotocol/server-everything", "")
}

// TestCursorReportsTheMatchedConfigKey covers the name reported when the display
// name and the config key differ. The prefixed form was observed in a real payload,
// and an administrator who copied "user-probe-uvx-stdio" into an allowlist entry
// would be naming a server that appears in no configuration file.
func TestCursorReportsTheMatchedConfigKey(t *testing.T) {
	f := newFixture(t, "darwin")
	ws := f.mkdir(f.path("ws"))
	f.write(filepath.Join(ws, ".cursor", "mcp.json"),
		`{"mcpServers":{"probe-uvx-stdio":{"command":"/opt/homebrew/bin/uvx","args":["mcp-server-time"]}}}`)

	// Matched but unresolvable: the entry was found and its command is a path rather
	// than a bare runner. It is still a match, so the key is what names it.
	res := Resolve(f.Env, cursorReq("user-probe-uvx-stdio", ws))
	assertUnresolved(t, res, "is a path, not a bare package runner")
	if res.ServerName != "probe-uvx-stdio" {
		t.Errorf("ServerName = %q, want the matched config key", res.ServerName)
	}
}

// TestCursorDeduplicatesConfigPaths covers two workspace roots naming the same
// directory: the same file is not consulted twice, and so cannot collide with
// itself.
func TestCursorDeduplicatesConfigPaths(t *testing.T) {
	f := newFixture(t, "darwin")
	ws := f.mkdir(f.path("ws"))
	f.write(filepath.Join(ws, ".cursor", "mcp.json"), `{"mcpServers":{"linear":{"url":"https://ws.example.com/sse"}}}`)

	res := Resolve(f.Env, cursorReq("linear", ws, ws))
	assertURL(t, res, "https://ws.example.com/sse")
	want := []string{
		filepath.Join(ws, ".cursor", "mcp.json"),
		f.homePath(".cursor", "mcp.json"),
	}
	if got := consultedPaths(res); !slices.Equal(got, want) {
		t.Fatalf("consulted\n%v\nwant\n%v", got, want)
	}
}
