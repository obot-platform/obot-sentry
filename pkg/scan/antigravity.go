package scan

import (
	"io/fs"
	"path"
)

// Antigravity keeps per-variant runtime state under ~/.gemini
// (antigravity, antigravity-cli, antigravity-ide) and the configuration
// shared by every variant in ~/.gemini/config:
// https://antigravity.google/docs/mcp
const (
	antigravityMCPConfigRel   = ".gemini/config/mcp_config.json"
	antigravityPluginsRel     = ".gemini/config/plugins"
	antigravityManifestSub    = "plugin.json"
	antigravityVersionSub     = "installed_version.json"
	antigravityBuiltinSkills  = ".gemini/antigravity/builtin/skills"
	antigravityPluginTypeName = "antigravity_plugin"
)

// antigravityServers reads Antigravity's MCP config, which puts the
// endpoint under serverUrl.
func antigravityServers(s *state, rel, projectPath string) observations {
	return observations{servers: emitJSONServers(s, rel, "mcpServers", "antigravity", projectPath)}
}

// antigravityPlugins walks ~/.gemini/config/plugins/<name>/. Antigravity
// records no disabled state.
func antigravityPlugins(s *state, _, _ string) observations {
	entries, err := fs.ReadDir(s.fsys, antigravityPluginsRel)
	if err != nil {
		return observations{}
	}
	var obs observations
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		installRel := path.Join(antigravityPluginsRel, e.Name())
		manifestRel := path.Join(installRel, antigravityManifestSub)
		if !fileExists(s.fsys, manifestRel) {
			continue
		}
		obs.add(emitPlugin(s, emitPluginOpts{
			installRel:      installRel,
			manifestRel:     manifestRel,
			pluginType:      antigravityPluginTypeName,
			client:          "antigravity",
			enabled:         true,
			nameFallback:    e.Name(),
			versionFallback: antigravityPluginVersion(s, installRel),
			nestedMCPRel:    []string{"mcp.json", ".mcp.json"},
		}))
	}
	return obs
}

// antigravityPluginVersion reads the version the manifest omits.
func antigravityPluginVersion(s *state, installRel string) string {
	v, ok := readJSON[struct {
		Version string `json:"version"`
	}](s.fsys, path.Join(installRel, antigravityVersionSub))
	if !ok {
		return ""
	}
	return v.Version
}
