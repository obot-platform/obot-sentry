package scan

import (
	"io/fs"
	"log/slog"
	"path"
	"slices"
	"strings"

	"github.com/obot-platform/obot/apiclient/types"
)

// OpenCode uses a literal ~/.config directory on every platform,
// including Windows (%USERPROFILE%\.config\opencode):
// https://opencode.ai/docs/config/
const (
	opencodeGlobalConfigJSONRel  = ".config/opencode/opencode.json"
	opencodeGlobalConfigJSONCRel = ".config/opencode/opencode.jsonc"
	opencodeLocalPluginsRel      = ".config/opencode/plugins"
	opencodeNPMCacheRel          = ".cache/opencode/node_modules"
)

var opencodePluginExtensions = map[string]bool{
	".js":  true,
	".ts":  true,
	".mjs": true,
	".mts": true,
}

// opencodeConfig is opencode.json's shape: top-level `mcp` map of named
// entries, plus `plugin` array listing npm package plugin names.
type opencodeConfig struct {
	MCP    map[string]opencodeEntry `json:"mcp"`
	Plugin []string                 `json:"plugin"`
}

// opencodeEntry has OpenCode-specific transport tags ("local"/"remote")
// and a Command-as-array shape for stdio.
type opencodeEntry struct {
	Type        string         `json:"type"`
	Command     []string       `json:"command"`
	URL         string         `json:"url"`
	Environment map[string]any `json:"environment"`
	Headers     map[string]any `json:"headers"`
	Enabled     *bool          `json:"enabled"`
}

type opencodeScanner struct{}

func (opencodeScanner) Name() string { return "opencode" }

func (opencodeScanner) Presence(string) presenceDef {
	return presenceDef{binaries: []string{"opencode"}, configPaths: []string{".config/opencode"}}
}

func (opencodeScanner) GlobalConfigs(string) []string {
	return []string{opencodeGlobalConfigJSONRel, opencodeGlobalConfigJSONCRel}
}

func (opencodeScanner) ProjectConfigs() []string { return []string{"opencode.json"} }

func (c opencodeScanner) ScanHome(s *state) observations {
	var (
		obs            observations
		npmPluginNames []string
	)
	for _, rel := range []string{opencodeGlobalConfigJSONRel, opencodeGlobalConfigJSONCRel} {
		cfg, ok := readJSON[opencodeConfig](s.fsys, rel)
		if !ok {
			continue
		}
		configPath := s.addFileOrAbs(rel)
		obs.servers = append(obs.servers, opencodeEmit(cfg.MCP, configPath, "")...)
		for _, name := range cfg.Plugin {
			if !slices.Contains(npmPluginNames, name) {
				npmPluginNames = append(npmPluginNames, name)
			}
		}
	}
	obs.add(scanOpenCodeLocalPlugins(s))
	obs.add(scanOpenCodeNPMPlugins(s, npmPluginNames))
	return obs
}

func (opencodeScanner) ScanProject(s *state, configRel string) observations {
	cfg, ok := readJSON[opencodeConfig](s.fsys, configRel)
	if !ok {
		return observations{}
	}
	configPath := s.addFileOrAbs(configRel)
	projectPath := s.abs(path.Dir(configRel))
	return observations{servers: opencodeEmit(cfg.MCP, configPath, projectPath)}
}

func opencodeEmit(servers map[string]opencodeEntry, configPath, projectPath string) []types.DeviceScanMCPServer {
	out := make([]types.DeviceScanMCPServer, 0, len(servers))
	for _, name := range sortedKeys(servers) {
		e := servers[name]
		if e.Enabled != nil && !*e.Enabled {
			continue
		}
		obs, ok := e.toServer(name, configPath, projectPath)
		if !ok {
			continue
		}
		out = append(out, obs)
	}
	return out
}

func (e opencodeEntry) toServer(name, configPath, projectPath string) (types.DeviceScanMCPServer, bool) {
	switch e.Type {
	case "local":
		if len(e.Command) == 0 {
			return types.DeviceScanMCPServer{}, false
		}
		cmd := e.Command[0]
		var args []string
		if len(e.Command) > 1 {
			args = e.Command[1:]
		}
		return types.DeviceScanMCPServer{
			Client:      "opencode",
			ProjectPath: projectPath,
			File:        configPath,
			Name:        name,
			Transport:   "stdio",
			Command:     cmd,
			Args:        args,
			EnvKeys:     sortedKeys(e.Environment),
			HeaderKeys:  []string{},
			ConfigHash:  mcpConfigHash(name, "stdio", cmd, args, ""),
		}, true
	case "remote":
		if e.URL == "" {
			return types.DeviceScanMCPServer{}, false
		}
		return types.DeviceScanMCPServer{
			Client:      "opencode",
			ProjectPath: projectPath,
			File:        configPath,
			Name:        name,
			Transport:   "http",
			URL:         e.URL,
			EnvKeys:     []string{},
			HeaderKeys:  sortedKeys(e.Headers),
			ConfigHash:  mcpConfigHash(name, "http", "", nil, e.URL),
		}, true
	}
	return types.DeviceScanMCPServer{}, false
}

// scanOpenCodeLocalPlugins emits plugin observations for
// subdirectories and standalone plugin files under
// ~/.config/opencode/plugins/.
func scanOpenCodeLocalPlugins(s *state) observations {
	entries, err := fs.ReadDir(s.fsys, opencodeLocalPluginsRel)
	if err != nil {
		return observations{}
	}
	var obs observations
	for _, e := range entries {
		itemRel := path.Join(opencodeLocalPluginsRel, e.Name())
		if e.IsDir() {
			obs.add(emitOpenCodePluginDir(s, itemRel, e.Name(), "opencode_plugin", ""))
			continue
		}
		if !opencodePluginExtensions[path.Ext(e.Name())] {
			continue
		}
		// Standalone plugin file.
		filePath, err := s.addFile(itemRel)
		if err != nil {
			slog.Debug("opencode: skipping plugin file", "path", itemRel, "err", err)
			continue
		}
		obs.plugins = append(obs.plugins, types.DeviceScanPlugin{
			Client:     "opencode",
			ConfigPath: filePath,
			Name:       strings.TrimSuffix(e.Name(), path.Ext(e.Name())),
			PluginType: "opencode_plugin",
			Enabled:    true,
			Files:      []string{filePath},
			HasHooks:   true,
		})
	}
	return obs
}

// scanOpenCodeNPMPlugins emits plugin observations for npm packages
// listed under opencode.json's `plugin` array (already parsed by
// ScanHome), found in ~/.cache/opencode/node_modules/<pkg>/.
func scanOpenCodeNPMPlugins(s *state, names []string) observations {
	if len(names) == 0 || !dirExists(s.fsys, opencodeNPMCacheRel) {
		return observations{}
	}
	var obs observations
	for _, pkg := range names {
		pkgRel := path.Join(opencodeNPMCacheRel, pkg)
		if !dirExists(s.fsys, pkgRel) {
			continue
		}
		obs.add(emitOpenCodePluginDir(s, pkgRel, pkg, "opencode_npm_plugin", "npm"))
	}
	return obs
}

// emitOpenCodePluginDir reads a plugin directory's package.json (if
// any) for metadata and produces plugin + nested MCP observations.
// Nested MCP config is looked up at {mcp.json, .mcp.json}.
func emitOpenCodePluginDir(s *state, installRel, fallbackName, pluginType, marketplace string) observations {
	s.claim(installRel)
	packageRel := path.Join(installRel, "package.json")
	pkg, _ := readJSON[map[string]any](s.fsys, packageRel)
	name, version, description, author := manifestMetadata(pkg)
	if name == "" {
		name = fallbackName
	}

	var pluginFilePath string
	if pkg != nil {
		pluginFilePath = s.addFileOrAbs(packageRel)
	}

	servers, foundMCP := emitNestedMCPServers(s, installRel, []string{"mcp.json", ".mcp.json"}, pkg, packageRel, "opencode", nil)

	return observations{
		plugins: []types.DeviceScanPlugin{{
			Client:        "opencode",
			ConfigPath:    pluginFilePath,
			Name:          name,
			PluginType:    pluginType,
			Version:       version,
			Description:   description,
			Author:        author,
			Enabled:       true,
			Marketplace:   marketplace,
			Files:         s.listArtifactPaths(installRel, pluginExtensions),
			HasMCPServers: foundMCP,
			HasHooks:      true,
		}},
		servers: servers,
	}
}
