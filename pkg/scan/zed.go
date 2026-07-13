package scan

import (
	"io/fs"
	"path"
	"strings"

	"github.com/obot-platform/obot/apiclient/types"
)

// Zed's settings live under ~/.config/zed on macOS and Linux and
// %APPDATA%\Zed on Windows; the extensions data dir differs per
// platform (Windows uses %LOCALAPPDATA%):
// https://zed.dev/docs/configuring-zed
// https://zed.dev/docs/extensions/installing-extensions
func zedSettingsRel(platform string) string {
	if platform == "windows" {
		return "AppData/Roaming/Zed/settings.json"
	}
	return ".config/zed/settings.json"
}

func zedExtensionsRel(platform string) string {
	switch platform {
	case "darwin":
		return "Library/Application Support/Zed/extensions/installed"
	case "windows":
		return "AppData/Local/Zed/extensions/installed" // %LOCALAPPDATA%\Zed
	default:
		return ".local/share/zed/extensions/installed"
	}
}

const zedExtensionPrefix = "mcp-server-"

// zedSettings has only the field we care about. Zed's `context_servers`
// map keys use opaque server names; values follow Zed's own schema:
// either {url, env, headers} for SSE or {command, args, env} for stdio,
// with an optional explicit `enabled: false` skip.
type zedSettings struct {
	ContextServers map[string]zedContextServer `json:"context_servers"`
}

type zedContextServer struct {
	URL     string         `json:"url"`
	Command string         `json:"command"`
	Args    []string       `json:"args"`
	Env     map[string]any `json:"env"`
	Headers map[string]any `json:"headers"`
	Enabled *bool          `json:"enabled"`
}

type zedScanner struct{}

func (zedScanner) Name() string { return "zed" }

func (zedScanner) Presence(platform string) presenceDef {
	return presenceDef{
		binaries:    []string{"zed"},
		appBundles:  []string{"Zed.app"},
		installDirs: []string{"AppData/Local/Programs/Zed", `C:\Program Files\Zed`},
		configPaths: []string{path.Dir(zedSettingsRel(platform))},
	}
}

func (zedScanner) GlobalConfigs(platform string) []string {
	return []string{zedSettingsRel(platform)}
}

func (zedScanner) ProjectConfigs() []string { return []string{".zed/settings.json"} }

func (zedScanner) ScanHome(s *state) observations {
	settingsRel := zedSettingsRel(s.platform)
	cfg, ok := readJSON[zedSettings](s.fsys, settingsRel)
	var configPath string
	if ok {
		configPath = s.addFileOrAbs(settingsRel)
	}
	emitted, servers := emitZedContextServers(cfg.ContextServers, configPath, "")
	servers = append(servers, mergeZedExtensions(s, configPath, emitted)...)
	return observations{servers: servers}
}

func (zedScanner) ScanProject(s *state, configRel string) observations {
	cfg, ok := readJSON[zedSettings](s.fsys, configRel)
	if !ok {
		return observations{}
	}
	configPath := s.addFileOrAbs(configRel)
	projectPath := s.abs(path.Dir(path.Dir(configRel)))
	_, servers := emitZedContextServers(cfg.ContextServers, configPath, projectPath)
	return observations{servers: servers}
}

// emitZedContextServers parses Zed's context_servers map. Returns the
// set of server names emitted (so the extensions merge can dedupe) and
// the observation slice.
func emitZedContextServers(servers map[string]zedContextServer, configPath, projectPath string) (map[string]bool, []types.DeviceScanMCPServer) {
	var (
		emitted = map[string]bool{}
		out     = make([]types.DeviceScanMCPServer, 0, len(servers))
	)
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
		emitted[name] = true
	}
	return emitted, out
}

// toServer parses a Zed context-server entry. URL → sse; command →
// stdio; neither (settings-only extension placeholders) → drop.
func (e zedContextServer) toServer(name, configPath, projectPath string) (types.DeviceScanMCPServer, bool) {
	if e.URL != "" {
		return types.DeviceScanMCPServer{
			Client:      "zed",
			ProjectPath: projectPath,
			File:        configPath,
			Name:        name,
			Transport:   "sse",
			URL:         e.URL,
			EnvKeys:     sortedKeys(e.Env),
			HeaderKeys:  sortedKeys(e.Headers),
			ConfigHash:  mcpConfigHash(name, "sse", "", nil, e.URL),
		}, true
	}
	if e.Command != "" {
		return types.DeviceScanMCPServer{
			Client:      "zed",
			ProjectPath: projectPath,
			File:        configPath,
			Name:        name,
			Transport:   "stdio",
			Command:     e.Command,
			Args:        e.Args,
			EnvKeys:     sortedKeys(e.Env),
			HeaderKeys:  []string{},
			ConfigHash:  mcpConfigHash(name, "stdio", e.Command, e.Args, ""),
		}, true
	}
	return types.DeviceScanMCPServer{}, false
}

// mergeZedExtensions scans the extensions tree for folders prefixed
// with mcp-server- and emits a stdio observation for each name not
// already present in `existing`. The extension itself supplies command/
// args at runtime, so we leave those blank.
func mergeZedExtensions(s *state, configPath string, existing map[string]bool) []types.DeviceScanMCPServer {
	entries, err := fs.ReadDir(s.fsys, zedExtensionsRel(s.platform))
	if err != nil {
		return nil
	}
	var out []types.DeviceScanMCPServer
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, zedExtensionPrefix) || existing[name] {
			continue
		}
		out = append(out, types.DeviceScanMCPServer{
			Client:     "zed",
			File:       configPath,
			Name:       name,
			Transport:  "stdio",
			EnvKeys:    []string{},
			HeaderKeys: []string{},
			ConfigHash: mcpConfigHash(name, "stdio", "", nil, ""),
		})
	}
	return out
}
