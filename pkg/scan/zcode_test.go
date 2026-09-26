package scan

import "testing"

func TestZCodeScanDetectsRuntimeWithoutConfig(t *testing.T) {
	manifest := runScanPlatform(t, "darwin", map[string]string{
		".zcode/v2/credentials.json": `{}`,
	})
	for _, client := range manifest.Clients {
		if client.Name == "zcode" {
			return
		}
	}
	t.Fatal("ZCode runtime-only installation was not detected")
}

func TestZCodeScanNativeAndWorkspaceConfigs(t *testing.T) {
	manifest := runScanPlatform(t, "darwin", map[string]string{
		".zcode/cli/config.json": `{"mcp":{"servers":{"user":{"type":"http","url":"https://user.example/mcp"}}}}`,
		"project/.zcode/config.json": `{"mcp":{"servers":{
			"project":{"type":"stdio","command":"npx","args":["-y","project-server"]},
			"disabled":{"url":"https://disabled.example/mcp","enable":false}
		}}}`,
		"project/.agents/mcp.json": `{"mcpServers":{"legacy":{"url":"https://legacy.example/mcp"}}}`,
	})
	if got := findServer(manifest, "zcode", "user"); got == nil || got.URL != "https://user.example/mcp" {
		t.Fatalf("user ZCode server missing: %+v", got)
	}
	if got := findServer(manifest, "zcode", "project"); got == nil || got.Transport != "stdio" {
		t.Fatalf("workspace ZCode server missing: %+v", got)
	}
	if got := findServer(manifest, "zcode", "legacy"); got != nil {
		t.Fatalf("native workspace config should suppress .agents fallback: %+v", got)
	}
	if got := findServer(manifest, "zcode", "disabled"); got != nil {
		t.Fatalf("disabled ZCode server was emitted: %+v", got)
	}
	var foundClient bool
	for _, client := range manifest.Clients {
		if client.Name == "zcode" {
			foundClient = true
		}
	}
	if !foundClient {
		t.Fatal("ZCode client was not reported")
	}
}

func TestZCodeScanProjectAliasAndSkills(t *testing.T) {
	manifest := runScanPlatform(t, "darwin", map[string]string{
		"project/zcode.json":             `{"mcp":{"servers":{"alias":{"url":"https://alias.example/mcp"}}}}`,
		".zcode/skills/example/SKILL.md": "# Example",
	})
	if got := findServer(manifest, "zcode", "alias"); got == nil {
		t.Fatal("ZCode zcode.json alias was not scanned")
	}
	var foundSkill bool
	for _, skill := range manifest.Skills {
		if skill.Name == "example" && skill.Client == "zcode" {
			foundSkill = true
		}
	}
	if !foundSkill {
		t.Fatalf("ZCode .zcode/skills was not scanned: %+v", manifest.Skills)
	}
}

func TestZCodeScanAgentsFallbackAndPlugins(t *testing.T) {
	manifest := runScanPlatform(t, "linux", map[string]string{
		".zcode/cli/config.json": `{"mcp":{"servers":{}}}`,
		".agents/mcp.json":       `{"mcpServers":{"legacy":{"url":"https://legacy.example/mcp"}}}`,
		".zcode/cli/plugins/cache/market/example/1.0.0/.zcode-plugin/plugin.json": `{"name":"example","version":"1.0.0"}`,
		".zcode/cli/plugins/cache/market/example/1.0.0/.mcp.json":                 `{"mcpServers":{"server":{"type":"http","url":"https://plugin.example/mcp"}}}`,
	})
	if got := findServer(manifest, "zcode", "legacy"); got == nil {
		t.Fatal("ZCode .agents fallback was not scanned")
	}
	if got := findServer(manifest, "zcode", "plugin:example:server"); got == nil || got.URL != "https://plugin.example/mcp" {
		t.Fatalf("ZCode plugin MCP server missing or not namespaced: %+v", got)
	}
	if len(manifest.Plugins) != 1 || manifest.Plugins[0].Name != "example" || manifest.Plugins[0].PluginType != "zcode_plugin" || !manifest.Plugins[0].HasMCPServers || !manifest.Plugins[0].Enabled {
		t.Fatalf("ZCode plugin observation missing: %+v", manifest.Plugins)
	}
}
