package scan

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/obot-platform/obot/apiclient/types"
)

func findPlugin(manifest types.DeviceScanManifest, client, name string) *types.DeviceScanPlugin {
	for i, p := range manifest.Plugins {
		if p.Client == client && p.Name == name {
			return &manifest.Plugins[i]
		}
	}
	return nil
}

// TestScanKiro_Servers covers the settings file: top-level servers with
// Kiro's `disabled` flag, remote servers, the powers section legacy
// powers register into, and a workspace-scope file found by the walk.
func TestScanKiro_Servers(t *testing.T) {
	manifest := runScan(t, map[string]string{
		".kiro/settings/mcp.json": `{
			"mcpServers": {
				"fetch": {"command": "uvx", "args": ["mcp-server-fetch"], "env": {"FASTMCP_LOG_LEVEL": "ERROR"}, "autoApprove": []},
				"off": {"command": "uvx", "disabled": true},
				"remote": {"url": "https://mcp.example.com/mcp", "headers": {"Authorization": "Bearer x"}},
			},
			// Kiro writes legacy power servers here.
			"powers": {"mcpServers": {"power-figma-figma": {"url": "https://mcp.figma.com/mcp"}}}
		}`,
		"src/app/.kiro/settings/mcp.json": `{"mcpServers": {"local": {"command": "node", "args": ["server.js"]}},
			"powers": {"mcpServers": {"ignored": {"command": "x"}}}}`,
	})

	fetch := findServer(manifest, "kiro", "fetch")
	if fetch == nil {
		t.Fatalf("fetch not emitted: %+v", manifest.MCPServers)
	}
	if fetch.Transport != "stdio" || fetch.Command != "uvx" || !slices.Equal(fetch.EnvKeys, []string{"FASTMCP_LOG_LEVEL"}) {
		t.Errorf("fetch = %+v", fetch)
	}
	if findServer(manifest, "kiro", "off") != nil {
		t.Errorf("disabled server emitted")
	}
	// Untyped remotes are Streamable HTTP, which Kiro tries first.
	if r := findServer(manifest, "kiro", "remote"); r == nil || r.URL != "https://mcp.example.com/mcp" ||
		r.Transport != "streamable-http" || !slices.Equal(r.HeaderKeys, []string{"Authorization"}) {
		t.Errorf("remote = %+v", r)
	}
	if findServer(manifest, "kiro", "power-figma-figma") == nil {
		t.Errorf("powers-section server not emitted")
	}

	local := findServer(manifest, "kiro", "local")
	if local == nil {
		t.Fatalf("workspace server not emitted: %+v", manifest.MCPServers)
	}
	if want := filepath.Join("/home/test", "src/app"); local.ProjectPath != want {
		t.Errorf("ProjectPath = %q, want %q", local.ProjectPath, want)
	}
	if findServer(manifest, "kiro", "ignored") != nil {
		t.Errorf("powers section read from a workspace file")
	}

	// Owning servers earns a clients[] row even with no install evidence.
	if c := findClient(manifest, "kiro"); c == nil || !c.HasMCPServers {
		t.Errorf("kiro client row = %+v", c)
	}
}

// TestScanKiro_Powers covers both power formats, and that only powers
// named in installed.json count.
func TestScanKiro_Powers(t *testing.T) {
	manifest := runScan(t, map[string]string{
		".kiro/powers/installed.json": `{"version": "1.0.0", "installedPowers": [
			{"name": "stripe", "registryId": "kiro-recommended"},
			{"name": "figma", "registryId": "user-added"},
			{"name": "../escape", "registryId": "user-added"},
			{"name": "missing", "registryId": "user-added"}
		]}`,
		".kiro/powers/registry.json": `{"powers": []}`,

		// Agent Plugins format.
		".kiro/powers/installed/stripe/plugin.json": `{"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
			"name": "stripe", "version": "1.2.0", "description": "Stripe payments", "author": {"name": "Stripe"}}`,
		".kiro/powers/installed/stripe/mcp.json": `{"$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
			"mcpServers": {
				"api": {"type": "stdio", "command": "./bin/server", "args": ["--root", "${PLUGIN_ROOT}"], "env": {"DATA": "${PLUGIN_DATA}"}},
				"docs": {"type": "streamable-http", "url": "https://docs.stripe.com/mcp"}
			}}`,
		".kiro/powers/installed/stripe/skills/checkout/SKILL.md":   namedSkill("checkout"),
		".kiro/powers/installed/stripe/dev.kiro/steering/style.md": "# style\n",

		// Legacy format: servers live in the settings powers section.
		".kiro/powers/installed/figma/POWER.md":      "---\nname: figma\ndisplayName: Figma\ndescription: Design to code\nkeywords: [figma]\n---\n# Figma\n",
		".kiro/powers/installed/figma/mcp.json":      `{"mcpServers": {"figma": {"url": "https://mcp.figma.com/mcp"}}}`,
		".kiro/powers/installed/figma/steering/a.md": "# a\n",

		// Not in installed.json, and a registry clone: neither may leak.
		".kiro/powers/installed/stale/plugin.json":                       `{"name": "stale"}`,
		".kiro/powers/installed/stale/mcp.json":                          `{"mcpServers": {"phantom": {"type": "stdio", "command": "x"}}}`,
		".kiro/powers/repos/acme/skills/phantom/SKILL.md":                namedSkill("phantom"),
		".kiro/powers/registry-repos/acme/power/.kiro/settings/mcp.json": `{"mcpServers": {"phantom2": {"command": "x"}}}`,
	})

	stripe := findPlugin(manifest, "kiro", "stripe")
	if stripe == nil {
		t.Fatalf("stripe power not emitted: %+v", manifest.Plugins)
	}
	if stripe.PluginType != "kiro_power" || stripe.Version != "1.2.0" || stripe.Marketplace != "kiro-recommended" ||
		stripe.Author != "Stripe" || !stripe.Enabled || !stripe.HasMCPServers || !stripe.HasSkills || !stripe.HasRules {
		t.Errorf("stripe = %+v", stripe)
	}
	root := filepath.Join("/home/test", ".kiro/powers/installed/stripe")
	api := findServer(manifest, "kiro", "api")
	if api == nil {
		t.Fatalf("plugin power server not emitted: %+v", manifest.MCPServers)
	}
	if want := root + "/bin/server"; api.Command != want {
		t.Errorf("api.Command = %q, want %q", api.Command, want)
	}
	if !slices.Equal(api.Args, []string{"--root", root}) {
		t.Errorf("api.Args = %v", api.Args)
	}
	if d := findServer(manifest, "kiro", "docs"); d == nil || d.Transport != "streamable-http" {
		t.Errorf("docs = %+v", d)
	}
	if got := skillClients(manifest, "stripe/skills/checkout/SKILL.md"); !slices.Equal(got, []string{"kiro"}) {
		t.Errorf("power skill clients = %v", got)
	}

	figma := findPlugin(manifest, "kiro", "figma")
	if figma == nil {
		t.Fatalf("legacy power not emitted: %+v", manifest.Plugins)
	}
	if figma.Description != "Design to code" || figma.Marketplace != "user-added" || !figma.HasMCPServers || !figma.HasRules {
		t.Errorf("figma = %+v", figma)
	}
	if findServer(manifest, "kiro", "figma") != nil {
		t.Errorf("legacy power servers emitted from the power dir; they belong to the settings powers section")
	}

	if len(manifest.Plugins) != 2 {
		t.Errorf("plugins = %+v, want stripe and figma only", manifest.Plugins)
	}
	for _, name := range []string{"phantom", "phantom2"} {
		if findServer(manifest, "kiro", name) != nil {
			t.Errorf("%s leaked from an uninstalled power tree", name)
		}
	}
	for _, sk := range manifest.Skills {
		if sk.Name == "phantom" {
			t.Errorf("registry clone skill leaked: %+v", sk)
		}
	}
}

// TestScanKiro_Agents covers servers declared in custom agent profiles,
// in both the JSON and Markdown-frontmatter formats.
func TestScanKiro_Agents(t *testing.T) {
	manifest := runScan(t, map[string]string{
		".kiro/agents/reviewer.json": `{"name": "reviewer", "prompt": "review",
			"mcpServers": {"git": {"command": "uvx", "args": ["mcp-server-git"]}, "off": {"command": "x", "disabled": true}}}`,
		".kiro/agents/team/writer.md": "---\nname: writer\nmcpServers:\n  search:\n    url: https://search.example.com/mcp\n---\nYou write.\n",
		".kiro/agents/plain.md":       "---\nname: plain\n---\nNo servers.\n",
	})
	if g := findServer(manifest, "kiro", "git"); g == nil || g.File != filepath.Join("/home/test", ".kiro/agents/reviewer.json") {
		t.Errorf("json agent server = %+v", g)
	}
	if findServer(manifest, "kiro", "off") != nil {
		t.Errorf("disabled agent server emitted")
	}
	if s := findServer(manifest, "kiro", "search"); s == nil || s.URL != "https://search.example.com/mcp" {
		t.Errorf("markdown agent server = %+v", s)
	}
}

// TestScanKiro_Skills: ~/.kiro/skills and <workspace>/.kiro/skills are
// read by Kiro alone.
func TestScanKiro_Skills(t *testing.T) {
	manifest := runScanPlatform(t, "linux", map[string]string{
		".local/share/kiro-cli/data.sqlite3":     "",
		".kiro/skills/deploy/SKILL.md":           namedSkill("deploy"),
		"work/api/.kiro/skills/migrate/SKILL.md": namedSkill("migrate"),
	})
	if got := skillClients(manifest, ".kiro/skills/deploy/SKILL.md"); !slices.Equal(got, []string{"kiro"}) {
		t.Errorf("home skill clients = %v", got)
	}
	if got := skillClients(manifest, "work/api/.kiro/skills/migrate/SKILL.md"); !slices.Equal(got, []string{"kiro"}) {
		t.Errorf("workspace skill clients = %v", got)
	}
	for _, sk := range manifest.Skills {
		if sk.Name == "migrate" && sk.ProjectPath != filepath.Join("/home/test", "work/api") {
			t.Errorf("workspace skill ProjectPath = %q", sk.ProjectPath)
		}
	}
	c := findClient(manifest, "kiro")
	if c == nil || !c.HasSkills {
		t.Fatalf("kiro client row = %+v", c)
	}
	if want := filepath.Join("/home/test", ".local/share/kiro-cli"); c.InstallPath != want {
		t.Errorf("InstallPath = %q, want %q", c.InstallPath, want)
	}
	if want := filepath.Join("/home/test", ".kiro"); c.ConfigPath != want {
		t.Errorf("ConfigPath = %q, want %q", c.ConfigPath, want)
	}
}

// TestDetectKiro covers each install signal per platform, and that a
// bare ~/.kiro (hand-authorable config) is not one.
func TestDetectKiro(t *testing.T) {
	cases := []struct {
		platform, rel string
	}{
		{"darwin", "Applications/Kiro.app/Contents/Info.plist"},
		{"darwin", "Library/Application Support/Kiro/Local Storage/x"},
		{"darwin", "Library/Application Support/kiro-cli/data.sqlite3"},
		{"linux", ".config/Kiro/User/settings.json"},
		{"linux", ".local/bin/kiro-cli"},
		{"windows", "AppData/Local/Programs/Kiro/Kiro.exe"},
		{"windows", "AppData/Roaming/Kiro/User/settings.json"},
		{"linux", ".kiro/powers/installed.json"},
	}
	for _, tc := range cases {
		t.Run(tc.platform+"/"+tc.rel, func(t *testing.T) {
			manifest := runScanPlatform(t, tc.platform, map[string]string{tc.rel: "x"})
			if findClient(manifest, "kiro") == nil {
				t.Errorf("kiro not detected from %s", tc.rel)
			}
		})
	}

	manifest := runScan(t, map[string]string{
		".kiro/settings/cli.json": "{}",
		".kiro/steering/a.md":     "# a\n",
	})
	if c := findClient(manifest, "kiro"); c != nil {
		t.Errorf("config alone reported kiro: %+v", c)
	}
}

// TestScanKiro_ProjectAgents: agent profiles in a workspace's .kiro/agents
// are inventoried with their project, like the home ones, and the walk hands
// the directory over instead of descending into it.
func TestScanKiro_ProjectAgents(t *testing.T) {
	manifest := runScan(t, map[string]string{
		".kiro/agents/home.json": `{"name": "home", "mcpServers": {"home-git": {"command": "uvx", "args": ["mcp-server-git"]}}}`,
		"work/api/.kiro/agents/reviewer.json": `{"name": "reviewer",
			"mcpServers": {"proj-docs": {"url": "https://docs.example.com/mcp"}}}`,
		"work/api/.kiro/agents/team/writer.md": "---\nname: writer\nmcpServers:\n  proj-search:\n    url: https://search.example.com/mcp\n---\nYou write.\n",
	})

	home := findServer(manifest, "kiro", "home-git")
	if home == nil || home.ProjectPath != "" {
		t.Fatalf("home agent server = %+v, want it with no project", home)
	}
	project := filepath.Join("/home/test", "work/api")
	for _, name := range []string{"proj-docs", "proj-search"} {
		s := findServer(manifest, "kiro", name)
		if s == nil {
			t.Fatalf("%s not inventoried: %+v", name, manifest.MCPServers)
		}
		if s.ProjectPath != project {
			t.Errorf("%s ProjectPath = %q, want %q", name, s.ProjectPath, project)
		}
	}

	counts := map[string]int{}
	for _, s := range manifest.MCPServers {
		counts[s.Name]++
	}
	for name, n := range counts {
		if n != 1 {
			t.Errorf("%s reported %d times, want once", name, n)
		}
	}
}

// TestScanKiro_AgentsWalkBounded (review t8): the agents reader keeps the
// scanner's guards, since it now runs in every workspace.
func TestScanKiro_AgentsWalkBounded(t *testing.T) {
	files := map[string]string{
		"proj/.kiro/agents/ok.json":                     `{"mcpServers": {"kept": {"url": "https://kept.example.com/mcp"}}}`,
		"proj/.kiro/agents/node_modules/pkg/agent.json": `{"mcpServers": {"vendored": {"url": "https://v.example.com/mcp"}}}`,
		"proj/.kiro/agents/a/b/c/d/deep.json":           `{"mcpServers": {"too-deep": {"url": "https://d.example.com/mcp"}}}`,
		"proj/.kiro/agents/a/b/c/shallow-enough.json":   `{"mcpServers": {"three-down": {"url": "https://s.example.com/mcp"}}}`,
	}
	manifest := runScan(t, files)
	for name, want := range map[string]bool{"kept": true, "three-down": true, "vendored": false, "too-deep": false} {
		if got := findServer(manifest, "kiro", name) != nil; got != want {
			t.Errorf("%s reported = %v, want %v", name, got, want)
		}
	}

	// Past the file cap the reader stops rather than reading everything.
	many := map[string]string{}
	for i := 0; i < kiroAgentsMaxFiles+10; i++ {
		many[fmt.Sprintf("proj/.kiro/agents/a%04d.json", i)] = fmt.Sprintf(`{"mcpServers": {"s%04d": {"url": "https://x.example.com/%d"}}}`, i, i)
	}
	if n := len(runScan(t, many).MCPServers); n != kiroAgentsMaxFiles {
		t.Errorf("servers from %d profiles = %d, want the cap %d", kiroAgentsMaxFiles+10, n, kiroAgentsMaxFiles)
	}
}
