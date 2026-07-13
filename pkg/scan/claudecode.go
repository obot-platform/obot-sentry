package scan

import (
	"io/fs"
	"path"
	"strings"

	"github.com/obot-platform/obot/apiclient/types"
)

// Claude Code stores everything under the home directory on every
// platform (%USERPROFILE%\.claude on Windows):
// https://code.claude.com/docs/en/claude-directory
const (
	claudeGlobalConfigRel     = ".claude.json"
	claudeSettingsRel         = ".claude/settings.json"
	claudePluginsRel          = ".claude/plugins"
	claudeInstalledPluginsRel = ".claude/plugins/installed_plugins.json"
	claudePluginManifestSub   = ".claude-plugin/plugin.json"
)

// claudeCodeConfig is the shape of ~/.claude.json: a global mcpServers
// map plus a projects map keyed by absolute project path, each with its
// own mcpServers block.
type claudeCodeConfig struct {
	MCPServers map[string]mcpServerSpec `json:"mcpServers"`
	Projects   map[string]struct {
		MCPServers map[string]mcpServerSpec `json:"mcpServers"`
	} `json:"projects"`
}

// claudePluginsRegistry is the shape of installed_plugins.json: a
// `plugins` map keyed by "name@marketplace" → list of installations.
type claudePluginsRegistry struct {
	Plugins map[string][]struct {
		InstallPath string `json:"installPath"`
		Version     string `json:"version"`
	} `json:"plugins"`
}

type claudeCodeScanner struct{}

func (claudeCodeScanner) Name() string { return "claude_code" }

func (claudeCodeScanner) Presence(string) presenceDef {
	return presenceDef{binaries: []string{"claude"}, configPaths: []string{".claude"}}
}

func (claudeCodeScanner) GlobalConfigs(string) []string { return []string{claudeGlobalConfigRel} }

func (claudeCodeScanner) ProjectConfigs() []string { return []string{".mcp.json"} }

func (c claudeCodeScanner) ScanHome(s *state) observations {
	// The plugins tree also holds marketplace clones and stale version
	// caches; the plugin scan below inventories what's actually
	// installed, so nothing under it may leak through the walk.
	s.claim(claudePluginsRel)

	obs := c.scanGlobalConfig(s)
	obs.add(c.scanPlugins(s))
	return obs
}

func (claudeCodeScanner) scanGlobalConfig(s *state) observations {
	cfg, ok := readJSON[claudeCodeConfig](s.fsys, claudeGlobalConfigRel)
	if !ok {
		return observations{}
	}
	configPath := s.addFileOrAbs(claudeGlobalConfigRel)

	servers := make([]types.DeviceScanMCPServer, 0, len(cfg.MCPServers))
	for _, name := range sortedKeys(cfg.MCPServers) {
		if e := cfg.MCPServers[name]; !e.disabled() {
			servers = append(servers, e.toServer(name, "claude_code", configPath, ""))
		}
	}
	for _, projKey := range sortedKeys(cfg.Projects) {
		// Project keys are native-form absolute paths; re-anchor them
		// onto the root so observations from a WSL home agree with the
		// walk-derived ones (\\wsl.localhost\...) for the same project.
		projectPath := projKey
		if rel, ok := s.relToHome(projKey); ok {
			projectPath = s.abs(rel)
		}
		proj := cfg.Projects[projKey]
		for _, name := range sortedKeys(proj.MCPServers) {
			if e := proj.MCPServers[name]; !e.disabled() {
				servers = append(servers, e.toServer(name, "claude_code", configPath, projectPath))
			}
		}
	}
	return observations{servers: servers}
}

func (claudeCodeScanner) ScanProject(s *state, configRel string) observations {
	projectPath := s.abs(path.Dir(configRel))
	return observations{servers: emitJSONServers(s, configRel, "mcpServers", "claude_code", projectPath)}
}

// scanPlugins reads installed_plugins.json and emits a plugin
// observation (plus nested MCP server / skill observations) for each
// installation that resolves to a directory under the home.
func (claudeCodeScanner) scanPlugins(s *state) observations {
	registry, ok := readJSON[claudePluginsRegistry](s.fsys, claudeInstalledPluginsRel)
	if !ok || len(registry.Plugins) == 0 {
		return observations{}
	}

	enabledByKey := readEnabledPluginsMap(s.fsys, claudeSettingsRel)

	var obs observations
	for _, pluginKey := range sortedKeys(registry.Plugins) {
		pluginName, marketplace := splitPluginKey(pluginKey)
		for _, install := range registry.Plugins[pluginKey] {
			if install.InstallPath == "" {
				continue
			}
			installRel, ok := s.relToHome(install.InstallPath)
			if !ok || !dirExists(s.fsys, installRel) {
				continue
			}
			manifestRel := path.Join(installRel, claudePluginManifestSub)
			if !fileExists(s.fsys, manifestRel) {
				continue
			}
			obs.add(emitPlugin(s, emitPluginOpts{
				installRel:      installRel,
				manifestRel:     manifestRel,
				pluginType:      "claude_code_plugin",
				client:          "claude_code",
				marketplace:     marketplace,
				enabled:         enabledByKey[pluginKey],
				nameFallback:    pluginName,
				versionFallback: install.Version,
				nestedMCPRel:    []string{"mcp.json", ".mcp.json"},
				mcpServerXform:  substituteClaudePluginRoot(install.InstallPath),
			}))
		}
	}
	return obs
}

// substituteClaudePluginRoot returns an mcpServerXform that replaces
// ${CLAUDE_PLUGIN_ROOT} with installPath in the command, args, env, and
// url fields of a parsed mcpServerSpec.
func substituteClaudePluginRoot(installPath string) func(*mcpServerSpec) {
	return func(e *mcpServerSpec) {
		sub := func(s string) string {
			return strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", installPath)
		}
		e.Command = sub(e.Command)
		e.URL = sub(e.URL)
		for i, a := range e.Args {
			e.Args[i] = sub(a)
		}
		for k, v := range e.Env {
			if str, ok := v.(string); ok {
				e.Env[k] = sub(str)
			}
		}
	}
}

// readEnabledPluginsMap reads enabledPlugins from a Claude-style
// settings file (Cursor uses the same shape) as map[pluginKey]bool.
func readEnabledPluginsMap(fsys fs.FS, rel string) map[string]bool {
	type settings struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	out, ok := readJSON[settings](fsys, rel)
	if !ok {
		return nil
	}
	return out.EnabledPlugins
}

// splitPluginKey separates "name@marketplace" plugin keys into their
// parts.
func splitPluginKey(key string) (name, marketplace string) {
	name, marketplace, _ = strings.Cut(key, "@")
	return name, marketplace
}
