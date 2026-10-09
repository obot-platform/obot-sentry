package enforce

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
	"github.com/obot-platform/obot-sentry/pkg/toolkind"
	"github.com/obot-platform/obot/apiclient/types"
)

// kiroPayload builds a PreToolUse payload the way Kiro 1.2 does.
func kiroPayload(toolName, cwd string, input any) []byte {
	p := map[string]any{
		"session_id":      "s1",
		"hook_event_name": "PreToolUse",
		"cwd":             cwd,
		"tool_name":       toolName,
	}
	if input != nil {
		p["tool_input"] = input
	}
	return mustJSON(p)
}

func kiroCall(t *testing.T, f *fixture, toolName, cwd string, input any) Call {
	t.Helper()
	call, err := normalizeCall(f.Env, localagent.Kiro, EventPreToolUse, kiroPayload(toolName, cwd, input))
	if err != nil {
		t.Fatalf("normalizeCall: %v", err)
	}
	if call.Request.Agent != "kiro" {
		t.Fatalf("agent = %q, want kiro", call.Request.Agent)
	}
	return call
}

func callResolution(call Call) Resolution {
	return Resolution{
		ServerName: call.Request.ServerName,
		Identity:   call.Request.Server,
		Unresolved: call.Request.Unresolved,
		Reason:     call.Request.UnresolvedReason,
		Trace:      call.Trace,
	}
}

func TestKiroSanitize(t *testing.T) {
	for in, want := range map[string]string{
		"github":              "github",
		"GitHub-Enterprise":   "github_enterprise",
		"my server":           "my_server",
		"acme.io/tools@v2":    "acmeiotoolsv2",
		"power-stripe-stripe": "power_stripe_stripe",
	} {
		if got := kiroSanitize(in); got != want {
			t.Errorf("kiroSanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKiroBuiltinTools(t *testing.T) {
	f := newFixture(t, "darwin")
	for name, kind := range map[string]string{
		"execute_bash": toolkind.KindShell,
		"read_file":    toolkind.KindRead,
		"fs_write":     toolkind.KindWrite,
		"web_fetch":    toolkind.KindGeneric,
		// A non-use kiro_powers action reads the Power's own files.
		"kiro_powers": toolkind.KindGeneric,
	} {
		call := kiroCall(t, f, name, f.Home, map[string]any{"action": "activate", "powerName": "stripe"})
		if call.Request.Kind != kind || call.Request.Tool != name || call.Request.Unresolved {
			t.Errorf("%s: request = %+v, want kind %s", name, call.Request, kind)
		}
	}
}

func TestKiroMCPToolResolvesFromUserConfig(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".kiro", "settings", "mcp.json"), `{
		// Kiro reads its settings as JSONC.
		"mcpServers": {
			"GitHub-Enterprise": {"url": "https://gh.example.com/mcp?token=secret", "disabled": true},
			"fetch": {"command": "uvx", "args": ["mcp-server-fetch"]}
		}
	}`)
	call := kiroCall(t, f, "mcp_github_enterprise_create_issue", f.path("proj"), map[string]any{"title": "x"})
	if call.Request.Kind != toolkind.KindMCP {
		t.Fatalf("kind = %q", call.Request.Kind)
	}
	// Disabled only governs whether the server starts; the name still points here.
	assertURL(t, callResolution(call), "https://gh.example.com/mcp")
	if call.Request.ServerName != "GitHub-Enterprise" || call.Request.Tool != "create_issue" {
		t.Fatalf("server/tool = %q/%q", call.Request.ServerName, call.Request.Tool)
	}

	call = kiroCall(t, f, "mcp_fetch_fetch", f.path("proj"), nil)
	assertPackage(t, callResolution(call), "pypi", "mcp-server-fetch", "")
}

func TestKiroWorkspaceBeatsUser(t *testing.T) {
	f := newFixture(t, "darwin")
	proj := f.mkdir(f.path("proj"))
	f.write(f.homePath(".kiro", "settings", "mcp.json"), `{"mcpServers": {"docs": {"url": "https://user.example.com/mcp"}}}`)
	f.write(f.path("proj", ".kiro", "settings", "mcp.json"), `{"mcpServers": {"docs": {"url": "https://workspace.example.com/mcp"}}}`)
	assertURL(t, callResolution(kiroCall(t, f, "mcp_docs_search", proj, nil)), "https://workspace.example.com/mcp")
}

// TestKiroPrefixAmbiguity: with servers "github" and "github_enterprise", the id
// mcp_github_enterprise_x is either server's; picking one could report an
// allowlisted server for a call that went to the other.
func TestKiroPrefixAmbiguity(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".kiro", "settings", "mcp.json"), `{"mcpServers": {
		"github": {"url": "https://api.githubcopilot.com/mcp/"},
		"github_enterprise": {"url": "https://evil.example.com/mcp"}
	}}`)
	call := kiroCall(t, f, "mcp_github_enterprise_x", f.Home, nil)
	assertUnresolved(t, callResolution(call), "more than one way")

	// A name only one of them can produce resolves.
	assertURL(t, callResolution(kiroCall(t, f, "mcp_github_list_repos", f.Home, nil)), "https://api.githubcopilot.com/mcp/")
}

// TestKiroSanitizedAlikeKeys: keys that sanitize to the same prefix are one
// reading when they define the same server, and ambiguous when they don't.
func TestKiroSanitizedAlikeKeys(t *testing.T) {
	f := newFixture(t, "darwin")
	cfg := f.homePath(".kiro", "settings", "mcp.json")
	f.write(cfg, `{"mcpServers": {
		"my-srv": {"url": "https://a.example.com/mcp"},
		"my_srv": {"url": "https://a.example.com/mcp"}
	}}`)
	assertURL(t, callResolution(kiroCall(t, f, "mcp_my_srv_go", f.Home, nil)), "https://a.example.com/mcp")

	f.write(cfg, `{"mcpServers": {
		"my-srv": {"url": "https://a.example.com/mcp"},
		"my_srv": {"url": "https://b.example.com/mcp"}
	}}`)
	assertUnresolved(t, callResolution(kiroCall(t, f, "mcp_my_srv_go", f.Home, nil)), "more than one way")
}

func TestKiroUnknownMCPToolIsUnresolved(t *testing.T) {
	f := newFixture(t, "darwin")
	call := kiroCall(t, f, "mcp_nowhere_tool", f.Home, nil)
	if call.Request.Kind != toolkind.KindMCP {
		t.Fatalf("kind = %q", call.Request.Kind)
	}
	assertUnresolved(t, callResolution(call), "no Kiro MCP configuration declares")
}

func TestKiroPowers(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".kiro", "powers", "installed.json"), `{"version": "1.0.0", "installedPowers": [
		{"name": "stripe", "registryId": "kiro-recommended"},
		{"name": "../escape", "registryId": "user-added"}
	]}`)
	f.write(f.homePath(".kiro", "powers", "installed", "stripe", "plugin.json"), `{"name": "stripe"}`)
	f.write(f.homePath(".kiro", "powers", "installed", "stripe", "mcp.json"),
		`{"$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json", "mcpServers": {"stripe": {"type": "streamable-http", "url": "https://mcp.stripe.com"}}}`)
	// A legacy power's server, registered in the settings file's powers section.
	f.write(f.homePath(".kiro", "settings", "mcp.json"),
		`{"mcpServers": {}, "powers": {"mcpServers": {"power-figma-figma": {"url": "https://mcp.figma.com/mcp"}}}}`)

	// Through kiro_powers, with the real tool name.
	call := kiroCall(t, f, "kiro_powers", f.Home, map[string]any{
		"action": "use", "powerName": "stripe", "serverName": "stripe", "toolName": "create_payment_link", "arguments": map[string]any{},
	})
	assertURL(t, callResolution(call), "https://mcp.stripe.com")
	if call.Request.ServerName != "power-stripe-stripe" || call.Request.Tool != "create_payment_link" {
		t.Fatalf("server/tool = %q/%q", call.Request.ServerName, call.Request.Tool)
	}

	// Legacy power servers are ordinary MCP tools to Kiro.
	assertURL(t, callResolution(kiroCall(t, f, "mcp_power_figma_figma_get_file", f.Home, nil)), "https://mcp.figma.com/mcp")

	// A use call that names nothing is unresolved, not built-in.
	call = kiroCall(t, f, "kiro_powers", f.Home, map[string]any{"action": "use"})
	if call.Request.Kind != toolkind.KindMCP {
		t.Fatalf("kind = %q", call.Request.Kind)
	}
	assertUnresolved(t, callResolution(call), "did not name its power")
}

// TestKiroAgentProfileConflict: the payload doesn't say which agent is running,
// so a profile that defines a name differently from the settings files is
// ambiguous rather than taken on trust.
func TestKiroAgentProfileConflict(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".kiro", "settings", "mcp.json"), `{"mcpServers": {"docs": {"url": "https://real.example.com/mcp"}}}`)
	agent := f.write(f.homePath(".kiro", "agents", "reviewer.json"), `{"name": "reviewer", "mcpServers": {"docs": {"url": "https://allowed.example.com/mcp"}}}`)
	assertUnresolved(t, callResolution(kiroCall(t, f, "mcp_docs_search", f.Home, nil)), "conflicting definitions")

	// Agreeing definitions, and a name only a profile declares, both resolve.
	f.write(agent, `{"name": "reviewer", "mcpServers": {"docs": {"url": "https://real.example.com/mcp"}, "git": {"command": "uvx", "args": ["mcp-server-git"]}}}`)
	assertURL(t, callResolution(kiroCall(t, f, "mcp_docs_search", f.Home, nil)), "https://real.example.com/mcp")
	f.write(f.homePath(".kiro", "agents", "team", "writer.md"),
		"---\nname: writer\nmcpServers:\n  search:\n    url: https://search.example.com/mcp\n---\nYou write.\n")
	assertURL(t, callResolution(kiroCall(t, f, "mcp_search_query", f.Home, nil)), "https://search.example.com/mcp")
	assertPackage(t, callResolution(kiroCall(t, f, "mcp_git_log", f.Home, nil)), "pypi", "mcp-server-git", "")
}

func TestKiroDeferredToolCall(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".kiro", "settings", "mcp.json"), `{"mcpServers": {"docs": {"url": "https://docs.example.com/mcp"}}}`)
	call := kiroCall(t, f, "tool_call", f.Home, map[string]any{"tool_id": "docs::search", "arguments": map[string]any{}})
	assertURL(t, callResolution(call), "https://docs.example.com/mcp")
	if call.Request.Tool != "search" {
		t.Fatalf("tool = %q", call.Request.Tool)
	}
}

// TestKiroRunProtocol: Kiro blocks only on exit 2 and reads the reason from
// stderr, so a deny writes nothing to stdout and sets BlockByExit; an allow
// writes nothing at all.
func TestKiroRunProtocol(t *testing.T) {
	f := newFixture(t, "darwin")
	run := func(decision string) (Result, string, string) {
		var stdout, stderr bytes.Buffer
		res := Run(context.Background(), Options{
			Env:    f.Env,
			Agent:  "kiro",
			Event:  "PreToolUse",
			Input:  bytes.NewReader(kiroPayload("execute_bash", f.Home, map[string]any{"command": "ls"})),
			Stdout: &stdout,
			Stderr: &stderr,
			Decide: func(_ context.Context, req types.EnforcementDecisionRequest) (types.EnforcementDecisionResponse, error) {
				if req.Agent != "kiro" || req.Kind != toolkind.KindShell {
					t.Errorf("request = %+v", req)
				}
				return types.EnforcementDecisionResponse{Decision: decision}, nil
			},
		})
		return res, stdout.String(), stderr.String()
	}

	res, stdout, stderr := run(types.EnforcementDecisionAllow)
	if res.Denied || res.BlockByExit || stdout != "" || stderr != "" {
		t.Fatalf("allow: result %+v, stdout %q, stderr %q", res, stdout, stderr)
	}

	res, stdout, stderr = run(types.EnforcementDecisionDeny)
	if !res.Denied || !res.BlockByExit {
		t.Fatalf("deny: result %+v", res)
	}
	if stdout != "" {
		t.Fatalf("deny wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "the policy refused it") || strings.Contains(stderr, "obot-sentry enforce: blocked") {
		t.Fatalf("deny stderr = %q, want only the agent-facing denial", stderr)
	}
}

// TestKiroMalformedPayloadBlocks: a payload that can't be read is an
// infrastructure deny, which for Kiro is still exit 2.
func TestKiroMalformedPayloadBlocks(t *testing.T) {
	f := newFixture(t, "darwin")
	var stdout, stderr bytes.Buffer
	res := Run(context.Background(), Options{
		Env: f.Env, Agent: "kiro", Event: "PreToolUse",
		Input: strings.NewReader("{"), Stdout: &stdout, Stderr: &stderr,
	})
	if !res.Denied || !res.BlockByExit || stdout.Len() != 0 || !strings.Contains(stderr.String(), "could not reach a verdict") {
		t.Fatalf("result %+v, stdout %q, stderr %q", res, stdout.String(), stderr.String())
	}
	var probe map[string]any
	if json.Unmarshal(stderr.Bytes(), &probe) == nil {
		t.Fatalf("Kiro deny should be plain text, got JSON: %s", stderr.String())
	}
}

// kiroCallInSession is kiroCall with a session id, whose session record lists
// the workspace roots.
func kiroCallInSession(t *testing.T, f *fixture, toolName, cwd, sessionID string) Call {
	t.Helper()
	p := map[string]any{"session_id": sessionID, "hook_event_name": "PreToolUse", "cwd": cwd, "tool_name": toolName}
	call, err := normalizeCall(f.Env, localagent.Kiro, EventPreToolUse, mustJSON(p))
	if err != nil {
		t.Fatalf("normalizeCall: %v", err)
	}
	return call
}

// TestKiroSecondWorkspaceRoot (review t1): the payload's cwd is only the first
// root, but Kiro merges every root's mcp.json, so a server declared by another
// root must not be resolved from the user file's allowlisted entry instead.
func TestKiroSecondWorkspaceRoot(t *testing.T) {
	f := newFixture(t, "darwin")
	first := f.mkdir(f.path("proj-a"))
	second := f.mkdir(f.path("proj-b"))
	f.write(f.homePath(".kiro", "settings", "mcp.json"), `{"mcpServers": {"docs": {"url": "https://allowed.example.com/mcp"}}}`)
	f.write(filepath.Join(second, ".kiro", "settings", "mcp.json"), `{"mcpServers": {"docs": {"url": "https://other.example.com/mcp"}}}`)
	f.write(f.homePath(".kiro", "sessions", "abc123", "sess_1111-2222", "session.json"),
		`{"schemaVersion": "1.0.0", "workspacePaths": ["`+first+`", "`+second+`"]}`)

	// With the session record, the second root's definition wins over the user file.
	assertURL(t, callResolution(kiroCallInSession(t, f, "mcp_docs_search", first, "sess_1111-2222")), "https://other.example.com/mcp")

	// Two roots that disagree are ambiguous: nothing says which root wins.
	f.write(filepath.Join(first, ".kiro", "settings", "mcp.json"), `{"mcpServers": {"docs": {"url": "https://third.example.com/mcp"}}}`)
	assertUnresolved(t, callResolution(kiroCallInSession(t, f, "mcp_docs_search", first, "sess_1111-2222")), "conflicting definitions")

	// A session id that is not one of Kiro's can't name a path.
	call := kiroCallInSession(t, f, "mcp_docs_search", first, "../../etc")
	assertURL(t, callResolution(call), "https://third.example.com/mcp")
}

// TestKiroAgentProfilesIncomplete (review t2): profiles that can't all be read
// are a reason to refuse, not to resolve without them.
func TestKiroAgentProfilesIncomplete(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".kiro", "settings", "mcp.json"), `{"mcpServers": {"docs": {"url": "https://allowed.example.com/mcp"}}}`)
	for i := 0; i <= kiroMaxAgentFiles; i++ {
		f.write(f.homePath(".kiro", "agents", fmt.Sprintf("a%04d.json", i)), `{"name": "x"}`)
	}
	assertUnresolved(t, callResolution(kiroCall(t, f, "mcp_docs_search", f.Home, nil)), "more than 256")
}

func TestKiroAgentDirUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any directory")
	}
	f := newFixture(t, "darwin")
	f.write(f.homePath(".kiro", "settings", "mcp.json"), `{"mcpServers": {"docs": {"url": "https://allowed.example.com/mcp"}}}`)
	locked := f.mkdir(f.homePath(".kiro", "agents", "team"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	assertUnresolved(t, callResolution(kiroCall(t, f, "mcp_docs_search", f.Home, nil)), "could not all be read")
}

// TestKiroSanitizeUnicodeWhitespace (review t3): Kiro's /[\s-]/g is
// JavaScript's \s, which includes no-break and other Unicode spaces.
func TestKiroSanitizeUnicodeWhitespace(t *testing.T) {
	for in, want := range map[string]string{
		"my\u00a0srv":            "my_srv",
		"my\u2003srv":            "my_srv",
		"my\ufeffsrv":            "my_srv",
		"my\u0085srv":            "mysrv", // NEL is not JavaScript whitespace
		"\u00dcn\u00efcode-Name": "ncode_name",
	} {
		if got := kiroSanitize(in); got != want {
			t.Errorf("kiroSanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestKiroPowerKeyShadowed (review t4): a same-named entry in mcp.json must not
// answer for a Power's own server; they are peers, and disagreeing is ambiguous.
func TestKiroPowerKeyShadowed(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".kiro", "powers", "installed.json"), `{"version": "1.0.0", "installedPowers": [{"name": "stripe", "registryId": "kiro-recommended"}]}`)
	f.write(f.homePath(".kiro", "powers", "installed", "stripe", "plugin.json"), `{"name": "stripe"}`)
	f.write(f.homePath(".kiro", "powers", "installed", "stripe", "mcp.json"),
		`{"mcpServers": {"stripe": {"type": "streamable-http", "url": "https://mcp.stripe.com"}}}`)
	f.write(f.homePath(".kiro", "settings", "mcp.json"), `{"mcpServers": {"power-stripe-stripe": {"url": "https://allowed.example.com/mcp"}}}`)

	use := map[string]any{"action": "use", "powerName": "stripe", "serverName": "stripe", "toolName": "create_payment_link"}
	assertUnresolved(t, callResolution(kiroCall(t, f, "kiro_powers", f.Home, use)), "conflicting definitions")
	assertUnresolved(t, callResolution(kiroCall(t, f, "mcp_power_stripe_stripe_create_payment_link", f.Home, nil)), "conflicting definitions")
}

// TestKiroSessionRecordIncomplete (review t1, round 2): a session record that
// exists but can't be read, or names more roots than are checked, refuses the
// call; a missing record falls back to cwd.
func TestKiroSessionRecordIncomplete(t *testing.T) {
	f := newFixture(t, "darwin")
	proj := f.mkdir(f.path("proj"))
	f.write(f.homePath(".kiro", "settings", "mcp.json"), `{"mcpServers": {"docs": {"url": "https://allowed.example.com/mcp"}}}`)

	// No record for this session: cwd alone, as before.
	assertURL(t, callResolution(kiroCallInSession(t, f, "mcp_docs_search", proj, "sess_missing")), "https://allowed.example.com/mcp")

	record := f.homePath(".kiro", "sessions", "h", "sess_broken", "session.json")
	f.write(record, `{"workspacePaths": [`)
	assertUnresolved(t, callResolution(kiroCallInSession(t, f, "mcp_docs_search", proj, "sess_broken")), "could not be read")

	paths := make([]string, 0, kiroMaxWorkspaceRoots+1)
	for i := 0; i <= kiroMaxWorkspaceRoots; i++ {
		paths = append(paths, f.path(fmt.Sprintf("root%03d", i)))
	}
	f.write(f.homePath(".kiro", "sessions", "h", "sess_many", "session.json"), string(mustJSON(map[string]any{"workspacePaths": paths})))
	assertUnresolved(t, callResolution(kiroCallInSession(t, f, "mcp_docs_search", proj, "sess_many")), "more than 64 workspace roots")
}
