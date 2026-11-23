package enforce

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

func TestLoadJSONResults(t *testing.T) {
	f := newFixture(t, "darwin")

	var out map[string]any
	if got := loadJSON(f.path("missing.json"), &out); got != loadAbsent {
		t.Errorf("missing file = %v, want loadAbsent", got)
	}
	if got := loadJSON(f.write(f.path("bad.json"), `{`), &out); got != loadUnusable {
		t.Errorf("malformed file = %v, want loadUnusable", got)
	}
	if got := loadJSON(f.write(f.path("ok.json"), `{"a":1}`), &out); got != loadOK {
		t.Errorf("valid file = %v, want loadOK", got)
	}
	if got := loadJSON(f.mkdir(f.path("dir.json")), &out); got != loadUnusable {
		t.Errorf("directory = %v, want loadUnusable", got)
	}
}

func TestLoadJSONStripsBOM(t *testing.T) {
	f := newFixture(t, "darwin")
	path := f.write(f.path("bom.json"), "\xef\xbb\xbf"+`{"mcpServers":{"linear":{"url":"https://x.example.com/sse"}}}`)

	set, got := jsonServers(newConfigLoader(), path)(t.Context())
	if got != loadOK {
		t.Fatalf("load = %v, want loadOK", got)
	}
	if _, ok := set["linear"]; !ok {
		t.Fatal("the servers table did not decode")
	}
}

func TestLoadJSONRefusesOversizedFiles(t *testing.T) {
	f := newFixture(t, "darwin")
	path := f.write(f.path("huge.json"), `{"pad":"`+strings.Repeat("a", maxConfigBytes)+`"}`)

	var out map[string]any
	if got := loadJSON(path, &out); got != loadUnusable {
		t.Fatalf("oversized file = %v, want loadUnusable", got)
	}
}

func TestConfigLoaderUsesOneFileSnapshot(t *testing.T) {
	f := newFixture(t, "darwin")
	path := f.write(f.path("mcp.json"), `{"mcpServers":{"first":{"url":"https://first.example.com"}}}`)
	loader := newConfigLoader()
	load := jsonServers(loader, path)

	first, res := load(t.Context())
	if res != loadOK || first["first"].URL != "https://first.example.com" {
		t.Fatalf("first load = (%+v, %v)", first, res)
	}
	f.write(path, `{"mcpServers":{"second":{"url":"https://second.example.com"}}}`)
	second, res := load(t.Context())
	if res != loadOK {
		t.Fatalf("second load result = %v", res)
	}
	if second["first"].URL != "https://first.example.com" {
		t.Fatalf("cached snapshot changed after an on-disk edit: %+v", second)
	}
	if _, ok := second["second"]; ok {
		t.Fatalf("same invocation observed a later file version: %+v", second)
	}
}

func TestConfigLoaderCancellationDoesNotPoisonCache(t *testing.T) {
	f := newFixture(t, "darwin")
	path := f.write(f.path("mcp.json"), `{"mcpServers":{"server":{"url":"https://server.example.com"}}}`)
	load := jsonServers(newConfigLoader(), path)

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, res := load(cancelled); res != loadUnusable {
		t.Fatalf("cancelled load = %v, want loadUnusable", res)
	}

	set, res := load(t.Context())
	if res != loadOK || set["server"].URL != "https://server.example.com" {
		t.Fatalf("later active load was poisoned by cancellation: (%+v, %v)", set, res)
	}
}

func TestEnvPathFallsBackToTheConventionalLocation(t *testing.T) {
	f := newFixture(t, "windows")

	got := f.Env.envPath("ProgramData", "C:/ProgramData", "OpenAI", "Codex", "managed_config.toml")
	want := filepath.Join(f.machinePath("C:/ProgramData"), "OpenAI", "Codex", "managed_config.toml")
	if strings.Contains(strings.TrimPrefix(want, filepath.VolumeName(want)), ":") {
		t.Fatalf("fallback %q carries a drive letter below the machine root", want)
	}
	if got != want {
		t.Fatalf("envPath = %q, want %q", got, want)
	}

	f.setenv("ProgramData", f.path("ProgramData"))
	got = f.Env.envPath("ProgramData", "C:/ProgramData", "OpenAI", "Codex", "managed_config.toml")
	if want := f.path("ProgramData", "OpenAI", "Codex", "managed_config.toml"); got != want {
		t.Fatalf("envPath = %q, want %q", got, want)
	}

	f.setenv("ProgramData", "relative-program-data")
	got = f.Env.envPath("ProgramData", "C:/ProgramData", "OpenAI", "Codex", "managed_config.toml")
	if got != want {
		t.Fatalf("envPath with relative override = %q, want fallback %q", got, want)
	}
}

// TestMachinePathIsAbsoluteInProduction covers a production Env, whose
// MachineRoot is empty: a fixed machine path is handed back as the host spells
// it, with no fixture root prepended.
func TestMachinePathIsAbsoluteInProduction(t *testing.T) {
	env := Env{Home: "/Users/dev", GOOS: "darwin"}
	for _, abs := range []string{claudeManagedDirDarwin, "/etc/codex/managed_config.toml"} {
		if got, want := env.machinePath(abs), filepath.FromSlash(abs); got != want {
			t.Errorf("machinePath(%q) = %q, want %q with nothing prepended", abs, got, want)
		}
	}

	// Since nothing is prepended, what comes back is absolute wherever the path
	// given belongs. Those above are the macOS locations, which are absolute on
	// macOS; on Windows the equivalent is Claude Code's drive-letter managed
	// directory.
	native, nativeEnv := claudeManagedDirDarwin, env
	if runtime.GOOS == "windows" {
		native, nativeEnv.GOOS = claudeManagedDirWindows, "windows"
	}
	if got := nativeEnv.machinePath(native); !filepath.IsAbs(got) {
		t.Errorf("machinePath(%q) = %q, want an absolute path", native, got)
	}
}

// TestNewEnvHomeOverride covers how the environment's home directory is
// compared with the account's: only a variable naming a different directory is
// an override.
func TestNewEnvHomeOverride(t *testing.T) {
	account := t.TempDir()
	other := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(account, link); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
	fixed := func(dir string) func() (string, error) {
		return func() (string, error) { return dir, nil }
	}
	unset := func() (string, error) { return "", errors.New("$HOME is not defined") }

	for name, tc := range map[string]struct {
		environment func() (string, error)
		override    string
	}{
		"same directory":          {fixed(account), ""},
		"same directory, unclean": {fixed(account + string(filepath.Separator) + "."), ""},
		"link to the directory":   {fixed(link), ""},
		"unset":                   {unset, ""},
		"another directory":       {fixed(other), other},
		"a directory inside it":   {fixed(filepath.Join(account, "sub")), filepath.Join(account, "sub")},
		"a directory not there":   {fixed(filepath.Join(other, "missing")), filepath.Join(other, "missing")},
		"a relative directory":    {fixed("relative-home"), "relative-home"},
	} {
		t.Run(name, func(t *testing.T) {
			env, err := newEnv(fixed(account), tc.environment)
			if err != nil {
				t.Fatal(err)
			}
			if env.Home != account {
				t.Errorf("Home = %q, want the account's %q", env.Home, account)
			}
			if env.HomeOverride != tc.override {
				t.Errorf("HomeOverride = %q, want %q", env.HomeOverride, tc.override)
			}
		})
	}

	if _, err := newEnv(fixed("relative-home"), fixed(account)); err == nil || !strings.Contains(err.Error(), "not absolute") {
		t.Errorf("newEnv with a relative account home: error = %v, want a rejection", err)
	}
	if _, err := newEnv(unset, fixed(account)); err == nil {
		t.Error("newEnv with no account home succeeded")
	}
}

// TestHomeOverrideLeavesHomeConfigurationUnresolved covers a hook whose
// environment names a home directory other than the account's. The agent may
// have read either one, so nothing under the home directory is resolved, for
// any agent. A Claude Code server from a source that is not read from the home
// directory resolves as it does without an override, and so does every source
// once CLAUDE_CONFIG_DIR takes Claude Code's configuration out of it.
func TestHomeOverrideLeavesHomeConfigurationUnresolved(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	const home = "https://home.example.com/sse"
	install := f.mkdir(f.homePath(".claude", "plugins", "cache", "mkt", "tool", "1.0.0"))
	f.write(filepath.Join(install, ".mcp.json"), `{"mcpServers":{"linear":{"type":"http","url":"`+home+`"}}}`)
	f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"tool@mkt":[{"scope":"user","installPath":`+quote(install)+`}]}}`)
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"linear":{"type":"http","url":"`+home+`"}},`+
		`"projects":{`+quote(project)+`:{"mcpServers":{"mine":{"type":"http","url":"`+home+`"}}}}}`)
	f.write(f.homePath(".codex", "config.toml"), "[mcp_servers.linear]\nurl = \""+home+"\"\n")
	f.write(f.homePath(".cursor", "mcp.json"), `{"mcpServers":{"linear":{"url":"`+home+`"}}}`)
	f.write(f.homePath(".kiro", "settings", "mcp.json"), `{"mcpServers":{"linear":{"url":"`+home+`"}}}`)
	underHome := map[string]ResolveRequest{
		"claude-code user":   claudeCodeReq(claudeSourceUser, "linear", project),
		"claude-code local":  claudeCodeReq(claudeSourceLocal, "mine", project),
		"claude-code plugin": pluginReq("tool", "linear", project),
		"codex":              codexReq("linear"),
		"cursor":             cursorReq("linear", project),
		"kiro":               {Agent: localagent.Kiro, ServerName: "linear", CWD: project},
	}

	const elsewhere = "https://elsewhere.example.com/mcp"
	f.write(filepath.Join(project, ".mcp.json"), `{"mcpServers":{"proj":{"type":"http","url":"`+elsewhere+`"}}}`)
	f.write(f.Env.managedMCPPath(), `{"mcpServers":{"ent":{"type":"http","url":"`+elsewhere+`"}}}`)
	f.write(f.Env.claudeManagedPath(claudeManagedSettings),
		`{"`+ClaudeDisableSideloadKey+`":true,"managedMcpServers":{"mgd":`+managedServer(elsewhere)+`}}`)
	notUnderHome := map[string]ResolveRequest{
		"claude-code project":    claudeCodeReq(claudeSourceProject, "proj", project),
		"claude-code enterprise": claudeCodeReq(claudeSourceEnterprise, "ent", project),
		"claude-code managed":    claudeCodeReq(claudeSourceManaged, "mgd", project),
	}

	for name, req := range underHome {
		if res := Resolve(f.Env, req); res.Unresolved || res.Identity.URL != home {
			t.Fatalf("%s: without an override, unresolved=%v (%s) url=%q\n%s", name, res.Unresolved, res.Reason, res.Identity.URL, resolveTrace(res))
		}
	}

	f.Env.HomeOverride = f.path("decoy")
	for name, req := range underHome {
		res := Resolve(f.Env, req)
		if !res.Unresolved || !strings.Contains(res.Reason, "HOME set to "+strconv.Quote(f.Env.HomeOverride)) {
			t.Errorf("%s: want unresolved for the HOME override, got unresolved=%v reason=%q", name, res.Unresolved, res.Reason)
		}
		if len(res.Trace) != 0 {
			t.Errorf("%s: read configuration despite the override:\n%s", name, resolveTrace(res))
		}
	}
	for name, req := range notUnderHome {
		t.Run(name, func(t *testing.T) { assertURL(t, Resolve(f.Env, req), elsewhere) })
	}
	connector := Resolve(f.Env, claudeCodeReq(claudeSourceClaudeAI, "claude.ai Linear", project))
	if connector.Unresolved || connector.Identity.Connector != "claude.ai Linear" {
		t.Errorf("a claude.ai connector under the override: unresolved=%v (%s) connector=%q", connector.Unresolved, connector.Reason, connector.Identity.Connector)
	}

	// CLAUDE_CONFIG_DIR takes all of it out of the home directory.
	const alt = "https://alt.example.com/sse"
	configDir := f.path("alt-claude")
	f.setenv(claudeConfigDirEnv, configDir)
	altInstall := f.mkdir(filepath.Join(configDir, "plugins", "cache", "mkt", "tool", "1.0.0"))
	f.write(filepath.Join(altInstall, ".mcp.json"), `{"mcpServers":{"linear":{"type":"http","url":"`+alt+`"}}}`)
	f.write(filepath.Join(configDir, "plugins", claudePluginRegistry),
		`{"version":2,"plugins":{"tool@mkt":[{"scope":"user","installPath":`+quote(altInstall)+`}]}}`)
	f.write(filepath.Join(configDir, ".claude.json"), `{"mcpServers":{"linear":{"type":"http","url":"`+alt+`"}},`+
		`"projects":{`+quote(project)+`:{"mcpServers":{"mine":{"type":"http","url":"`+alt+`"}}}}}`)
	for name, req := range underHome {
		if req.Agent != localagent.ClaudeCode {
			assertUnresolved(t, Resolve(f.Env, req), "HOME set to")
			continue
		}
		t.Run(name+" with CLAUDE_CONFIG_DIR", func(t *testing.T) { assertURL(t, Resolve(f.Env, req), alt) })
	}

	f.Env.GOOS = "windows"
	if reason, _ := f.Env.homeOverridden(); !strings.Contains(reason, "USERPROFILE set to") {
		t.Errorf("Windows reason = %q, want it to name USERPROFILE", reason)
	}
}

// TestHomeOverrideOnEveryHookPath covers the override on the path each agent's
// hook really takes, payload to decision request. Kiro's does not go through
// resolve, so a check there alone would leave its hook reading the account's
// ~/.kiro for a Kiro that read another.
func TestHomeOverrideOnEveryHookPath(t *testing.T) {
	f := newFixture(t, "darwin")
	ws := f.mkdir(f.path("ws"))
	const home = "https://home.example.com/sse"
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"linear":{"type":"sse","url":"`+home+`"}}}`)
	f.write(f.homePath(".codex", "config.toml"), "[mcp_servers.linear]\nurl = \""+home+"\"\n")
	f.write(f.homePath(".cursor", "mcp.json"), `{"mcpServers":{"linear":{"url":"`+home+`"}}}`)
	f.write(f.homePath(".kiro", "settings", "mcp.json"), `{"mcpServers":{"linear":{"url":"`+home+`"}}}`)
	preTool := func(toolName string) []byte {
		return mustJSON(map[string]any{"session_id": "s1", "hook_event_name": "PreToolUse", "cwd": ws, "tool_name": toolName})
	}
	claudePreTool := func(toolName, server, source string) []byte {
		return mustJSON(map[string]any{
			"session_id": "s1", "hook_event_name": "PreToolUse", "cwd": ws, "tool_name": toolName,
			"mcp_server": map[string]string{"name": server, "source": source},
		})
	}
	hooks := []struct {
		name    string
		agent   localagent.Agent
		event   Event
		payload []byte
	}{
		{"claude-code", localagent.ClaudeCode, EventPreToolUse, claudePreTool("mcp__linear__search", "linear", claudeSourceUser)},
		{"codex", localagent.Codex, EventPreToolUse, preTool("mcp__linear__search")},
		{"cursor", localagent.Cursor, EventCursorBeforeMCPExecution, mustJSON(map[string]any{
			"hook_event_name": "beforeMCPExecution", "tool_name": "search", "mcp_server_name": "linear", "workspace_roots": []string{ws},
		})},
		{"kiro", localagent.Kiro, EventPreToolUse, kiroPayload("mcp_linear_search", ws, nil)},
		{"kiro tool_call", localagent.Kiro, EventPreToolUse, kiroPayload(kiroToolCall, ws, map[string]any{"tool_id": "linear::search"})},
	}
	run := func(t *testing.T, agent localagent.Agent, event Event, payload []byte) Call {
		t.Helper()
		call, err := normalizeCall(f.Env, agent, event, payload)
		if err != nil {
			t.Fatalf("normalizeCall: %v", err)
		}
		return call
	}

	for _, h := range hooks {
		if call := run(t, h.agent, h.event, h.payload); call.Request.Unresolved || call.Request.Server.URL != home {
			t.Fatalf("%s: without an override, unresolved=%v (%s) url=%q", h.name, call.Request.Unresolved, call.Request.UnresolvedReason, call.Request.Server.URL)
		}
	}

	f.Env.HomeOverride = f.path("decoy")
	for _, h := range hooks {
		call := run(t, h.agent, h.event, h.payload)
		if !call.Request.Unresolved || !strings.Contains(call.Request.UnresolvedReason, "HOME set to") {
			t.Errorf("%s: want unresolved for the HOME override, got unresolved=%v reason=%q", h.name, call.Request.Unresolved, call.Request.UnresolvedReason)
		}
		if call.Request.Server.URL != "" || len(call.Trace) != 0 {
			t.Errorf("%s: read the home directory despite the override: url=%q trace=%v", h.name, call.Request.Server.URL, call.Trace)
		}
	}

	// A built-in tool reads no configuration, and a built-in MCP server is known
	// by its name and source whichever home directory was read.
	if call := run(t, localagent.Kiro, EventPreToolUse, kiroPayload("execute_bash", ws, map[string]any{"command": "ls"})); call.Request.Unresolved {
		t.Errorf("a Kiro built-in tool was unresolved under the override: %s", call.Request.UnresolvedReason)
	}
	builtin := run(t, localagent.ClaudeCode, EventPreToolUse, claudePreTool("mcp__claude-in-chrome__navigate", "claude-in-chrome", "dynamic"))
	if builtin.Request.Unresolved || builtin.Request.ServerName != "claude-in-chrome" || builtin.Request.Server.URL != "" {
		t.Errorf("a Claude Code built-in MCP server under the override: unresolved=%v (%s) name=%q", builtin.Request.Unresolved, builtin.Request.UnresolvedReason, builtin.Request.ServerName)
	}
}
