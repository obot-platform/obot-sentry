package scan

import (
	"strings"

	"github.com/obot-platform/obot/apiclient/types"
)

// gooseGlobalConfigRel returns Goose's config.yaml location: ~/.config
// on macOS and Linux, %APPDATA%\Block\goose\config on Windows:
// https://github.com/block/goose/blob/main/documentation/docs/guides/config-files.md
func gooseGlobalConfigRel(platform string) string {
	if platform == "windows" {
		return "AppData/Roaming/Block/goose/config/config.yaml"
	}
	return ".config/goose/config.yaml"
}

func gooseConfigDir(platform string) string {
	if platform == "windows" {
		return "AppData/Roaming/Block/goose"
	}
	return ".config/goose"
}

// gooseConfig is Goose's config.yaml shape: a top-level `extensions`
// map. Goose uses non-standard field names (cmd/envs/uri instead of
// command/env/url) and gates every entry on a required `enabled: true`.
type gooseConfig struct {
	Extensions map[string]gooseExtension `yaml:"extensions"`
}

type gooseExtension struct {
	Type    string         `yaml:"type"`
	Name    string         `yaml:"name"`
	Cmd     string         `yaml:"cmd"`
	Args    []string       `yaml:"args"`
	URI     string         `yaml:"uri"`
	Envs    map[string]any `yaml:"envs"`
	Headers map[string]any `yaml:"headers"`
	Enabled bool           `yaml:"enabled"`
}

type gooseScanner struct{}

func (gooseScanner) Name() string { return "goose" }

func (gooseScanner) Presence(platform string) presenceDef {
	return presenceDef{binaries: []string{"goose"}, configPaths: []string{gooseConfigDir(platform)}}
}

func (gooseScanner) GlobalConfigs(platform string) []string {
	return []string{gooseGlobalConfigRel(platform)}
}

func (gooseScanner) ProjectConfigs() []string { return nil }

func (gooseScanner) ScanProject(*state, string) observations { return observations{} }

func (gooseScanner) ScanHome(s *state) observations {
	configRel := gooseGlobalConfigRel(s.platform)
	cfg, ok := readYAML[gooseConfig](s.fsys, configRel)
	if !ok {
		return observations{}
	}
	configPath := s.addFileOrAbs(configRel)

	servers := make([]types.DeviceScanMCPServer, 0, len(cfg.Extensions))
	for _, key := range sortedKeys(cfg.Extensions) {
		ext := cfg.Extensions[key]
		if !ext.Enabled {
			continue
		}
		obs, ok := ext.toServer(key, configPath)
		if !ok {
			continue
		}
		servers = append(servers, obs)
	}
	return observations{servers: servers}
}

// toServer materializes a Goose extension. Only stdio/sse/streamable_http
// types are surfaced (other types are MCP-irrelevant).
func (e gooseExtension) toServer(key, configPath string) (types.DeviceScanMCPServer, bool) {
	switch e.Type {
	case "stdio", "sse", "streamable_http":
	default:
		return types.DeviceScanMCPServer{}, false
	}
	name := key
	if e.Name != "" {
		name = e.Name
	}

	if e.Type == "stdio" {
		return types.DeviceScanMCPServer{
			Client:     "goose",
			File:       configPath,
			Name:       name,
			Transport:  "stdio",
			Command:    e.Cmd,
			Args:       e.Args,
			EnvKeys:    sortedKeys(e.Envs),
			HeaderKeys: []string{},
			ConfigHash: mcpConfigHash(name, "stdio", e.Cmd, e.Args, ""),
		}, true
	}

	transport := strings.ReplaceAll(e.Type, "_", "-")
	return types.DeviceScanMCPServer{
		Client:     "goose",
		File:       configPath,
		Name:       name,
		Transport:  transport,
		URL:        e.URI,
		EnvKeys:    sortedKeys(e.Envs),
		HeaderKeys: sortedKeys(e.Headers),
		ConfigHash: mcpConfigHash(name, transport, "", nil, e.URI),
	}, true
}
