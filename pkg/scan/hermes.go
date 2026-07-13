package scan

import "github.com/obot-platform/obot/apiclient/types"

const hermesGlobalConfigRel = ".hermes/config.yaml"

// hermesConfig is Hermes's config.yaml shape: a single top-level
// `mcp_servers` map of named entries.
type hermesConfig struct {
	MCPServers map[string]hermesEntry `yaml:"mcp_servers"`
}

// hermesEntry mirrors mcpServerSpec but with Hermes-specific transport
// rules: `url` implies streamable-http (no explicit type field), and
// `enabled` defaults to true (only honored when explicitly false).
type hermesEntry struct {
	Command string         `yaml:"command"`
	Args    []string       `yaml:"args"`
	URL     string         `yaml:"url"`
	Env     map[string]any `yaml:"env"`
	Headers map[string]any `yaml:"headers"`
	Enabled *bool          `yaml:"enabled"`
}

type hermesScanner struct{}

func (hermesScanner) Name() string { return "hermes" }

func (hermesScanner) Presence(string) presenceDef {
	return presenceDef{binaries: []string{"hermes"}, configPaths: []string{".hermes"}}
}

func (hermesScanner) GlobalConfigs(string) []string { return []string{hermesGlobalConfigRel} }

func (hermesScanner) ProjectConfigs() []string { return nil } // global config only

func (hermesScanner) ScanProject(*state, string) observations { return observations{} }

func (hermesScanner) ScanHome(s *state) observations {
	cfg, ok := readYAML[hermesConfig](s.fsys, hermesGlobalConfigRel)
	if !ok {
		return observations{}
	}
	configPath := s.addFileOrAbs(hermesGlobalConfigRel)

	servers := make([]types.DeviceScanMCPServer, 0, len(cfg.MCPServers))
	for _, name := range sortedKeys(cfg.MCPServers) {
		e := cfg.MCPServers[name]
		if e.Enabled != nil && !*e.Enabled {
			continue
		}
		obs, ok := e.toServer(name, configPath)
		if !ok {
			continue
		}
		servers = append(servers, obs)
	}
	return observations{servers: servers}
}

// toServer materializes a Hermes entry. Returns ok=false for entries
// with neither command nor url (settings-only stubs).
func (e hermesEntry) toServer(name, configPath string) (types.DeviceScanMCPServer, bool) {
	if e.Command != "" {
		return types.DeviceScanMCPServer{
			Client:     "hermes",
			File:       configPath,
			Name:       name,
			Transport:  "stdio",
			Command:    e.Command,
			Args:       e.Args,
			EnvKeys:    sortedKeys(e.Env),
			HeaderKeys: []string{},
			ConfigHash: mcpConfigHash(name, "stdio", e.Command, e.Args, ""),
		}, true
	}
	if e.URL != "" {
		return types.DeviceScanMCPServer{
			Client:     "hermes",
			File:       configPath,
			Name:       name,
			Transport:  "streamable-http",
			URL:        e.URL,
			EnvKeys:    sortedKeys(e.Env),
			HeaderKeys: sortedKeys(e.Headers),
			ConfigHash: mcpConfigHash(name, "streamable-http", "", nil, e.URL),
		}, true
	}
	return types.DeviceScanMCPServer{}, false
}
