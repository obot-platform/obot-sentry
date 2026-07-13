package scan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/obot-platform/obot/apiclient/types"
)

// runScanPlatform is runScan against a home laid out for the given
// platform.
func runScanPlatform(t *testing.T, platform string, files map[string]string) types.DeviceScanManifest {
	t.Helper()
	root := mapRoot(files)
	root.Platform = platform
	return runScanRoots(t, []Root{root})
}

// findServer returns the first MCP server matching client+name, or nil.
func findServer(manifest types.DeviceScanManifest, client, name string) *types.DeviceScanMCPServer {
	for i, m := range manifest.MCPServers {
		if m.Client == client && m.Name == name {
			return &manifest.MCPServers[i]
		}
	}
	return nil
}

// TestScanners_Smoke covers each scanner with one happy-path config
// (stdio or http, whichever is most natural) per platform layout and
// asserts the server is emitted with the expected client + transport.
// The orchestrator, walker, build(), and per-scanner toServer logic are
// all exercised.
func TestScanners_Smoke(t *testing.T) {
	cases := []struct {
		name      string
		platform  string
		client    string
		serverNm  string
		transport string
		files     map[string]string
	}{
		{
			name:      "claude_code stdio",
			client:    "claude_code",
			serverNm:  "github",
			transport: "stdio",
			files: map[string]string{
				".claude.json": `{"mcpServers":{"github":{"command":"npx","args":["-y","@modelcontextprotocol/server-github"]}}}`,
			},
		},
		{
			name:      "claude_desktop stdio darwin",
			client:    "claude_desktop",
			serverNm:  "github",
			transport: "stdio",
			files: map[string]string{
				"Library/Application Support/Claude/claude_desktop_config.json": `{"mcpServers":{"github":{"command":"npx","args":["-y","x"]}}}`,
			},
		},
		{
			name:      "claude_desktop stdio windows",
			platform:  "windows",
			client:    "claude_desktop",
			serverNm:  "github",
			transport: "stdio",
			files: map[string]string{
				"AppData/Roaming/Claude/claude_desktop_config.json": `{"mcpServers":{"github":{"command":"npx","args":["-y","x"]}}}`,
			},
		},
		{
			name:      "codex stdio",
			client:    "codex",
			serverNm:  "github",
			transport: "stdio",
			files: map[string]string{
				".codex/config.toml": "[mcp_servers.github]\ncommand = \"npx\"\nargs = [\"-y\", \"x\"]\n",
			},
		},
		{
			name:      "cursor stdio",
			client:    "cursor",
			serverNm:  "github",
			transport: "stdio",
			files: map[string]string{
				".cursor/mcp.json": `{"mcpServers":{"github":{"command":"npx","args":["-y","x"]}}}`,
			},
		},
		{
			name:      "goose stdio",
			client:    "goose",
			serverNm:  "github",
			transport: "stdio",
			files: map[string]string{
				".config/goose/config.yaml": "extensions:\n  github:\n    type: stdio\n    cmd: npx\n    args: [\"-y\", \"x\"]\n    enabled: true\n",
			},
		},
		{
			name:      "goose stdio windows",
			platform:  "windows",
			client:    "goose",
			serverNm:  "github",
			transport: "stdio",
			files: map[string]string{
				"AppData/Roaming/Block/goose/config/config.yaml": "extensions:\n  github:\n    type: stdio\n    cmd: npx\n    args: [\"-y\", \"x\"]\n    enabled: true\n",
			},
		},
		{
			name:      "hermes http",
			client:    "hermes",
			serverNm:  "remote",
			transport: "streamable-http",
			files: map[string]string{
				".hermes/config.yaml": "mcp_servers:\n  remote:\n    url: https://mcp.example.com/mcp\n",
			},
		},
		{
			name:      "opencode local",
			client:    "opencode",
			serverNm:  "github",
			transport: "stdio",
			files: map[string]string{
				".config/opencode/opencode.json": `{"mcp":{"github":{"type":"local","command":["npx","-y","x"]}}}`,
			},
		},
		{
			name:      "vscode stdio darwin",
			client:    "vscode",
			serverNm:  "github",
			transport: "stdio",
			files: map[string]string{
				"Library/Application Support/Code/User/mcp.json": `{"servers":{"github":{"command":"npx","args":["-y","x"]}}}`,
			},
		},
		{
			name:      "vscode stdio linux",
			platform:  "linux",
			client:    "vscode",
			serverNm:  "github",
			transport: "stdio",
			files: map[string]string{
				".config/Code/User/mcp.json": `{"servers":{"github":{"command":"npx","args":["-y","x"]}}}`,
			},
		},
		{
			name:      "vscode stdio windows",
			platform:  "windows",
			client:    "vscode",
			serverNm:  "github",
			transport: "stdio",
			files: map[string]string{
				"AppData/Roaming/Code/User/mcp.json": `{"servers":{"github":{"command":"npx","args":["-y","x"]}}}`,
			},
		},
		{
			name:      "windsurf stdio",
			client:    "windsurf",
			serverNm:  "github",
			transport: "stdio",
			files: map[string]string{
				".codeium/windsurf/mcp_config.json": `{"mcpServers":{"github":{"command":"npx","args":["-y","x"]}}}`,
			},
		},
		{
			name:      "zed stdio",
			client:    "zed",
			serverNm:  "github",
			transport: "stdio",
			files: map[string]string{
				".config/zed/settings.json": `{"context_servers":{"github":{"command":"npx","args":["-y","x"]}}}`,
			},
		},
		{
			name:      "zed stdio windows",
			platform:  "windows",
			client:    "zed",
			serverNm:  "github",
			transport: "stdio",
			files: map[string]string{
				"AppData/Roaming/Zed/settings.json": `{"context_servers":{"github":{"command":"npx","args":["-y","x"]}}}`,
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			platform := c.platform
			if platform == "" {
				platform = "darwin"
			}
			manifest := runScanPlatform(t, platform, c.files)
			s := findServer(manifest, c.client, c.serverNm)
			if s == nil {
				t.Fatalf("no server emitted for client=%q name=%q; got %+v", c.client, c.serverNm, manifest.MCPServers)
			}
			if s.Transport != c.transport {
				t.Errorf("Transport = %q, want %q", s.Transport, c.transport)
			}
			if s.ConfigHash == "" {
				t.Errorf("ConfigHash empty")
			}
			// build() must synthesize a clients[] row whenever an
			// observation references a client, even if presence didn't
			// fire in the test environment.
			client := findClient(manifest, c.client)
			if client == nil {
				t.Fatalf("no clients[] row synthesized for %q", c.client)
			}
			if !client.HasMCPServers {
				t.Errorf("HasMCPServers = false for client %q", c.client)
			}
		})
	}
}

// TestScan_PlatformLayoutRespected: a config at another platform's
// location must not be picked up (macOS layout scanned as linux).
func TestScan_PlatformLayoutRespected(t *testing.T) {
	manifest := runScanPlatform(t, "linux", map[string]string{
		"Library/Application Support/Code/User/mcp.json":                `{"servers":{"github":{"command":"npx"}}}`,
		"Library/Application Support/Claude/claude_desktop_config.json": `{"mcpServers":{"github":{"command":"npx"}}}`,
	})
	if len(manifest.MCPServers) != 0 {
		t.Errorf("darwin-layout configs scanned under linux platform: %+v", manifest.MCPServers)
	}
}

// TestScan_DisabledServerSkipped covers the rule that an explicit
// `enabled = false` removes a server from the output. Codex (TOML) and
// cursor (generic JSON path) are exercised here; hermes below covers
// the YAML path; goose inverts the default (must be explicit true).
func TestScan_DisabledServerSkipped(t *testing.T) {
	manifest := runScan(t, map[string]string{
		".codex/config.toml": "[mcp_servers.on]\ncommand = \"x\"\n\n[mcp_servers.off]\ncommand = \"y\"\nenabled = false\n",
		".cursor/mcp.json":   `{"mcpServers":{"on":{"command":"x"},"off":{"command":"y","enabled":false}}}`,
	})
	for _, client := range []string{"codex", "cursor"} {
		if findServer(manifest, client, "off") != nil {
			t.Errorf("%s: disabled server emitted", client)
		}
		if findServer(manifest, client, "on") == nil {
			t.Errorf("%s: enabled server missing", client)
		}
	}
}

// TestScan_JSONConfigNoise: real-world mcp.json files carry non-server
// top-level keys (VS Code's documented `inputs` array, `$schema`) and
// JSONC syntax; neither may poison the parse.
func TestScan_JSONConfigNoise(t *testing.T) {
	manifest := runScan(t, map[string]string{
		"Library/Application Support/Code/User/mcp.json": `{
			"inputs": [{"id": "token", "type": "promptString"}],
			"servers": {"github": {"command": "npx"}}, // comment
		}`,
		".config/opencode/opencode.jsonc": `{
			// opencode config
			"mcp": {"search": {"type": "remote", "url": "https://mcp.example.com"}},
		}`,
	})
	if findServer(manifest, "vscode", "github") == nil {
		t.Errorf("vscode servers dropped by non-server top-level keys: %+v", manifest.MCPServers)
	}
	if findServer(manifest, "opencode", "search") == nil {
		t.Errorf("opencode.jsonc servers dropped: %+v", manifest.MCPServers)
	}
}

// TestScan_PluginTreesClaimed: marketplace clones and version caches
// under ~/.claude/plugins must not leak observations through the walk —
// only registry-installed plugins count.
func TestScan_PluginTreesClaimed(t *testing.T) {
	manifest := runScan(t, map[string]string{
		".claude/plugins/marketplaces/acme/plugins/foo/.mcp.json":         `{"mcpServers":{"phantom":{"command":"x"}}}`,
		".claude/plugins/marketplaces/acme/plugins/foo/skills/s/SKILL.md": namedSkill("phantom-skill"),
	})
	if findServer(manifest, "claude_code", "phantom") != nil {
		t.Errorf("uninstalled marketplace plugin emitted an MCP server")
	}
	if len(manifest.Skills) != 0 {
		t.Errorf("uninstalled marketplace plugin emitted skills: %+v", manifest.Skills)
	}
}

func TestCompareVersionNames(t *testing.T) {
	cases := []struct {
		versions []string
		want     string
	}{
		{[]string{"2.1", "2.1.5"}, "2.1.5"},
		{[]string{"1.2.3", "1.10.0", "1.9.9"}, "1.10.0"},
		{[]string{"1.2.3-rc1", "1.2.3"}, "1.2.3"},
		{[]string{"1.2.3-rc1", "1.2.3-rc2"}, "1.2.3-rc2"},
		{[]string{"1.2.3-alpha", "1.2.3-beta"}, "1.2.3-beta"},
		{[]string{"1.2.3-alpha", "1.2.3-alpha.1"}, "1.2.3-alpha.1"},
		{[]string{"0.9", "0.10"}, "0.10"},
	}
	for _, c := range cases {
		got := slices.MaxFunc(c.versions, compareVersionNames)
		if got != c.want {
			t.Errorf("max(%v) = %q, want %q", c.versions, got, c.want)
		}
	}
}

// TestScan_ProjectScopeWalk verifies the walker dispatches a
// project-scope config to its owning scanner with the project root
// resolved correctly.
func TestScan_ProjectScopeWalk(t *testing.T) {
	manifest := runScan(t, map[string]string{
		"projects/foo/.cursor/mcp.json": `{"mcpServers":{"github":{"command":"npx"}}}`,
	})
	s := findServer(manifest, "cursor", "github")
	if s == nil {
		t.Fatalf("no project-scope server emitted; got %+v", manifest.MCPServers)
	}
	if want := filepath.Join("/home/test", "projects", "foo"); s.ProjectPath != want {
		t.Errorf("ProjectPath = %q, want %q", s.ProjectPath, want)
	}
}

// TestScanHermes covers the Hermes YAML shape end to end: stdio
// command/env extraction, streamable-http detection with sorted header
// keys, enabled semantics, settings-only stub skipping, and config file
// capture even when no servers parse.
func TestScanHermes(t *testing.T) {
	manifest := runScan(t, map[string]string{
		".hermes/config.yaml": `
mcp_servers:
  github:
    command: npx
    args: ["-y", "@modelcontextprotocol/server-github"]
    env:
      GITHUB_PERSONAL_ACCESS_TOKEN: secret
  remote:
    url: https://mcp.example.com/mcp
    headers:
      X-Tenant: acme
      Authorization: "Bearer xxx"
  off:
    command: npx
    enabled: false
  empty:
    timeout: 30
`,
	})

	github := findServer(manifest, "hermes", "github")
	if github == nil {
		t.Fatalf("github server missing: %+v", manifest.MCPServers)
	}
	if github.Transport != "stdio" || github.Command != "npx" || len(github.Args) != 2 {
		t.Errorf("unexpected stdio server: %+v", github)
	}
	if len(github.EnvKeys) != 1 || github.EnvKeys[0] != "GITHUB_PERSONAL_ACCESS_TOKEN" {
		t.Errorf("env keys wrong: %+v", github.EnvKeys)
	}
	if want := filepath.Join("/home/test", ".hermes", "config.yaml"); github.File != want {
		t.Errorf("File = %q, want %q", github.File, want)
	}

	remote := findServer(manifest, "hermes", "remote")
	if remote == nil {
		t.Fatalf("remote server missing")
	}
	if remote.Transport != "streamable-http" || remote.URL != "https://mcp.example.com/mcp" {
		t.Errorf("unexpected remote server: %+v", remote)
	}
	if len(remote.HeaderKeys) != 2 || remote.HeaderKeys[0] != "Authorization" || remote.HeaderKeys[1] != "X-Tenant" {
		t.Errorf("header keys wrong (expect sorted): %+v", remote.HeaderKeys)
	}

	if findServer(manifest, "hermes", "off") != nil {
		t.Errorf("enabled:false server emitted")
	}
	if findServer(manifest, "hermes", "empty") != nil {
		t.Errorf("settings-only stub emitted")
	}
}

func TestScanHermes_ConfigRecordedWithoutServers(t *testing.T) {
	manifest := runScan(t, map[string]string{
		".hermes/config.yaml": "other_section:\n  foo: bar\n",
	})
	if len(manifest.MCPServers) != 0 {
		t.Fatalf("expected 0 servers, got %+v", manifest.MCPServers)
	}
	want := filepath.Join("/home/test", ".hermes", "config.yaml")
	var found bool
	for _, f := range manifest.Files {
		if f.Path == want {
			found = true
		}
	}
	if !found {
		t.Errorf("config file not recorded, files=%+v", manifest.Files)
	}
}

// TestScanClaudeCodePlugins covers the installed-plugins registry path:
// enabled resolution from settings.json, ${CLAUDE_PLUGIN_ROOT}
// substitution in nested MCP servers, nested skill attribution, and
// resolving the registry's absolute install path back into the scanned
// root.
func TestScanClaudeCodePlugins(t *testing.T) {
	installDir := ".claude/plugins/cache/acme/tools"
	manifest := runScan(t, map[string]string{
		".claude/plugins/installed_plugins.json":   `{"plugins":{"tools@acme":[{"installPath":"/home/test/` + installDir + `","version":"2.1.0"}]}}`,
		".claude/settings.json":                    `{"enabledPlugins":{"tools@acme":true}}`,
		installDir + "/.claude-plugin/plugin.json": `{"name":"tools","description":"Handy tools","author":{"name":"Acme"}}`,
		installDir + "/mcp.json":                   `{"mcpServers":{"runner":{"command":"${CLAUDE_PLUGIN_ROOT}/bin/run","args":["--root","${CLAUDE_PLUGIN_ROOT}"]}}}`,
		installDir + "/skills/helper/SKILL.md":     namedSkill("helper"),
	})

	if len(manifest.Plugins) != 1 {
		t.Fatalf("want 1 plugin, got %+v", manifest.Plugins)
	}
	plugin := manifest.Plugins[0]
	if plugin.Client != "claude_code" || plugin.Name != "tools" || plugin.Marketplace != "acme" {
		t.Errorf("unexpected plugin: %+v", plugin)
	}
	if plugin.Version != "2.1.0" {
		t.Errorf("Version = %q, want fallback from registry", plugin.Version)
	}
	if !plugin.Enabled || !plugin.HasMCPServers || !plugin.HasSkills {
		t.Errorf("plugin flags wrong: %+v", plugin)
	}

	runner := findServer(manifest, "claude_code", "runner")
	if runner == nil {
		t.Fatalf("nested MCP server missing: %+v", manifest.MCPServers)
	}
	if want := "/home/test/" + installDir + "/bin/run"; runner.Command != want {
		t.Errorf("Command = %q, want %q (plugin root substituted)", runner.Command, want)
	}
	if len(runner.Args) != 2 || runner.Args[1] != "/home/test/"+installDir {
		t.Errorf("Args = %v (plugin root substituted)", runner.Args)
	}

	if got := skillClients(manifest, installDir+"/skills/helper/SKILL.md"); len(got) != 1 || got[0] != "claude_code" {
		t.Errorf("nested skill clients = %v, want [claude_code]", got)
	}
}

// TestClaudeDesktopSkillsPlugin covers the Cowork skills-plugin scan:
// two snapshots of the same plugin-uuid (older and newer
// manifest.json:lastUpdated) plus an ephemeral session sibling. The
// active snapshot wins; the ephemeral sibling is ignored entirely.
//
// The skills-plugin bundle is intentionally NOT surfaced as a plugin
// row — it's a delivery vehicle for bundled skills, not a plugin in the
// sense of MCP servers/commands/hooks. Only skill rows are emitted;
// real plugins come through the rpm scan.
func TestClaudeDesktopSkillsPlugin(t *testing.T) {
	appDir := "Library/Application Support/Claude"
	skillsPluginDir := appDir + "/local-agent-mode-sessions/skills-plugin"
	installA := skillsPluginDir + "/install-a/plugin-x"
	installB := skillsPluginDir + "/install-b/plugin-x"
	ephemeralDir := appDir + "/local-agent-mode-sessions/session-uuid/inner-uuid/local_abc"

	pluginManifest := `{"name":"anthropic-skills","version":"1.0.0","description":"Bundled skills"}`

	manifest := runScan(t, map[string]string{
		// Newer snapshot (lastUpdated=2000).
		installA + "/.claude-plugin/plugin.json": pluginManifest,
		installA + "/manifest.json":              `{"lastUpdated":2000}`,
		installA + "/skills/foo/SKILL.md":        "---\nname: foo\ndescription: newer copy\n---\nbody\n",
		// Older snapshot of the same plugin-uuid (lastUpdated=1000).
		installB + "/.claude-plugin/plugin.json": pluginManifest,
		installB + "/manifest.json":              `{"lastUpdated":1000}`,
		installB + "/skills/foo/SKILL.md":        "---\nname: foo\ndescription: older copy\n---\nbody\n",
		// Ephemeral session sandbox — must not produce any rows.
		ephemeralDir + "/.claude/skills/bad/SKILL.md": "---\nname: bad\ndescription: ephemeral\n---\nbody\n",
	})

	if len(manifest.Plugins) != 0 {
		t.Errorf("skills bundle surfaced as plugin rows: %+v", manifest.Plugins)
	}
	if len(manifest.Skills) != 1 {
		t.Fatalf("want 1 skill, got %+v", manifest.Skills)
	}
	skill := manifest.Skills[0]
	if skill.Name != "foo" {
		t.Errorf("Name = %q", skill.Name)
	}
	if skill.Description != "newer copy" {
		t.Errorf("Description = %q; active snapshot should win", skill.Description)
	}
	if skill.Client != "claude_desktop" {
		t.Errorf("Client = %q", skill.Client)
	}
}

// TestClaudeDesktopRpmPlugin covers Cowork's RPM (server-pushed) plugin
// scan. Fixture mirrors a real install: a plugin_<id>/ with
// .claude-plugin/plugin.json, .mcp.json (HTTP transport), a nested
// skill, and a sibling rpm/manifest.json carrying the marketplace name.
// Plugin name/version/description/author come from plugin.json;
// Marketplace comes from rpm/manifest.json joined by plugin id.
func TestClaudeDesktopRpmPlugin(t *testing.T) {
	// The two UUID levels under local-agent-mode-sessions/ are opaque
	// (one is the account/user id, the other the org id, in an order
	// that has varied between Cowork versions). The scanner treats them
	// as opaque, so we use stand-in labels.
	rpmDir := "Library/Application Support/Claude/local-agent-mode-sessions/outerUUID/innerUUID/rpm"
	pluginID := "plugin_01XXJmxLXPEhPMmnxmrgntNw"
	installDir := rpmDir + "/" + pluginID

	// Note: rpm/manifest.json's plugins[].name is intentionally a decoy
	// — the scanner sources Name from .claude-plugin/plugin.json, and
	// only reads this file for marketplaceName joined by plugin id.
	rpmManifest := `{
		"lastUpdated": 1779337664941,
		"plugins": [{
			"id": "` + pluginID + `",
			"name": "design-from-rpm-manifest",
			"marketplaceId": "marketplace_01QRn9XAjzzeAokB5nPWVMxP",
			"marketplaceName": "knowledge-work-plugins",
			"installedBy": "user"
		}]
	}`

	manifest := runScan(t, map[string]string{
		rpmDir + "/manifest.json":                       rpmManifest,
		installDir + "/.claude-plugin/plugin.json":      `{"name":"design","version":"1.2.0","description":"Design workflows","author":{"name":"Anthropic"}}`,
		installDir + "/.mcp.json":                       `{"mcpServers":{"figma":{"type":"http","url":"https://mcp.figma.com/mcp"}}}`,
		installDir + "/skills/design-critique/SKILL.md": "---\nname: design-critique\ndescription: Get structured design feedback\n---\nbody\n",
	})

	if len(manifest.Plugins) != 1 {
		t.Fatalf("want 1 plugin, got %+v", manifest.Plugins)
	}
	plugin := manifest.Plugins[0]
	if plugin.Name != "design" {
		t.Errorf("Name = %q; should come from .claude-plugin/plugin.json, not rpm/manifest.json or opaque dir name", plugin.Name)
	}
	if plugin.Version != "1.2.0" || plugin.Description != "Design workflows" || plugin.Author != "Anthropic" {
		t.Errorf("plugin metadata wrong: %+v", plugin)
	}
	if plugin.Marketplace != "knowledge-work-plugins" {
		t.Errorf("Marketplace = %q; should come from rpm/manifest.json", plugin.Marketplace)
	}
	if !plugin.HasMCPServers || !plugin.HasSkills {
		t.Errorf("component flags wrong: %+v", plugin)
	}

	if len(manifest.MCPServers) != 1 {
		t.Fatalf("want 1 MCP server, got %+v", manifest.MCPServers)
	}
	server := manifest.MCPServers[0]
	if server.Name != "figma" || server.Transport != "http" || server.URL != "https://mcp.figma.com/mcp" {
		t.Errorf("unexpected server: %+v", server)
	}

	if len(manifest.Skills) != 1 || manifest.Skills[0].Name != "design-critique" {
		t.Errorf("unexpected skills: %+v", manifest.Skills)
	}
}

// runPresence exercises detectPresence against a real home directory
// and returns the detected client rows.
func runPresence(t *testing.T, home string) map[string]types.DeviceScanClient {
	t.Helper()
	clients := map[string]types.DeviceScanClient{}
	s := newState(
		Root{FS: os.DirFS(home), Path: home, Platform: runtime.GOOS, Primary: true},
		DefaultMaxDepth, map[string]types.DeviceScanFile{}, clients,
	)
	detectPresence(s)
	return clients
}

func TestPresence_NotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("OPENCLAW_PROFILE", "")
	appBundleDirs = []string{t.TempDir()}
	t.Cleanup(func() { appBundleDirs = nil })

	if clients := runPresence(t, t.TempDir()); len(clients) != 0 {
		t.Fatalf("expected no clients, got %+v", clients)
	}
}

func TestPresence_BinaryOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-style stub binary")
	}
	pathDir := t.TempDir()
	stub := filepath.Join(pathDir, "openclaw")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", pathDir)
	t.Setenv("OPENCLAW_PROFILE", "")
	appBundleDirs = []string{t.TempDir()}
	t.Cleanup(func() { appBundleDirs = nil })

	clients := runPresence(t, t.TempDir())
	c, ok := clients["openclaw"]
	if !ok {
		t.Fatalf("openclaw not detected: %+v", clients)
	}
	if c.BinaryPath != stub {
		t.Errorf("BinaryPath = %q, want %q", c.BinaryPath, stub)
	}
	if c.InstallPath != "" || c.ConfigPath != "" {
		t.Errorf("expected only BinaryPath set, got %+v", c)
	}
}

func TestPresence_ConfigDirWithProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("OPENCLAW_PROFILE", "dev")
	appBundleDirs = []string{t.TempDir()}
	t.Cleanup(func() { appBundleDirs = nil })

	profileDir := filepath.Join(home, ".openclaw-dev")
	if err := os.Mkdir(profileDir, 0o755); err != nil {
		t.Fatalf("mkdir profile: %v", err)
	}
	// Distractor: unsuffixed dir must NOT match when profile is set.
	if err := os.Mkdir(filepath.Join(home, ".openclaw"), 0o755); err != nil {
		t.Fatalf("mkdir distractor: %v", err)
	}

	clients := runPresence(t, home)
	c, ok := clients["openclaw"]
	if !ok {
		t.Fatalf("openclaw not detected: %+v", clients)
	}
	if c.ConfigPath != profileDir {
		t.Errorf("ConfigPath = %q, want %q", c.ConfigPath, profileDir)
	}
}

// TestScan_ContextCancelled: a cancelled context aborts the scan with
// its error rather than returning a partial manifest.
func TestScan_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Scan(ctx, Options{Roots: []Root{mapRoot(map[string]string{
		".hermes/config.yaml": "mcp_servers:\n  a:\n    command: x\n",
	})}})
	if err == nil {
		t.Fatalf("expected context error")
	}
}

// TestScan_Deterministic: two scans of the same tree serialize
// identically — map-backed configs must emit in sorted order.
func TestScan_Deterministic(t *testing.T) {
	files := map[string]string{
		".claude.json":                `{"mcpServers":{"zeta":{"command":"z"},"alpha":{"command":"a"},"mid":{"command":"m"}}}`,
		".cursor/mcp.json":            `{"mcpServers":{"b":{"command":"b"},"a":{"command":"a"},"c":{"command":"c"}}}`,
		".agents/skills/doc/SKILL.md": namedSkill("doc"),
	}
	first, err := json.Marshal(runScan(t, files))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	second, err := json.Marshal(runScan(t, files))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("scan output not deterministic:\n%s\n%s", first, second)
	}
}

func TestStripJSONC(t *testing.T) {
	in := `{
	// line comment
	"servers": { /* block comment */
		"github": {
			"command": "npx", // trailing note
			"args": ["-y", "x",],
			"url": "https://example.com//not-a-comment",
		},
	},
}`
	var out map[string]map[string]mcpServerSpec
	if err := json.Unmarshal(stripJSONC([]byte(in)), &out); err != nil {
		t.Fatalf("sanitized JSONC does not parse: %v", err)
	}
	e := out["servers"]["github"]
	if e.Command != "npx" || len(e.Args) != 2 {
		t.Errorf("unexpected entry: %+v", e)
	}
	if e.URL != "https://example.com//not-a-comment" {
		t.Errorf("string content mangled: %q", e.URL)
	}
}
