package scan

import (
	"io/fs"
	"path"
	"strings"

	zcode "github.com/obot-platform/obot-sentry/pkg/zcode"
	"github.com/obot-platform/obot/apiclient/types"
)

type zcodeScanConfig struct {
	MCP struct {
		Servers map[string]mcpServerSpec `json:"servers"`
	} `json:"mcp"`
	MCPServers map[string]mcpServerSpec `json:"mcpServers"`
}

func zcodeServers(s *state, rel, projectPath string) observations {
	cfg, ok := readJSON[zcodeScanConfig](s.fsys, rel)
	if !ok {
		return observations{}
	}
	configPath := s.addFileOrAbs(rel)
	s.addClient(types.DeviceScanClient{Name: "zcode", ConfigPath: configPath})
	servers := cfg.MCPServers
	if cfg.MCP.Servers != nil {
		servers = cfg.MCP.Servers
	}
	return observations{servers: zcodeScanServerList(servers, "zcode", configPath, projectPath)}
}

func zcodeCompatServers(s *state, rel, projectPath string) observations {
	if rel != zcode.UserCompatRel && (projectPath == "" || !strings.HasSuffix(rel, "/"+zcode.ProjectCompatRel)) {
		return observations{}
	}
	present, hasServers, readable := zcodeNativeConfigState(s, rel, projectPath)
	if present && (!readable || hasServers) {
		return observations{}
	}
	cfg, ok := readJSON[zcodeScanConfig](s.fsys, rel)
	if !ok {
		return observations{}
	}
	configPath := s.addFileOrAbs(rel)
	servers := cfg.MCPServers
	if cfg.MCP.Servers != nil {
		servers = cfg.MCP.Servers
	}
	return observations{servers: zcodeScanServerList(servers, "zcode", configPath, projectPath)}
}

func zcodeScanServerList(servers map[string]mcpServerSpec, client, filePath, projectPath string) []types.DeviceScanMCPServer {
	out := make([]types.DeviceScanMCPServer, 0, len(servers))
	for _, name := range sortedKeys(servers) {
		entry := servers[name]
		if entry.disabled() {
			continue
		}
		out = append(out, entry.toServer(name, client, filePath, projectPath))
	}
	return out
}

func zcodeNativeConfigState(s *state, rel, projectPath string) (present, hasServers, readable bool) {
	var nativeRel string
	if rel == zcode.UserCompatRel {
		nativeRel = zcode.UserConfigRel
	} else if projectPath != "" && (rel == zcode.ProjectCompatRel || strings.HasSuffix(rel, "/"+zcode.ProjectCompatRel)) {
		projectRel := strings.TrimSuffix(rel, "/"+zcode.ProjectCompatRel)
		if projectRel == rel {
			projectRel = ""
		}
		if projectRel != "" {
			projectRel = path.Clean(projectRel)
		}
		if projectRel == "" {
			if native, ok := s.relToHome(projectPath); ok {
				projectRel = native
			}
		}
		if projectRel != "" {
			nativeRel = path.Join(projectRel, ".zcode", "config.json")
			if !fileExists(s.fsys, nativeRel) {
				alias := path.Join(projectRel, zcode.ProjectAliasRel)
				if fileExists(s.fsys, alias) {
					nativeRel = alias
				}
			}
		}
	}
	if nativeRel == "" || !fileExists(s.fsys, nativeRel) {
		return false, false, true
	}
	cfg, ok := readJSON[zcodeScanConfig](s.fsys, nativeRel)
	if !ok {
		return true, false, false
	}
	return true, len(cfg.MCP.Servers) > 0 || len(cfg.MCPServers) > 0, true
}

// zcodePlugins walks the user or workspace plugin tree and emits the same
// plugin/MCP observations as the other plugin scanners. ZCode's real install
// layout is versioned plugin trees below cli/plugins/cache; the same walk also
// supports local .zcode/plugins trees. ZCode namespaces plugin MCP keys as
// plugin:<plugin>:<server>; the namespace is applied after the shared emitter
// has parsed the nested .mcp.json or manifest block.
func zcodePlugins(s *state, rel, _ string) observations {
	manifests := make([]string, 0)
	_ = fs.WalkDir(s.fsys, rel, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if current != rel && zcodeScanSkipDir(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Name() != "plugin.json" {
			return nil
		}
		parent := path.Base(path.Dir(current))
		if parent == ".zcode-plugin" || parent == ".claude-plugin" {
			manifests = append(manifests, current)
		}
		return nil
	})

	var out observations
	for _, manifestRel := range manifests {
		installRel := path.Dir(path.Dir(manifestRel))
		nameFallback := zcodeManifestName(s, manifestRel, path.Base(installRel))
		pluginObs := emitPlugin(s, emitPluginOpts{
			installRel:   installRel,
			manifestRel:  manifestRel,
			pluginType:   "zcode_plugin",
			client:       "zcode",
			enabled:      zcodePluginEnabled(s, installRel, nameFallback),
			marketplace:  zcodePluginMarketplace(installRel),
			nameFallback: nameFallback,
			nestedMCPRel: []string{".mcp.json", "mcp.json"},
		})
		if len(pluginObs.plugins) > 0 {
			pluginName := pluginObs.plugins[0].Name
			for i := range pluginObs.servers {
				pluginObs.servers[i].Name = "plugin:" + pluginName + ":" + pluginObs.servers[i].Name
			}
		}
		out.add(pluginObs)
	}
	return out
}

func zcodeManifestName(s *state, manifestRel, fallback string) string {
	manifest, ok := readJSON[map[string]any](s.fsys, manifestRel)
	if ok {
		if name, ok := manifest["name"].(string); ok && strings.TrimSpace(name) != "" {
			return name
		}
	}
	return fallback
}

func zcodePluginMarketplace(installRel string) string {
	parts := strings.Split(path.Clean(installRel), "/")
	for i, part := range parts {
		if part == "cache" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func zcodePluginEnabled(s *state, installRel, nameFallback string) bool {
	var cfg struct {
		Plugins struct {
			EnabledPlugins map[string]bool `json:"enabledPlugins"`
		} `json:"plugins"`
	}
	parsed, ok := readJSON[struct {
		Plugins struct {
			EnabledPlugins map[string]bool `json:"enabledPlugins"`
		} `json:"plugins"`
	}](s.fsys, zcode.UserConfigRel)
	if ok {
		cfg.Plugins.EnabledPlugins = parsed.Plugins.EnabledPlugins
		marketplace := zcodePluginMarketplace(installRel)
		keys := []string{nameFallback}
		if marketplace != "" {
			keys = append(keys, nameFallback+"@"+marketplace)
		}
		for _, key := range keys {
			if enabled, exists := cfg.Plugins.EnabledPlugins[key]; exists {
				return enabled
			}
		}
	}
	// Newly installed ZCode plugins are enabled by default. A missing or
	// malformed toggle file therefore does not make an installed plugin vanish
	// from Inventory; the runtime still owns the actual enable state.
	return true
}

func zcodeScanSkipDir(name string) bool {
	switch name {
	case "v2", "logs", "dist", "build", "node_modules", ".git", "__pycache__":
		return true
	default:
		return false
	}
}
