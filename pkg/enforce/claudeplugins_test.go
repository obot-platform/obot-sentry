package enforce

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// pluginReq builds a request for a plugin's server, which Claude Code names
// "plugin:<plugin>:<server>".
func pluginReq(plugin, server, cwd string) ResolveRequest {
	return claudeCodeReq(claudeSourcePlugin, "plugin:"+plugin+":"+server, cwd)
}

// pluginRegistry writes installed_plugins.json under configDir/plugins, and
// turns on disableSideloadFlags in managed settings, without which no plugin
// server resolves.
func (f *fixture) pluginRegistry(configDir, body string) {
	f.t.Helper()
	f.write(filepath.Join(configDir, "plugins", claudePluginRegistry), body)
	f.write(f.Env.claudeManagedPath(claudeManagedSettings), `{"`+ClaudeDisableSideloadKey+`":true}`)
}

// TestClaudeCodePluginServer covers a server a plugin ships, which Claude Code
// reports as "plugin:<plugin>:<server>" with the plugin source.
func TestClaudeCodePluginServer(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	install := f.mkdir(f.homePath(".claude", "plugins", "cache", "mkt", "my-tool", "1.0.0"))
	f.write(filepath.Join(install, ".mcp.json"), `{"mcpServers":{"lin.ear":{"type":"http","url":"https://plugin.example.com/mcp"}}}`)
	f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"my.tool@mkt":[`+
		`{"scope":"user","installPath":`+quote(install)+`}]}}`)

	res := Resolve(f.Env, pluginReq("my.tool", "lin.ear", project))
	assertURL(t, res, "https://plugin.example.com/mcp")
	if res.ServerName != "plugin:my.tool:lin.ear" {
		t.Fatalf("ServerName = %q, want the name Claude Code gives the server", res.ServerName)
	}

	// The namespaced form names nothing: the key is matched exactly.
	assertUnresolved(t, Resolve(f.Env, claudeCodeReq(claudeSourcePlugin, "plugin_my_tool_lin_ear", project)), "did not find it there")
}

// TestClaudeCodePluginManifestServers covers the manifest's mcpServers in each of
// its forms, merged over the plugin's .mcp.json, and ${CLAUDE_PLUGIN_ROOT}.
func TestClaudeCodePluginManifestServers(t *testing.T) {
	f := newFixture(t, "darwin")
	project := f.path("proj")
	install := f.mkdir(f.path("plugins", "kit"))
	f.write(filepath.Join(install, ".mcp.json"), `{"over":{"type":"http","url":"https://dotfile.example.com/mcp"}}`)
	f.write(filepath.Join(install, "servers.json"), `{"mcpServers":{"fromfile":{"type":"http","url":"https://file.example.com/mcp"}}}`)
	f.write(filepath.Join(install, ".claude-plugin", "plugin.json"), `{"name":"kit","mcpServers":[
		"./servers.json",
		"../escape.json",
		{"over":{"type":"http","url":"https://manifest.example.com/mcp"}},
		{"bundled":{"command":"${CLAUDE_PLUGIN_ROOT}/bin/server","args":["--root","${CLAUDE_PLUGIN_ROOT}"]}},
		{"pkg":{"command":"npx","args":["-y","@acme/srv@1.2.3"]}}
	]}`)
	f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"kit@mkt":[`+
		`{"scope":"user","installPath":`+quote(install)+`}]}}`)

	assertURL(t, Resolve(f.Env, pluginReq("kit", "fromfile", project)), "https://file.example.com/mcp")
	assertURL(t, Resolve(f.Env, pluginReq("kit", "over", project)), "https://manifest.example.com/mcp")
	assertPackage(t, Resolve(f.Env, pluginReq("kit", "pkg", project)), "npm", "@acme/srv", "1.2.3")

	// Not a package runner, so unresolved — but reporting the path it runs from
	// rather than the unexpanded variable.
	res := Resolve(f.Env, pluginReq("kit", "bundled", project))
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
		`{"name":"inline_p","mcpServers":{"srv":{"type":"http","url":"https://inline.example.com/mcp"}}}`)
	f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"inline_p@mkt":[`+
		`{"scope":"user","installPath":`+quote(install)+`}]}}`)

	assertURL(t, Resolve(f.Env, pluginReq("inline_p", "srv", f.path("proj"))), "https://inline.example.com/mcp")
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
	f.write(filepath.Join(v1, ".mcp.json"), `{"s":{"type":"http","url":"https://v1.example.com/mcp"}}`)
	f.write(filepath.Join(v2, ".mcp.json"), `{"s":{"type":"http","url":"https://v2.example.com/mcp"}}`)
	f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"p@mkt":[`+
		`{"scope":"project","installPath":`+quote(v1)+`,"projectPath":`+quote(project)+`},`+
		`{"scope":"local","installPath":`+quote(v2)+`,"projectPath":`+quote(other)+`}]}}`)

	assertURL(t, Resolve(f.Env, pluginReq("p", "s", project)), "https://v1.example.com/mcp")
	assertURL(t, Resolve(f.Env, pluginReq("p", "s", other)), "https://v2.example.com/mcp")
	assertUnresolved(t, Resolve(f.Env, pluginReq("p", "s", f.mkdir(f.path("third")))), "did not find it there")

	f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"p@mkt":[`+
		`{"scope":"user","installPath":`+quote(v1)+`},`+
		`{"scope":"local","installPath":`+quote(v2)+`,"projectPath":`+quote(project)+`}]}}`)
	assertUnresolved(t, Resolve(f.Env, pluginReq("p", "s", project)), "conflicting definitions")
	assertURL(t, Resolve(f.Env, pluginReq("p", "s", other)), "https://v1.example.com/mcp")
}

// TestClaudeCodePluginRegistryFollowsConfigDir covers CLAUDE_CONFIG_DIR, which
// moves the plugin registry along with .claude.json.
func TestClaudeCodePluginRegistryFollowsConfigDir(t *testing.T) {
	f := newFixture(t, "darwin")
	configDir := f.path("alt-claude")
	f.setenv(claudeConfigDirEnv, configDir)
	install := f.mkdir(f.path("cache", "p"))
	f.write(filepath.Join(install, ".mcp.json"), `{"s":{"type":"http","url":"https://alt.example.com/mcp"}}`)
	f.pluginRegistry(configDir, `{"version":2,"plugins":{"p@mkt":[{"scope":"user","installPath":`+quote(install)+`}]}}`)

	assertURL(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "https://alt.example.com/mcp")
}

// TestClaudeCodePluginSourceReadsOnlyPlugins covers a user server named like a
// plugin's: the plugin source is the plugin's definition, whatever else holds the
// name.
func TestClaudeCodePluginSourceReadsOnlyPlugins(t *testing.T) {
	f := newFixture(t, "darwin")
	install := f.mkdir(f.path("cache", "p"))
	f.write(filepath.Join(install, ".mcp.json"), `{"s":{"type":"http","url":"https://plugin.example.com/mcp"}}`)
	f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"p@mkt":[{"scope":"user","installPath":`+quote(install)+`}]}}`)
	f.write(f.homePath(".claude.json"), `{"mcpServers":{"plugin:p:s":{"type":"http","url":"https://user.example.com/mcp"}}}`)

	assertURL(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "https://plugin.example.com/mcp")
	assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceUser, "plugin:p:s", f.path("proj"))), "https://user.example.com/mcp")
}

// TestClaudeCodeLocalMarketplacePlugin covers a plugin from a marketplace added
// from a local folder or catalog file. Claude Code reads the catalog afresh and
// takes the first entry for the plugin: a "./" source, or a bare name under
// metadata.pluginRoot, loads in place from the marketplace folder; any other
// source loads from the installed copy. Each case was verified against Claude
// Code.
func TestClaudeCodeLocalMarketplacePlugin(t *testing.T) {
	const (
		live   = "https://live.example.com/mcp"
		root   = "https://root.example.com/mcp"
		bare   = "https://bare.example.com/mcp"
		cached = "https://cached.example.com/mcp"
		none   = ""
	)
	cases := []struct{ name, catalog, want string }{
		{"dot-slash path", `"plugins":[{"name":"p","source":"./plugins/p"}]`, live},
		{"trailing slash", `"plugins":[{"name":"p","source":"./plugins/p/"}]`, live},
		{"doubled slash", `"plugins":[{"name":"p","source":".//plugins/p"}]`, live},
		{"marketplace root", `"plugins":[{"name":"p","source":"./"}]`, root},
		{"bare dot", `"plugins":[{"name":"p","source":"."}]`, root},
		{"first entry wins", `"plugins":[{"name":"p","source":"./plugins/p"},{"name":"p","source":"./p"}]`, live},
		{"path without dot-slash", `"plugins":[{"name":"p","source":"plugins/p"}]`, cached},
		{"escaping path", `"plugins":[{"name":"p","source":"../p"}]`, cached},
		{"absolute path", `"plugins":[{"name":"p","source":"/abs"}]`, cached},
		{"bare name without root", `"plugins":[{"name":"p","source":"p"}]`, cached},
		{"remote source", `"plugins":[{"name":"p","source":{"source":"github","repo":"acme/p"}}]`, cached},
		{"non-string source", `"plugins":[{"name":"p","source":7}]`, cached},
		{"dot-dot segment", `"plugins":[{"name":"p","source":"./plugins/../p"}]`, none},
		{"dot segment", `"plugins":[{"name":"p","source":"././p"}]`, none},
		{"not listed", `"plugins":[{"name":"q","source":"./plugins/p"}]`, none},
		{"plugin root", `"metadata":{"pluginRoot":"./plugins"},"plugins":[{"name":"p","source":"p"}]`, live},
		{"plugin root without dot-slash", `"metadata":{"pluginRoot":"plugins/"},"plugins":[{"name":"p","source":"p"}]`, live},
		{"plugin root at the marketplace", `"metadata":{"pluginRoot":"./"},"plugins":[{"name":"p","source":"p"}]`, bare},
		{"plugin root leaves dot-slash alone", `"metadata":{"pluginRoot":"./plugins"},"plugins":[{"name":"p","source":"./p"}]`, bare},
		{"plugin root leaves paths alone", `"metadata":{"pluginRoot":"./plugins"},"plugins":[{"name":"p","source":"plugins/p"}]`, cached},
		{"escaping plugin root", `"metadata":{"pluginRoot":"../x"},"plugins":[{"name":"p","source":"p"}]`, cached},
		{"plugin root with dot-dot", `"metadata":{"pluginRoot":"./plugins/../plugins"},"plugins":[{"name":"p","source":"p"}]`, cached},
		{"non-string plugin root", `"metadata":{"pluginRoot":7},"plugins":[{"name":"p","source":"p"}]`, none},
		{"missing plugin root", `"metadata":{"pluginRoot":"./nonexist"},"plugins":[{"name":"p","source":"p"}]`, none},
		{"catalog Claude Code cannot parse", `"plugins":[{"name":"p","source":"./plugins/p"}],`, none},
	}
	for _, kind := range []string{"directory", "file"} {
		for _, tc := range cases {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				f := newFixture(t, "darwin")
				market := f.mkdir(f.path("mkt"))
				catalog := filepath.Join(market, ".claude-plugin", "marketplace.json")
				source := `{"source":"directory","path":` + quote(market) + `}`
				if kind == "file" {
					source = `{"source":"file","path":` + quote(catalog) + `}`
				}
				f.write(f.homePath(".claude", "plugins", claudeMarketplaceRegistry),
					`{"mkt":{"source":`+source+`,"installLocation":`+quote(market)+`}}`)
				cachedDir := f.mkdir(f.homePath(".claude", "plugins", "cache", "mkt", "p", "1.0.0"))
				f.write(filepath.Join(cachedDir, ".mcp.json"), `{"s":{"type":"http","url":"`+cached+`"}}`)
				f.write(filepath.Join(market, "plugins", "p", ".mcp.json"), `{"s":{"type":"http","url":"`+live+`"}}`)
				f.write(filepath.Join(market, "p", ".mcp.json"), `{"s":{"type":"http","url":"`+bare+`"}}`)
				f.write(filepath.Join(market, ".mcp.json"), `{"s":{"type":"http","url":"`+root+`"}}`)
				f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"p@mkt":[{"scope":"user","installPath":`+quote(cachedDir)+`}]}}`)
				f.write(catalog, `{"name":"mkt",`+tc.catalog+`}`)

				res := Resolve(f.Env, pluginReq("p", "s", f.path("proj")))
				if tc.want == none {
					assertUnresolved(t, res, "did not find it there")
					return
				}
				assertURL(t, res, tc.want)
			})
		}
	}
}

// TestClaudeCodePluginFilesAreStrictJSON covers a plugin server file Claude Code
// cannot parse: it loads none of that file's servers.
func TestClaudeCodePluginFilesAreStrictJSON(t *testing.T) {
	f := newFixture(t, "darwin")
	install := f.mkdir(f.path("cache", "p"))
	f.write(filepath.Join(install, ".mcp.json"), `{"s":{"type":"http","url":"https://plugin.example.com/mcp"},}`)
	f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"p@mkt":[{"scope":"user","installPath":`+quote(install)+`}]}}`)

	assertUnresolved(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "did not find it there")
}

// TestClaudeCodePluginBoundsFailClosed covers the resolver's bounds on plugin
// installations and manifest server files, which Claude Code does not share. A
// later file or installation can redefine a server, so past either bound the
// server is unresolved rather than answered from what was read.
func TestClaudeCodePluginBoundsFailClosed(t *testing.T) {
	t.Run("server files", func(t *testing.T) {
		f := newFixture(t, "darwin")
		install := f.mkdir(f.path("cache", "p"))
		var refs []string
		for i := range maxClaudePluginServerFiles + 1 {
			name := fmt.Sprintf("./s%02d.json", i)
			refs = append(refs, quote(name))
			url := "https://allowed.example.com/mcp"
			if i == maxClaudePluginServerFiles {
				url = "https://override.example.com/mcp"
			}
			f.write(filepath.Join(install, name), `{"s":{"type":"http","url":"`+url+`"}}`)
		}
		f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"p@mkt":[{"scope":"user","installPath":`+quote(install)+`}]}}`)
		manifest := filepath.Join(install, ".claude-plugin", "plugin.json")

		f.write(manifest, `{"name":"p","mcpServers":[`+strings.Join(refs[:maxClaudePluginServerFiles], ",")+`]}`)
		assertURL(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "https://allowed.example.com/mcp")

		f.write(manifest, `{"name":"p","mcpServers":[`+strings.Join(refs, ",")+`]}`)
		assertUnresolved(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "more installations or server files than the hook reads")

		// Another plugin cannot define p's servers, so its bounds do not matter to
		// them, and p's do not matter to it.
		other := f.mkdir(f.path("cache", "q"))
		f.write(filepath.Join(other, ".mcp.json"), `{"s":{"type":"http","url":"https://other.example.com/mcp"}}`)
		f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{`+
			`"p@mkt":[{"scope":"user","installPath":`+quote(install)+`}],`+
			`"q@mkt":[{"scope":"user","installPath":`+quote(other)+`}]}}`)
		assertURL(t, Resolve(f.Env, pluginReq("q", "s", f.path("proj"))), "https://other.example.com/mcp")
	})

	t.Run("installations", func(t *testing.T) {
		f := newFixture(t, "darwin")
		install := f.mkdir(f.path("cache", "p"))
		f.write(filepath.Join(install, ".mcp.json"), `{"s":{"type":"http","url":"https://allowed.example.com/mcp"}}`)
		entry := `{"scope":"user","installPath":` + quote(install) + `}`
		registry := func(n int) string {
			return `{"version":2,"plugins":{"p@mkt":[` + strings.TrimSuffix(strings.Repeat(entry+",", n), ",") + `]}}`
		}

		f.pluginRegistry(f.homePath(".claude"), registry(maxClaudePluginInstalls))
		assertURL(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "https://allowed.example.com/mcp")

		f.pluginRegistry(f.homePath(".claude"), registry(maxClaudePluginInstalls+1))
		assertUnresolved(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "more installations or server files than the hook reads")

		// Installations of other plugins do not count toward p's bound.
		other := `"q@mkt":[` + strings.TrimSuffix(strings.Repeat(entry+",", maxClaudePluginInstalls), ",") + `]`
		f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{`+other+`,`+
			`"p@mkt":[`+entry+`]}}`)
		assertURL(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "https://allowed.example.com/mcp")
	})
}

// TestClaudeCodePluginsNeedSideloadDisabled covers plugins loaded for one session
// (--plugin-dir, --plugin-dir-no-mcp, --plugin-url, CLAUDE_CODE_PLUGIN_DIRS, an
// Agent SDK host's initialize plugins). Their servers report the same name and
// source as an installed plugin's, and one named like an installed plugin
// replaces it, so plugin servers resolve only while managed settings set
// disableSideloadFlags, which makes Claude Code refuse all of them.
func TestClaudeCodePluginsNeedSideloadDisabled(t *testing.T) {
	setup := func(t *testing.T) *fixture {
		f := newFixture(t, "darwin")
		install := f.mkdir(f.path("cache", "p"))
		f.write(filepath.Join(install, ".mcp.json"), `{"s":{"type":"http","url":"https://installed.example.com/mcp"}}`)
		f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"p@mkt":[{"scope":"user","installPath":`+quote(install)+`}]}}`)
		return f
	}
	const unset = "do not set " + ClaudeDisableSideloadKey

	f := setup(t)
	assertURL(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "https://installed.example.com/mcp")

	for name, tc := range map[string]struct{ settings, dropIn string }{
		"absent":                  {settings: `{}`},
		"false":                   {settings: `{"disableSideloadFlags":false}`},
		"not a boolean":           {settings: `{"disableSideloadFlags":"true"}`},
		"other case":              {settings: `{"DisableSideloadFlags":true}`},
		"overridden by a drop-in": {settings: `{"disableSideloadFlags":true}`, dropIn: `{"disableSideloadFlags":false}`},
		"unparseable":             {settings: `{"disableSideloadFlags":true,}`},
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			f.write(f.Env.claudeManagedPath(claudeManagedSettings), tc.settings)
			if tc.dropIn != "" {
				f.write(f.Env.claudeManagedPath(claudeManagedDropInDir, "10.json"), tc.dropIn)
			}
			assertUnresolved(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), unset)
		})
	}

	t.Run("set by a drop-in", func(t *testing.T) {
		f := setup(t)
		f.write(f.Env.claudeManagedPath(claudeManagedSettings), `{"disableSideloadFlags":false}`)
		f.write(f.Env.claudeManagedPath(claudeManagedDropInDir, "10.json"), `{"disableSideloadFlags":true}`)
		assertURL(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "https://installed.example.com/mcp")
	})

	// Claude Code honors the setting only from managed settings.
	t.Run("user settings", func(t *testing.T) {
		f := setup(t)
		f.write(f.Env.claudeManagedPath(claudeManagedSettings), `{}`)
		f.write(f.homePath(".claude", "settings.json"), `{"disableSideloadFlags":true}`)
		assertUnresolved(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), unset)
	})

	t.Run("drop-ins not read in full", func(t *testing.T) {
		f := setup(t)
		for i := range maxClaudeManagedDropIns + 1 {
			f.write(f.Env.claudeManagedPath(claudeManagedDropInDir, fmt.Sprintf("%04d.json", i)), `{"disableSideloadFlags":true}`)
		}
		assertUnresolved(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), unset)
	})
}
