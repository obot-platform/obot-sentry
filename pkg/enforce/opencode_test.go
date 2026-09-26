package enforce

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
	"github.com/obot-platform/obot/apiclient/types"
)

func TestParseOpenCodeEnforcementAgentAndEvent(t *testing.T) {
	agent, err := ParseAgent("opencode")
	if err != nil || agent != localagent.OpenCode {
		t.Fatalf("ParseAgent(opencode) = %q, %v", agent, err)
	}
	event, err := ParseEvent(agent, string(EventOpenCodePermission))
	if err != nil || event != EventOpenCodePermission {
		t.Fatalf("ParseEvent(opencode) = %q, %v", event, err)
	}
	if got := wireAgent(agent); got != "opencode" {
		t.Fatalf("wireAgent(opencode) = %q", got)
	}
	if got := Events(agent); len(got) != 1 || got[0] != EventOpenCodePermission {
		t.Fatalf("Events(opencode) = %v", got)
	}
}

func TestNormalizeOpenCodeRejectsMissingAction(t *testing.T) {
	env := Env{Home: t.TempDir(), GOOS: "windows"}
	if _, err := normalizeOpenCodePermission(context.Background(), env, []byte(`{"cwd":"C:/work"}`)); err == nil {
		t.Fatal("an OpenCode payload without an action must be refused")
	}
}

func TestOpenCodeBuiltinActionKinds(t *testing.T) {
	env := Env{Home: t.TempDir(), GOOS: "windows"}
	cases := map[string]string{
		"read":      "read",
		"edit":      "write",
		"write":     "write",
		"patch":     "write",
		"shell":     "shell",
		"subagent":  "task",
		"glob":      "generic",
		"todowrite": "generic",
		"doom_loop": "generic",
		"websearch": "generic",
		"list":      "generic",
	}
	for action, wantKind := range cases {
		payload, err := json.Marshal(openCodePermissionPayload{Action: action, CWD: "C:/work"})
		if err != nil {
			t.Fatal(err)
		}
		call, err := normalizeOpenCodePermission(context.Background(), env, payload)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if call.Request.Kind != wantKind {
			t.Errorf("%s: kind = %q, want %q", action, call.Request.Kind, wantKind)
		}
		if call.Request.Unresolved {
			t.Errorf("%s: a known built-in must resolve", action)
		}
	}
}

func TestOpenCodeUnknownActionIsUnresolved(t *testing.T) {
	// No configuration is readable here, so an unrecognized action must not fall
	// through to the generic kind, which allowAllBuiltinAgentTools would allow.
	env := Env{Home: t.TempDir(), GOOS: "windows"}
	call, err := normalizeOpenCodePermission(context.Background(), env,
		[]byte(`{"action":"something_new_v3","cwd":"C:/work"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !call.Request.Unresolved {
		t.Fatalf("an unknown OpenCode action must be unresolved, got %#v", call.Request)
	}
	// It must not be filed as the generic kind, which the built-in-tools toggle
	// would then allow with no target identity at all.
	if call.Request.Kind != "mcp" {
		t.Fatalf("an unknown OpenCode action must not be filed as a built-in kind: %#v", call.Request)
	}
	if call.Request.UnresolvedReason == "" {
		t.Fatal("an unresolved OpenCode call must carry a reason")
	}
}

func TestOpenCodeMCPServerIsResolvedFromLocalConfigOnly(t *testing.T) {
	f := newFixture(t, "windows")
	f.write(f.homePath(".config", "opencode", "opencode.jsonc"), `{
		// OpenCode reads JSONC.
		"mcp": {
			"github": {"type": "remote", "url": "https://mcp.example.test/sse"},
			"local-tool": {"type": "local", "command": ["npx", "-y", "@example/mcp"]}
		}
	}`)

	candidates, ambiguous, err := openCodeServerCandidates(context.Background(), f.Env, "github_search", f.homePath("project"))
	if err != nil || ambiguous {
		t.Fatalf("candidates = %v, ambiguous = %v, err = %v", candidates, ambiguous, err)
	}
	if len(candidates) != 1 || candidates[0] != "github" {
		t.Fatalf("candidates for github_search = %v", candidates)
	}

	// OpenCode's sanitizer keeps hyphens, so a hyphenated server is reached
	// through its own name and not through an underscore rewrite of it.
	candidates, _, err = openCodeServerCandidates(context.Background(), f.Env, "local-tool_query", f.homePath("project"))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0] != "local-tool" {
		t.Fatalf("candidates for local-tool_query = %v", candidates)
	}

	cwd := strings.ReplaceAll(f.homePath("project"), `\`, "/")
	call, err := normalizeOpenCodePermission(context.Background(), f.Env,
		[]byte(`{"action":"github_search","cwd":"`+cwd+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if call.Request.Unresolved || call.Request.Server.URL != "https://mcp.example.test/sse" {
		t.Fatalf("unexpected OpenCode call: %#v", call.Request)
	}
}

func TestOpenCodeCallerCannotChooseItsOwnServerCandidate(t *testing.T) {
	// A caller that names its own server hint could omit a longer allowlisted
	// server and make the call resolve as a shorter one. The payload carries no
	// such field, so the only enumeration is the local configuration's.
	f := newFixture(t, "windows")
	f.write(f.homePath(".config", "opencode", "opencode.jsonc"), `{
		"mcp": {
			"github": {"type": "remote", "url": "https://mcp.example.test/sse"}
		}
	}`)
	cwd := strings.ReplaceAll(f.homePath("project"), `\`, "/")

	call, err := normalizeOpenCodePermission(context.Background(), f.Env, []byte(`{
		"action":"github_search",
		"cwd":"`+cwd+`",
		"mcp_servers":["attacker_controlled"],
		"mcp_server_candidates":["attacker_controlled"],
		"server_name":"attacker_controlled"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if call.Request.ServerName != "github" {
		t.Fatalf("a caller-supplied server name must be ignored, got %q", call.Request.ServerName)
	}
	if call.Request.Server.URL != "https://mcp.example.test/sse" {
		t.Fatalf("the local configuration must decide the identity: %#v", call.Request)
	}
}

func TestOpenCodeNamespaceCollisionIsRefused(t *testing.T) {
	f := newFixture(t, "windows")
	// "a.b" and "a_b" both fold to the namespace "a_b".
	f.write(f.homePath(".config", "opencode", "opencode.jsonc"), `{
		"mcp": {
			"a.b": {"type": "remote", "url": "https://one.example.test/mcp"},
			"a_b": {"type": "remote", "url": "https://two.example.test/mcp"}
		}
	}`)

	_, ambiguous, err := openCodeServerCandidates(context.Background(), f.Env, "a_b_search", f.homePath("project"))
	if err != nil {
		t.Fatal(err)
	}
	if !ambiguous {
		t.Fatal("two names that collapse to one namespace must be reported as ambiguous")
	}
}

func TestOpenCodeAmbiguousPrefixKeepsEveryCandidate(t *testing.T) {
	f := newFixture(t, "windows")
	f.write(f.homePath(".config", "opencode", "opencode.jsonc"), `{
		"mcp": {
			"docs":        {"type": "remote", "url": "https://docs.example.test/mcp"},
			"docs_search": {"type": "remote", "url": "https://search.example.test/mcp"}
		}
	}`)

	// "docs_search_extra" is a tool of either server, so both must be kept.
	cwd := strings.ReplaceAll(f.homePath("project"), `\`, "/")
	call, err := normalizeOpenCodePermission(context.Background(), f.Env,
		[]byte(`{"action":"docs_search_extra","cwd":"`+cwd+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if call.Request.Kind != "mcp" {
		t.Fatalf("expected an MCP call, got %#v", call.Request)
	}
}

func TestOpenCodeEntryOpenCodeWouldNotStartStaysUnresolved(t *testing.T) {
	f := newFixture(t, "windows")
	f.write(f.homePath(".config", "opencode", "opencode.jsonc"), `{
		"mcp": {
			"github": {"type": "remote", "url": "https://mcp.example.test/sse"},
			"broken": {"type": "nonsense"},
			"bare":   {"type": "local", "command": "not-an-array"}
		}
	}`)

	for _, server := range []string{"broken", "bare"} {
		res := resolveOpenCode(context.Background(), newConfigLoader(), f.Env, ResolveRequest{
			Agent:      localagent.OpenCode,
			ServerName: server,
			CWD:        f.homePath("project"),
		}, server, &tracer{})
		if !res.Unresolved {
			t.Errorf("%s: an entry OpenCode would not start must stay unresolved: %#v", server, res)
		}
	}
}

func TestOpenCodeMissingConfigStaysUnresolved(t *testing.T) {
	f := newFixture(t, "windows")
	res := resolveOpenCode(context.Background(), newConfigLoader(), f.Env, ResolveRequest{
		Agent:      localagent.OpenCode,
		ServerName: "github",
		CWD:        f.homePath("project"),
	}, "github", &tracer{})
	if !res.Unresolved || res.Reason == "" {
		t.Fatalf("a missing OpenCode config must be unresolved: %#v", res)
	}
}

func TestOpenCodePayloadCarriesNoArguments(t *testing.T) {
	// The public request is parameter-free: nothing from the OpenCode event
	// beyond the action and the working directory may reach a decision.
	env := Env{Home: t.TempDir(), GOOS: "windows"}
	payload := []byte(`{
		"action":"shell",
		"cwd":"C:/work",
		"session_id":"secret-session",
		"agent":"build",
		"resources":["C:/secret/file.txt"],
		"metadata":{"password":"hunter2"},
		"source":{"id":"call-1"},
		"mcp_servers":["attacker_supplied"]
	}`)
	call, err := normalizeOpenCodePermission(context.Background(), env, payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(call.Request)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-session", "hunter2", "call-1", "attacker_supplied", "secret/file.txt"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("decision request leaked %q: %s", secret, raw)
		}
	}
}

func TestOpenCodeUnresolvedCallIsNeverAllowed(t *testing.T) {
	raw := []byte(`{"action":"github_search","cwd":"C:/work/project"}`)
	var stdout, stderr bytes.Buffer
	result := Run(context.Background(), Options{
		Agent:  "opencode",
		Event:  string(EventOpenCodePermission),
		Input:  bytes.NewReader(raw),
		Stdout: &stdout,
		Stderr: &stderr,
		Decide: func(context.Context, types.EnforcementDecisionRequest) (types.EnforcementDecisionResponse, error) {
			// Even a server that answers allow must not release a call whose
			// target was never resolved.
			return types.EnforcementDecisionResponse{Decision: types.EnforcementDecisionAllow}, nil
		},
	})
	if !result.Denied {
		t.Fatalf("an unresolved OpenCode call was allowed: %#v", result)
	}
	var response openCodeHookOutput
	if err := json.Unmarshal(result.Response, &response); err != nil {
		t.Fatal(err)
	}
	if response.Effect != "deny" {
		t.Fatalf("unresolved OpenCode response = %#v", response)
	}
}

func TestOpenCodeVerdictProtocol(t *testing.T) {
	var allow openCodeHookOutput
	if err := json.Unmarshal(Allow(localagent.OpenCode), &allow); err != nil {
		t.Fatal(err)
	}
	if allow.Effect != "allow" {
		t.Fatalf("OpenCode allow response = %#v", allow)
	}

	var deny openCodeHookOutput
	if err := json.Unmarshal(Deny(localagent.OpenCode, EventOpenCodePermission, PolicyDenial()), &deny); err != nil {
		t.Fatal(err)
	}
	if deny.Effect != "deny" || deny.Message == "" {
		t.Fatalf("OpenCode deny response = %#v", deny)
	}
}
