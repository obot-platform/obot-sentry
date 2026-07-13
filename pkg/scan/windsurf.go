package scan

import "path"

// Windsurf keeps its MCP config under ~/.codeium on every platform:
// https://docs.windsurf.com/windsurf/cascade/mcp
const windsurfGlobalConfigRel = ".codeium/windsurf/mcp_config.json"

type windsurfScanner struct{}

func (windsurfScanner) Name() string { return "windsurf" }

func (windsurfScanner) Presence(string) presenceDef {
	return presenceDef{
		binaries:    []string{"windsurf"},
		appBundles:  []string{"Windsurf.app"},
		installDirs: []string{"AppData/Local/Programs/Windsurf", `C:\Program Files\Windsurf`},
		configPaths: []string{".windsurf", ".codeium"},
	}
}

func (windsurfScanner) GlobalConfigs(string) []string { return []string{windsurfGlobalConfigRel} }

func (windsurfScanner) ProjectConfigs() []string {
	return []string{".windsurf/mcp_config.json"}
}

func (windsurfScanner) ScanHome(s *state) observations {
	return observations{servers: emitJSONServers(s, windsurfGlobalConfigRel, "mcpServers", "windsurf", "")}
}

func (windsurfScanner) ScanProject(s *state, configRel string) observations {
	projectPath := s.abs(path.Dir(path.Dir(configRel)))
	return observations{servers: emitJSONServers(s, configRel, "mcpServers", "windsurf", projectPath)}
}
