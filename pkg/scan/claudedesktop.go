package scan

import (
	"io/fs"
	"path"
	"strings"

	"github.com/obot-platform/obot/apiclient/types"
)

// claudeDesktopMSIXPackage is the MSIX package family name of the
// current Windows build (winget manifest Anthropic.Claude). MSIX
// virtualizes the app's Roaming writes under the package's LocalCache.
const claudeDesktopMSIXPackage = "Claude_pzs8sxrjxfjjc"

// claudeDesktopDirs returns the home-relative application-support
// directories that may hold claude_desktop_config.json and Cowork
// state, or nil on platforms without an official Claude Desktop build:
// https://modelcontextprotocol.io/quickstart/user
func claudeDesktopDirs(platform string) []string {
	switch platform {
	case "darwin":
		return []string{"Library/Application Support/Claude"}
	case "windows":
		return []string{
			// Legacy per-user .exe install writes %APPDATA%\Claude.
			"AppData/Roaming/Claude",
			// MSIX install virtualizes the same tree under LocalCache.
			"AppData/Local/Packages/" + claudeDesktopMSIXPackage + "/LocalCache/Roaming/Claude",
		}
	default:
		return nil
	}
}

// claudeDesktopExtensions wraps the top-level `extensions` map in
// extensions-installations.json. Each extension carries a manifest with
// nested server info.
type claudeDesktopExtensions struct {
	Extensions map[string]struct {
		Manifest struct {
			DisplayName string `json:"display_name"`
			Server      struct {
				MCPConfig  *mcpServerSpec `json:"mcp_config"`
				EntryPoint string         `json:"entry_point"`
			} `json:"server"`
		} `json:"manifest"`
	} `json:"extensions"`
}

// claudeDesktopConfig is the JSON shape of claude_desktop_config.json.
// One parse feeds both the MCP server observations and the connector
// plugin rows.
type claudeDesktopConfig struct {
	MCPServers map[string]mcpServerSpec `json:"mcpServers"`
}

type claudeDesktopScanner struct{}

func (claudeDesktopScanner) Name() string { return "claude_desktop" }

func (claudeDesktopScanner) Presence(platform string) presenceDef {
	return presenceDef{
		appBundles: []string{"Claude.app"},
		installDirs: []string{
			"AppData/Local/AnthropicClaude", // legacy per-user installer
			"AppData/Local/Packages/" + claudeDesktopMSIXPackage,
		},
		configPaths: claudeDesktopDirs(platform),
	}
}

func (claudeDesktopScanner) GlobalConfigs(platform string) []string {
	var out []string
	for _, dir := range claudeDesktopDirs(platform) {
		out = append(out,
			path.Join(dir, "extensions-installations.json"),
			path.Join(dir, "claude_desktop_config.json"),
		)
	}
	return out
}

func (claudeDesktopScanner) ProjectConfigs() []string { return nil }

func (claudeDesktopScanner) ScanProject(*state, string) observations { return observations{} }

// ScanHome emits four flavors of observation, all tagged
// client=claude_desktop:
//
//  1. One MCP server per extension in extensions-installations.json and
//     per entry in claude_desktop_config.json's mcpServers block.
//  2. One plugin per mcpServers entry (plugin_type =
//     claude_desktop_connector), capturing the connector as a
//     first-class artifact alongside its MCP server.
//  3. One plugin per Cowork RPM-installed plugin under
//     local-agent-mode-sessions/<X>/<Y>/rpm/plugin_<id>/, plus its
//     nested MCP servers (from .mcp.json) and skills.
//  4. Skills for every skills/<name>/SKILL.md under
//     local-agent-mode-sessions/skills-plugin/<install>/<plugin>/.
//     Cowork ships these as a bundle — there's no .mcp.json, no
//     commands, no hooks; treating the bundle as a plugin would inflate
//     the inventory with an empty wrapper, so only the skills surface.
func (c claudeDesktopScanner) ScanHome(s *state) observations {
	var obs observations
	for _, dir := range claudeDesktopDirs(s.platform) {
		obs.servers = append(obs.servers, scanClaudeDesktopExtensions(s, path.Join(dir, "extensions-installations.json"))...)
		obs.add(scanClaudeDesktopServers(s, path.Join(dir, "claude_desktop_config.json")))
		obs.add(scanCoworkRpmPlugins(s, dir))
		obs.skills = append(obs.skills, scanCoworkSkillsPlugin(s, dir)...)
	}
	return obs
}

// scanClaudeDesktopServers parses claude_desktop_config.json once and
// emits both an MCP server and a connector plugin row per enabled
// entry.
func scanClaudeDesktopServers(s *state, configRel string) observations {
	cfg, ok := readJSON[claudeDesktopConfig](s.fsys, configRel)
	if !ok {
		return observations{}
	}
	configPath := s.addFileOrAbs(configRel)

	var obs observations
	for _, name := range sortedKeys(cfg.MCPServers) {
		e := cfg.MCPServers[name]
		if e.disabled() {
			continue
		}
		obs.servers = append(obs.servers, e.toServer(name, "claude_desktop", configPath, ""))
		obs.plugins = append(obs.plugins, types.DeviceScanPlugin{
			Client:        "claude_desktop",
			ConfigPath:    configPath,
			Name:          name,
			PluginType:    "claude_desktop_connector",
			Enabled:       true,
			Files:         []string{configPath},
			HasMCPServers: true,
		})
	}
	return obs
}

// claudeDesktopSkillsManifest is the shape of <snapshot>/manifest.json.
// We only need lastUpdated for active-snapshot selection; per-skill
// metadata comes from SKILL.md frontmatter via ingestSkill.
type claudeDesktopSkillsManifest struct {
	LastUpdated int64 `json:"lastUpdated"`
}

// scanCoworkSkillsPlugin enumerates Cowork's persistent skills bundle,
// laid out as <root>/<install-uuid>/<plugin-uuid>/{.claude-plugin/
// plugin.json, manifest.json, skills/<name>/SKILL.md}. Multiple
// <install-uuid> snapshots of the same <plugin-uuid> can coexist on
// disk; only the snapshot with the newest manifest.json:lastUpdated is
// kept so stale copies don't produce phantom skill rows.
func scanCoworkSkillsPlugin(s *state, appDir string) []skill {
	bundleRoot := path.Join(appDir, "local-agent-mode-sessions/skills-plugin")
	snapshotInstalls, err := fs.ReadDir(s.fsys, bundleRoot)
	if err != nil {
		return nil
	}

	type snapshot struct {
		installRel  string
		lastUpdated int64
	}
	newestByPluginID := map[string]snapshot{}
	for _, snapshotInstall := range snapshotInstalls {
		if !snapshotInstall.IsDir() {
			continue
		}
		pluginDirs, err := fs.ReadDir(s.fsys, path.Join(bundleRoot, snapshotInstall.Name()))
		if err != nil {
			continue
		}
		for _, pluginDir := range pluginDirs {
			if !pluginDir.IsDir() {
				continue
			}
			installRel := path.Join(bundleRoot, snapshotInstall.Name(), pluginDir.Name())
			if !fileExists(s.fsys, path.Join(installRel, ".claude-plugin/plugin.json")) {
				continue
			}
			var lastUpdated int64
			if manifest, ok := readJSON[claudeDesktopSkillsManifest](s.fsys, path.Join(installRel, "manifest.json")); ok {
				lastUpdated = manifest.LastUpdated
			}
			pluginID := pluginDir.Name()
			existing, seen := newestByPluginID[pluginID]
			if !seen || lastUpdated > existing.lastUpdated || (lastUpdated == existing.lastUpdated && installRel < existing.installRel) {
				newestByPluginID[pluginID] = snapshot{installRel: installRel, lastUpdated: lastUpdated}
			}
		}
	}

	var out []skill
	for _, pluginID := range sortedKeys(newestByPluginID) {
		out = append(out, nestedSkills(s, newestByPluginID[pluginID].installRel, "claude_desktop")...)
	}
	return out
}

// claudeDesktopRpmManifest is the partial shape of rpm/manifest.json we
// consume. We only read what's not in .claude-plugin/plugin.json: the
// per-plugin marketplaceName. Everything else (name, version,
// description, author) comes from plugin.json via emitPlugin.
type claudeDesktopRpmManifest struct {
	Plugins []struct {
		ID              string `json:"id"`
		MarketplaceName string `json:"marketplaceName"`
	} `json:"plugins"`
}

// scanCoworkRpmPlugins enumerates Cowork's server-pushed RPM plugin
// installations. Layout:
//
//	<appDir>/local-agent-mode-sessions/<X>/<Y>/rpm/
//	    manifest.json                    ← marketplace metadata join
//	    plugin_<id>/                     ← install root
//	        .claude-plugin/plugin.json   ← name, version, description, author
//	        .mcp.json                    ← nested MCP servers (optional)
//	        skills/<name>/SKILL.md       ← nested skills (optional)
//
// .claude-plugin/plugin.json is the source of truth for plugin
// metadata; rpm/manifest.json supplies marketplaceName (joined by
// plugin id) as enrichment. Install paths are naturally unique across
// rpm/ trees (each is rooted at a distinct <X>/<Y> session context), so
// no dedup is needed.
func scanCoworkRpmPlugins(s *state, appDir string) observations {
	sessionsRoot := path.Join(appDir, "local-agent-mode-sessions")
	outerSessionDirs, err := fs.ReadDir(s.fsys, sessionsRoot)
	if err != nil {
		return observations{}
	}

	var obs observations
	for _, outerSession := range outerSessionDirs {
		if !outerSession.IsDir() || outerSession.Name() == "skills-plugin" {
			continue
		}
		innerSessionDirs, err := fs.ReadDir(s.fsys, path.Join(sessionsRoot, outerSession.Name()))
		if err != nil {
			continue
		}
		for _, innerSession := range innerSessionDirs {
			if !innerSession.IsDir() {
				continue
			}
			rpmRoot := path.Join(sessionsRoot, outerSession.Name(), innerSession.Name(), "rpm")
			pluginDirs, err := fs.ReadDir(s.fsys, rpmRoot)
			if err != nil {
				continue
			}
			marketplaceByPluginID := map[string]string{}
			if manifest, ok := readJSON[claudeDesktopRpmManifest](s.fsys, path.Join(rpmRoot, "manifest.json")); ok {
				for _, p := range manifest.Plugins {
					if p.ID != "" && p.MarketplaceName != "" {
						marketplaceByPluginID[p.ID] = p.MarketplaceName
					}
				}
			}
			for _, pluginDir := range pluginDirs {
				if !pluginDir.IsDir() || !strings.HasPrefix(pluginDir.Name(), "plugin_") {
					continue
				}
				installRel := path.Join(rpmRoot, pluginDir.Name())
				if !fileExists(s.fsys, path.Join(installRel, ".claude-plugin/plugin.json")) {
					// Half-written snapshot or non-install dir. Skip.
					continue
				}
				obs.add(emitPlugin(s, emitPluginOpts{
					installRel:   installRel,
					manifestRel:  path.Join(installRel, ".claude-plugin/plugin.json"),
					pluginType:   "claude_desktop_plugin",
					client:       "claude_desktop",
					marketplace:  marketplaceByPluginID[pluginDir.Name()],
					enabled:      true,
					nameFallback: pluginDir.Name(), // "plugin_<id>" when plugin.json lacks name
					nestedMCPRel: []string{".mcp.json"},
				}))
			}
		}
	}
	return obs
}

func scanClaudeDesktopExtensions(s *state, extRel string) []types.DeviceScanMCPServer {
	cfg, ok := readJSON[claudeDesktopExtensions](s.fsys, extRel)
	if !ok {
		return nil
	}
	configPath := s.addFileOrAbs(extRel)

	out := make([]types.DeviceScanMCPServer, 0, len(cfg.Extensions))
	for _, name := range sortedKeys(cfg.Extensions) {
		ext := cfg.Extensions[name]
		displayName := name
		if ext.Manifest.DisplayName != "" {
			displayName = ext.Manifest.DisplayName
		}

		var (
			command string
			args    []string
			env     map[string]any
		)
		if mc := ext.Manifest.Server.MCPConfig; mc != nil {
			command = mc.Command
			args = mc.Args
			env = mc.Env
		} else if ep := ext.Manifest.Server.EntryPoint; ep != "" {
			parts := strings.Fields(ep)
			if len(parts) > 0 {
				command = parts[0]
				args = parts[1:]
			}
		}

		out = append(out, types.DeviceScanMCPServer{
			Client:     "claude_desktop",
			File:       configPath,
			Name:       displayName,
			Transport:  "stdio",
			Command:    command,
			Args:       args,
			EnvKeys:    sortedKeys(env),
			HeaderKeys: []string{},
			ConfigHash: mcpConfigHash(displayName, "stdio", command, args, ""),
		})
	}
	return out
}
