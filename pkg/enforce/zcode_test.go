package enforce

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
	"github.com/obot-platform/obot/apiclient/types"
)

func zcodeRequest(serverName, cwd string) ResolveRequest {
	return ResolveRequest{Agent: localagent.ZCode, ServerName: serverName, CWD: cwd}
}

func TestZCodeNamespace(t *testing.T) {
	if got := formZCode("android-emulator"); got != "android_emulator" {
		t.Fatalf("formZCode(android-emulator) = %q", got)
	}
	if got := formZCode("plugin:example:server"); got != "plugin_example_server" {
		t.Fatalf("formZCode(plugin namespace) = %q", got)
	}
}

func TestZCodeEnforcementProtocol(t *testing.T) {
	agent, err := ParseAgent("zcode")
	if err != nil || agent != localagent.ZCode {
		t.Fatalf("ParseAgent(zcode) = %q, %v", agent, err)
	}
	event, err := ParseEvent(agent, "PreToolUse")
	if err != nil || event != EventPreToolUse {
		t.Fatalf("ParseEvent(ZCode, PreToolUse) = %q, %v", event, err)
	}
	if got := wireAgent(agent); got != wireAgentZCode {
		t.Fatalf("wireAgent(ZCode) = %q, want %q", got, wireAgentZCode)
	}
	if got := Events(agent); len(got) != 1 || got[0] != EventPreToolUse {
		t.Fatalf("Events(ZCode) = %v, want [PreToolUse]", got)
	}
}

func TestZCodeResolvesUserConfig(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".zcode", "cli", "config.json"), `{
		"mcp": {"servers": {"docs": {"type": "http", "url": "https://user.example/mcp"}}}
	}`)
	assertURL(t, Resolve(f.Env, zcodeRequest("docs", f.path("project"))), "https://user.example/mcp")
}

func TestZCodeProjectConfigAndUserPrecedence(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("project", "nested"))
	f.write(f.homePath(".zcode", "cli", "config.json"), `{
		"mcp": {"servers": {"docs": {"url": "https://user.example/mcp"}}}
	}`)
	f.write(filepath.Join(project, ".zcode", "config.json"), `{
		"mcp": {"servers": {
			"docs": {"url": "https://project.example/mcp"},
			"project-only": {"url": "https://project-only.example/mcp"}
		}}
	}`)

	assertURL(t, Resolve(f.Env, zcodeRequest("docs", project)), "https://user.example/mcp")
	assertURL(t, Resolve(f.Env, zcodeRequest("project-only", project)), "https://project-only.example/mcp")
}

func TestZCodeAgentsFallbackOnlyWhenNativeConfigIsAbsent(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("project"))
	f.write(f.homePath(".agents", "mcp.json"), `{"mcpServers": {"legacy": {"url": "https://legacy.example/mcp"}}}`)
	f.write(filepath.Join(project, ".agents", "mcp.json"), `{"mcpServers": {"project-legacy": {"url": "https://project-legacy.example/mcp"}}}`)
	assertURL(t, Resolve(f.Env, zcodeRequest("legacy", project)), "https://legacy.example/mcp")
	assertURL(t, Resolve(f.Env, zcodeRequest("project-legacy", project)), "https://project-legacy.example/mcp")

	f.write(f.homePath(".zcode", "cli", "config.json"), `{"mcp":{"servers":{}}}`)
	assertURL(t, Resolve(f.Env, zcodeRequest("legacy", project)), "https://legacy.example/mcp")
}

func TestZCodeResolvesPluginMCP(t *testing.T) {
	f := newFixture(t, "darwin")
	pluginDir := f.homePath(".zcode", "plugins", "example")
	f.write(filepath.Join(pluginDir, ".zcode-plugin", "plugin.json"), `{"name":"example","version":"1.0.0"}`)
	f.write(filepath.Join(pluginDir, ".mcp.json"), `{"mcpServers":{"server":{"url":"https://plugin.example/mcp"}}}`)
	assertURL(t, Resolve(f.Env, zcodeRequest("plugin:example:server", f.path("project"))), "https://plugin.example/mcp")
}

func TestZCodePluginTemplateExpansion(t *testing.T) {
	f := newFixture(t, "linux")
	pluginDir := f.homePath(".zcode", "cli", "plugins", "cache", "market", "example", "1.0.0")
	manifest := filepath.Join(pluginDir, ".zcode-plugin", "plugin.json")
	f.write(manifest, `{
		"name":"example",
		"userConfig":{"sdk_path":{"type":"string","default":"/sdk"}}
	}`)
	f.write(filepath.Join(pluginDir, ".mcp.json"), `{
		"mcpServers":{"server":{"command":"node","args":["${CLAUDE_PLUGIN_ROOT}/server.js"],"env":{"SDK":"${user_config.sdk_path}","DATA":"${ZCODE_PLUGIN_DATA}"}}}
	}`)
	set, _, ok := zcodePluginServers(context.Background(), newConfigLoader(), f.Env, f.homePath(".zcode"), pluginDir, manifest, f.path("project"))
	if !ok {
		t.Fatal("ZCode plugin template was not loaded")
	}
	entry := set["plugin:example:server"]
	if entry.ConfigError != "" {
		t.Fatalf("plugin template had unresolved variables: %s", entry.ConfigError)
	}
	if entry.Command != "node" || len(entry.Args) != 1 || filepath.Clean(entry.Args[0]) != filepath.Join(pluginDir, "server.js") {
		t.Fatalf("plugin command was not expanded: %+v", entry)
	}
	if entry.Environment["SDK"] != "/sdk" || entry.Environment["DATA"] == "" {
		t.Fatalf("plugin environment was not expanded: %+v", entry.Environment)
	}
	res := Resolve(f.Env, zcodeRequest("plugin:example:server", f.path("project")))
	if res.Unresolved || res.Identity.Connector != "zcode-plugin:example:server" {
		t.Fatalf("plugin server did not resolve to a stable connector: %+v", res)
	}
}

func TestZCodePluginManifestMCPPath(t *testing.T) {
	f := newFixture(t, "linux")
	pluginDir := f.homePath(".zcode", "plugins", "example")
	manifest := filepath.Join(pluginDir, ".zcode-plugin", "plugin.json")
	f.write(manifest, `{"name":"example","mcpServers":"mcp.json"}`)
	f.write(filepath.Join(pluginDir, "mcp.json"), `{"mcpServers":{"server":{"url":"https://manifest.example/mcp"}}}`)
	set, _, ok := zcodePluginServers(context.Background(), newConfigLoader(), f.Env, f.homePath(".zcode"), pluginDir, manifest, f.path("project"))
	if !ok || set["plugin:example:server"].URL != "https://manifest.example/mcp" {
		t.Fatalf("manifest MCP path was not resolved: %+v", set)
	}
}

func TestZCodeDisabledServerMasksLowerScope(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("project"))
	f.write(f.homePath(".zcode", "cli", "config.json"), `{"mcp":{"servers":{}}}`)
	f.write(filepath.Join(project, ".zcode", "config.json"), `{
		"mcp": {"servers": {"docs": {"url": "https://project.example/mcp", "enable": false}}}
	}`)
	assertUnresolved(t, Resolve(f.Env, zcodeRequest("docs", project)), "disabled")
}

func TestNormalizeZCodePreToolUse(t *testing.T) {
	f := newFixture(t, "windows")
	cwd := filepath.Join(f.Home, "zcode-project")
	f.write(f.homePath(".zcode", "cli", "config.json"), `{
		"mcp": {"servers": {"docs": {"url": "https://docs.example/mcp"}}}
	}`)
	payload := []byte(`{
		"hook_event_name": "PreToolUse",
		"tool_name": "mcp__docs__search",
		"tool_input": {"query": "x"},
		"tool_use_id": "zcode-tool-1",
		"session_id": "zcode-session-1",
		"cwd": ` + string(mustJSON(cwd)) + `
	}`)
	call, err := normalizeCall(f.Env, localagent.ZCode, EventPreToolUse, payload)
	if err != nil {
		t.Fatal(err)
	}
	if call.Request.Agent != wireAgentZCode || call.Request.Tool != "search" || call.Request.Kind != "mcp" {
		t.Fatalf("unexpected normalized request: %+v", call.Request)
	}
	if call.Request.Unresolved || call.Request.Server.URL != "https://docs.example/mcp" {
		t.Fatalf("unexpected resolution: %+v", call.Request)
	}
}

func TestZCodeUnknownToolIsUnresolved(t *testing.T) {
	f := newFixture(t, "linux")
	payload := []byte(`{"hook_event_name":"PreToolUse","tool_name":"plugin-defined-tool","cwd":"/tmp"}`)
	call, err := normalizeCall(f.Env, localagent.ZCode, EventPreToolUse, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !call.Request.Unresolved {
		t.Fatalf("unknown ZCode tool was not unresolved: %+v", call.Request)
	}
}

func TestZCodeAllowLeavesNativePermissionFlowUntouched(t *testing.T) {
	f := newFixture(t, "linux")
	run := runHook(t, f.Env, hookCase{
		agent:   "zcode",
		event:   "PreToolUse",
		payload: `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo ok"}}`,
		resp:    types.EnforcementDecisionResponse{Decision: types.EnforcementDecisionAllow},
	})
	if run.Result.Denied || run.stdout != "" {
		t.Fatalf("ZCode allow wrote a decision: denied=%v stdout=%q reason=%q", run.Result.Denied, run.stdout, run.Result.Reason)
	}
}

func TestZCodeDenyUsesStrictPreToolUseResponse(t *testing.T) {
	f := newFixture(t, "linux")
	run := runHook(t, f.Env, hookCase{
		agent:   "zcode",
		event:   "PreToolUse",
		payload: `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"false"}}`,
		resp:    types.EnforcementDecisionResponse{Decision: types.EnforcementDecisionDeny, Reason: "blocked"},
	})
	if !strings.Contains(run.stdout, `"hookEventName":"PreToolUse"`) || !strings.Contains(run.stdout, `"permissionDecision":"deny"`) {
		t.Fatalf("ZCode deny response = %q", run.stdout)
	}
}

func TestZCodeUnresolvedFailsClosedBeforeServerAllow(t *testing.T) {
	f := newFixture(t, "linux")
	run := runHook(t, f.Env, hookCase{
		agent:   "zcode",
		event:   "PreToolUse",
		payload: `{"hook_event_name":"PreToolUse","tool_name":"mcp__unknown__tool","tool_input":{}}`,
		resp:    types.EnforcementDecisionResponse{Decision: types.EnforcementDecisionAllow},
	})
	if !run.Result.Denied || run.Result.Reason == "" {
		t.Fatalf("unresolved ZCode target was not denied locally: %+v", run.Result)
	}
}
