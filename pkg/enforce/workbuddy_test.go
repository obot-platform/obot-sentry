package enforce

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

func TestWorkBuddyEnforcementProtocol(t *testing.T) {
	agent, err := ParseAgent("workbuddy")
	if err != nil || agent != localagent.WorkBuddy {
		t.Fatalf("ParseAgent(workbuddy) = %q, %v", agent, err)
	}
	event, err := ParseEvent(agent, "PreToolUse")
	if err != nil || event != EventPreToolUse {
		t.Fatalf("ParseEvent(WorkBuddy, PreToolUse) = %q, %v", event, err)
	}
	if got := wireAgent(agent); got != wireAgentWorkBuddy {
		t.Fatalf("wireAgent(WorkBuddy) = %q, want %q", got, wireAgentWorkBuddy)
	}
	if got := Events(agent); len(got) != 1 || got[0] != EventPreToolUse {
		t.Fatalf("Events(WorkBuddy) = %v, want [PreToolUse]", got)
	}
}

func workBuddyRequest(serverName, cwd string) ResolveRequest {
	return ResolveRequest{Agent: localagent.WorkBuddy, ServerName: serverName, CWD: cwd}
}

func TestWorkBuddyResolvesUserMCP(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".workbuddy", ".mcp.json"), `{
		// WorkBuddy accepts JSONC in MCP configuration.
		"mcpServers": {
			"docs": {"type": "http", "url": "https://user.example.com/mcp"},
		},
	}`)

	res := Resolve(f.Env, workBuddyRequest("docs", f.path("project")))
	assertURL(t, res, "https://user.example.com/mcp")
}

func TestWorkBuddyUserConfigUsesFirstExistingFile(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".workbuddy", ".mcp.json"), `{
		"mcpServers": {"current": {"url": "https://current.example.com/mcp"}}
	}`)
	f.write(f.homePath(".workbuddy", "mcp.json"), `{
		"mcpServers": {"legacy": {"url": "https://legacy.example.com/mcp"}}
	}`)

	assertURL(t, Resolve(f.Env, workBuddyRequest("current", f.path("project"))), "https://current.example.com/mcp")
	assertUnresolved(t, Resolve(f.Env, workBuddyRequest("legacy", f.path("project"))), "was not found in any WorkBuddy MCP configuration")
}

func TestWorkBuddyHonorsDesktopConfigDir(t *testing.T) {
	f := newFixture(t, "windows")
	configDir := f.path("custom-workbuddy")
	f.setenv("WORKBUDDY_CONFIG_DIR", configDir)
	f.write(filepath.Join(configDir, ".mcp.json"), `{
		"mcpServers": {"custom": {"url": "https://custom.example.com/mcp"}}
	}`)
	f.write(f.homePath(".workbuddy", ".mcp.json"), `{
		"mcpServers": {"default": {"url": "https://default.example.com/mcp"}}
	}`)

	assertURL(t, Resolve(f.Env, workBuddyRequest("custom", f.path("project"))), "https://custom.example.com/mcp")
	assertUnresolved(t, Resolve(f.Env, workBuddyRequest("default", f.path("project"))), "was not found in any WorkBuddy MCP configuration")
}

func TestWorkBuddySupportsDeprecatedConfigNames(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".workbuddy", "mcp.json"), `{
		"mcpServers": {"legacy-user": {"url": "https://legacy-user.example.com/mcp"}}
	}`)
	project := f.mkdir(f.path("project"))
	f.write(filepath.Join(project, "mcp.json"), `{
		"mcpServers": {"legacy-project": {"url": "https://legacy-project.example.com/mcp"}}
	}`)

	assertURL(t, Resolve(f.Env, workBuddyRequest("legacy-user", project)), "https://legacy-user.example.com/mcp")
	assertURL(t, Resolve(f.Env, workBuddyRequest("legacy-project", project)), "https://legacy-project.example.com/mcp")
}

func TestWorkBuddyProjectLocalBeatsProjectFileAndUser(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("project", "nested"))
	userPath := f.homePath(".workbuddy", ".mcp.json")
	f.write(userPath, fmt.Sprintf(`{
		"mcpServers": {"docs": {"url": "https://user.example.com/mcp"}},
		"projects": {%q: {"mcpServers": {"docs": {"url": "https://local.example.com/mcp"}}}}
	}`, project))
	f.write(filepath.Join(project, ".mcp.json"), `{
		"mcpServers": {"docs": {"url": "https://project.example.com/mcp"}}
	}`)

	res := Resolve(f.Env, workBuddyRequest("docs", filepath.Join(project, "nested")))
	assertURL(t, res, "https://local.example.com/mcp")
}

func TestWorkBuddyDuplicateLocalProjectSpellingsAreAmbiguous(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("project"))
	first := project
	second := project + string(filepath.Separator)
	f.write(f.homePath(".workbuddy", ".mcp.json"), fmt.Sprintf(`{
		"projects": {
			%q: {"mcpServers": {"docs": {"url": "https://first.example.com/mcp"}}},
			%q: {"mcpServers": {"docs": {"url": "https://second.example.com/mcp"}}}
		}
	}`, first, second))

	res := Resolve(f.Env, workBuddyRequest("docs", project))
	assertUnresolved(t, res, "conflicting definitions")
}

func TestWorkBuddyDisabledServerMasksLowerScope(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("project"))
	f.write(f.homePath(".workbuddy", ".mcp.json"), `{
		"mcpServers": {"docs": {"url": "https://user.example.com/mcp"}}
	}`)
	f.write(filepath.Join(project, ".mcp.json"), `{
		"mcpServers": {"docs": {"url": "https://project.example.com/mcp"}},
		"disabledMcpServers": ["docs"]
	}`)

	assertUnresolved(t, Resolve(f.Env, workBuddyRequest("docs", project)), "disabled")
}

func TestWorkBuddyLocalDisabledServerMasksUser(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("project")
	f.write(f.homePath(".workbuddy", ".mcp.json"), fmt.Sprintf(`{
		"mcpServers": {"docs": {"url": "https://user.example.com/mcp"}},
		"projects": {%q: {
			"mcpServers": {"docs": {"url": "https://local.example.com/mcp"}},
			"disabledMcpServers": ["docs"]
		}}
	}`, project))

	assertUnresolved(t, Resolve(f.Env, workBuddyRequest("docs", project)), "disabled")
}

func TestWorkBuddyExpandsMCPEnvironment(t *testing.T) {
	f := newFixture(t, "darwin")
	f.setenv("MCP_HOST", "mcp.example.com")
	f.setenv("TOKEN", "test-token")
	f.setenv("PACKAGE_VERSION", "2.4.6")
	f.write(f.homePath(".workbuddy", ".mcp.json"), `{
		"mcpServers": {
			"docs": {"url": "https://${MCP_HOST}/mcp?token=${TOKEN}"},
			"fallback": {"url": "https://${MISSING_HOST:-fallback.example.com}/mcp"},
			"package": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-everything@${PACKAGE_VERSION}"]},
			"missing": {"url": "https://${MISSING_HOST}/mcp"}
		}
	}`)

	assertURL(t, Resolve(f.Env, workBuddyRequest("docs", f.path("project"))), "https://mcp.example.com/mcp")
	assertURL(t, Resolve(f.Env, workBuddyRequest("fallback", f.path("project"))), "https://fallback.example.com/mcp")
	assertPackage(t, Resolve(f.Env, workBuddyRequest("package", f.path("project"))),
		"npm", "@modelcontextprotocol/server-everything", "2.4.6")
	assertUnresolved(t, Resolve(f.Env, workBuddyRequest("missing", f.path("project"))), "undefined environment variable")
}

func TestWorkBuddyProjectFileBeatsUser(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("project"))
	f.write(f.homePath(".workbuddy", ".mcp.json"), `{
		"mcpServers": {"docs": {"url": "https://user.example.com/mcp"}}
	}`)
	f.write(filepath.Join(project, ".mcp.json"), `{
		"mcpServers": {"docs": {"url": "https://project.example.com/mcp"}}
	}`)

	res := Resolve(f.Env, workBuddyRequest("docs", filepath.Join(project, "sub")))
	assertURL(t, res, "https://project.example.com/mcp")
}

func TestWorkBuddyPreservesHyphenatedServerName(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".workbuddy", ".mcp.json"), `{
		"mcpServers": {"work-rally": {"url": "https://work-rally.example.com/mcp"}}
	}`)

	res := Resolve(f.Env, workBuddyRequest("work-rally", f.path("project")))
	assertURL(t, res, "https://work-rally.example.com/mcp")
}

func TestWorkBuddyDoesNotGuessNamespaceTransform(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".workbuddy", ".mcp.json"), `{
		"mcpServers": {"my.server": {"url": "https://actual.example.com/mcp"}}
	}`)

	res := Resolve(f.Env, workBuddyRequest("my_server", f.path("project")))
	assertUnresolved(t, res, "was not found in any WorkBuddy MCP configuration")
}

func TestWorkBuddyMissingServerIsUnresolved(t *testing.T) {
	f := newFixture(t, "darwin")
	res := Resolve(f.Env, workBuddyRequest("docs", f.path("project")))
	assertUnresolved(t, res, "was not found in any WorkBuddy MCP configuration")
}

func TestNormalizeWorkBuddyPreToolUseResolvesMCP(t *testing.T) {
	f := newFixture(t, "windows")
	cwd := filepath.Join(f.Home, "probe-workspace")
	f.write(f.homePath(".workbuddy", ".mcp.json"), `{
		"mcpServers": {
			"probe-npx-stdio": {
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-everything"]
			}
		}
	}`)
	payload := []byte(`{
		"session_id": "session-workbuddy",
		"cwd": ` + string(mustJSON(cwd)) + `,
		"hook_event_name": "PreToolUse",
		"tool_name": "mcp__probe-npx-stdio__echo",
		"tool_input": {"message": "hello"},
		"tool_use_id": "tool-workbuddy"
	}`)

	call, err := normalizeCall(f.Env, localagent.WorkBuddy, EventPreToolUse, payload)
	if err != nil {
		t.Fatal(err)
	}
	if call.Request.Agent != wireAgentWorkBuddy || call.Request.Tool != "echo" || call.Request.Kind != "mcp" {
		t.Fatalf("unexpected normalized request: %+v", call.Request)
	}
	if call.Request.Unresolved {
		t.Fatalf("request was unresolved: %+v", call.Request)
	}
	if call.Request.Server.Package == nil || call.Request.Server.Package.Name != "@modelcontextprotocol/server-everything" {
		t.Fatalf("unexpected server identity: %+v", call.Request.Server)
	}
}
