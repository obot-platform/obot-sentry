package enforce

import (
	"path/filepath"
	"testing"
)

// pluginRegistry writes installed_plugins.json under configDir/plugins.
func (f *fixture) pluginRegistry(configDir, body string) {
	f.t.Helper()
	f.write(filepath.Join(configDir, "plugins", claudePluginRegistry), body)
}

// TestClaudeCodePluginServer covers a server a plugin ships. Claude Code names it
// "plugin:<plugin>:<server>", so the call arrives as plugin_<plugin>_<server> with
// either half free to contain an underscore of its own; it only matches forward.
func TestClaudeCodePluginServer(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	install := f.mkdir(f.homePath(".claude", "plugins", "cache", "mkt", "my-tool", "1.0.0"))
	f.write(filepath.Join(install, ".mcp.json"), `{"mcpServers":{"lin.ear":{"url":"https://plugin.example.com/mcp"}}}`)
	f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"my.tool@mkt":[`+
		`{"scope":"user","installPath":`+quote(install)+`}]}}`)

	res := Resolve(f.Env, claudeCodeReq("plugin_my_tool_lin_ear", project))
	assertURL(t, res, "https://plugin.example.com/mcp")
	if res.ServerName != "plugin:my.tool:lin.ear" {
		t.Fatalf("ServerName = %q, want the name Claude Code gives the server", res.ServerName)
	}

	// A name outside the plugin namespace never reaches the registry.
	res = Resolve(f.Env, claudeCodeReq("lin_ear", project))
	assertUnresolved(t, res, "was not found")
}

// TestClaudeCodePluginManifestServers covers the manifest's mcpServers in each of
// its forms, merged over the plugin's .mcp.json, and ${CLAUDE_PLUGIN_ROOT}.
func TestClaudeCodePluginManifestServers(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	install := f.mkdir(f.path("plugins", "kit"))
	f.write(filepath.Join(install, ".mcp.json"), `{"over":{"url":"https://dotfile.example.com/mcp"}}`)
	f.write(filepath.Join(install, "servers.json"), `{"mcpServers":{"fromfile":{"url":"https://file.example.com/mcp"}}}`)
	f.write(filepath.Join(install, ".claude-plugin", "plugin.json"), `{"name":"kit","mcpServers":[
		"./servers.json",
		"../escape.json",
		{"over":{"url":"https://manifest.example.com/mcp"}},
		{"bundled":{"command":"${CLAUDE_PLUGIN_ROOT}/bin/server","args":["--root","${CLAUDE_PLUGIN_ROOT}"]}},
		{"pkg":{"command":"npx","args":["-y","@acme/srv@1.2.3"]}}
	]}`)
	f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"kit@mkt":[`+
		`{"scope":"user","installPath":`+quote(install)+`}]}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("plugin_kit_fromfile", project)), "https://file.example.com/mcp")
	assertURL(t, Resolve(f.Env, claudeCodeReq("plugin_kit_over", project)), "https://manifest.example.com/mcp")
	assertPackage(t, Resolve(f.Env, claudeCodeReq("plugin_kit_pkg", project)), "npm", "@acme/srv", "1.2.3")

	// Not a package runner, so unresolved — but reporting the path it runs from
	// rather than the unexpanded variable.
	res := Resolve(f.Env, claudeCodeReq("plugin_kit_bundled", project))
	if want := install + "/bin/server"; res.Identity.Command != want {
		t.Fatalf("Command = %q, want %q\n%s", res.Identity.Command, want, resolveTrace(res))
	}
}

// TestClaudeCodePluginInlineManifest covers a manifest whose mcpServers is the
// server table itself, with no .mcp.json beside it.
func TestClaudeCodePluginInlineManifest(t *testing.T) {
	f := newFixture(t, "darwin")
	install := f.mkdir(f.path("plugins", "inline_p"))
	f.write(filepath.Join(install, ".claude-plugin", "plugin.json"),
		`{"name":"inline_p","mcpServers":{"srv":{"url":"https://inline.example.com/mcp"}}}`)
	f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"inline_p@mkt":[`+
		`{"scope":"user","installPath":`+quote(install)+`}]}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("plugin_inline_p_srv", f.path("proj"))), "https://inline.example.com/mcp")
}

// TestClaudeCodePluginInstallScopes covers which installations can serve a
// session: a project- or local-scoped one only serves the project it was
// installed for, and installations that disagree about a server leave it
// ambiguous rather than picking one.
func TestClaudeCodePluginInstallScopes(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.mkdir(f.path("proj"))
	other := f.mkdir(f.path("other"))
	v1 := f.mkdir(f.path("cache", "p", "1.0.0"))
	v2 := f.mkdir(f.path("cache", "p", "2.0.0"))
	f.write(filepath.Join(v1, ".mcp.json"), `{"s":{"url":"https://v1.example.com/mcp"}}`)
	f.write(filepath.Join(v2, ".mcp.json"), `{"s":{"url":"https://v2.example.com/mcp"}}`)
	f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"p@mkt":[`+
		`{"scope":"project","installPath":`+quote(v1)+`,"projectPath":`+quote(project)+`},`+
		`{"scope":"local","installPath":`+quote(v2)+`,"projectPath":`+quote(other)+`}]}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("plugin_p_s", project)), "https://v1.example.com/mcp")
	assertURL(t, Resolve(f.Env, claudeCodeReq("plugin_p_s", other)), "https://v2.example.com/mcp")
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq("plugin_p_s", f.mkdir(f.path("third")))), "was not found")

	f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"p@mkt":[`+
		`{"scope":"user","installPath":`+quote(v1)+`},`+
		`{"scope":"local","installPath":`+quote(v2)+`,"projectPath":`+quote(project)+`}]}}`)
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq("plugin_p_s", project)), "conflicting definitions")
	assertURL(t, Resolve(f.Env, claudeCodeReq("plugin_p_s", other)), "https://v1.example.com/mcp")
}

// TestClaudeCodePluginRegistryFollowsConfigDir covers CLAUDE_CONFIG_DIR, which
// moves the plugin registry along with .claude.json.
func TestClaudeCodePluginRegistryFollowsConfigDir(t *testing.T) {
	f := newFixture(t, "darwin")
	configDir := f.path("alt-claude")
	f.setenv(claudeConfigDirEnv, configDir)
	install := f.mkdir(f.path("cache", "p"))
	f.write(filepath.Join(install, ".mcp.json"), `{"s":{"url":"https://alt.example.com/mcp"}}`)
	f.pluginRegistry(configDir, `{"version":2,"plugins":{"p@mkt":[{"scope":"user","installPath":`+quote(install)+`}]}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("plugin_p_s", f.path("proj"))), "https://alt.example.com/mcp")
}

// TestClaudeCodePluginRanksBelowUser covers precedence: a configured server of
// the same name outranks a plugin's, which Claude Code merges first.
func TestClaudeCodePluginRanksBelowUser(t *testing.T) {
	f := newFixture(t, "darwin")
	install := f.mkdir(f.path("cache", "p"))
	f.write(filepath.Join(install, ".mcp.json"), `{"s":{"url":"https://plugin.example.com/mcp"}}`)
	f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"p@mkt":[{"scope":"user","installPath":`+quote(install)+`}]}}`)
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"plugin:p:s":{"url":"https://user.example.com/mcp"}}}`)

	assertURL(t, Resolve(f.Env, claudeCodeReq("plugin_p_s", f.path("proj"))), "https://user.example.com/mcp")
}
