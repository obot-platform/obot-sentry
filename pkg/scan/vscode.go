package scan

import "path"

// vscodeUserDir returns the home-relative VS Code user configuration
// directory holding the user-level mcp.json (default profile):
// https://code.visualstudio.com/docs/configure/settings
func vscodeUserDir(platform string) string {
	switch platform {
	case "darwin":
		return "Library/Application Support/Code/User"
	case "windows":
		return "AppData/Roaming/Code/User" // %APPDATA%\Code\User
	default:
		return ".config/Code/User"
	}
}

type vscodeScanner struct{}

func (vscodeScanner) Name() string { return "vscode" }

func (vscodeScanner) Presence(platform string) presenceDef {
	return presenceDef{
		binaries:   []string{"code"},
		appBundles: []string{"Visual Studio Code.app"},
		installDirs: []string{
			"AppData/Local/Programs/Microsoft VS Code", // user setup
			`C:\Program Files\Microsoft VS Code`,       // system setup
		},
		configPaths: []string{".vscode", path.Dir(vscodeUserDir(platform))},
	}
}

func (vscodeScanner) GlobalConfigs(platform string) []string {
	return []string{path.Join(vscodeUserDir(platform), "mcp.json")}
}

func (vscodeScanner) ProjectConfigs() []string { return []string{".vscode/mcp.json"} }

// VS Code uses "servers" rather than "mcpServers" for both global and
// project configs; entries follow the standard JSON shape:
// https://code.visualstudio.com/docs/copilot/customization/mcp-servers
func (vscodeScanner) ScanHome(s *state) observations {
	configRel := path.Join(vscodeUserDir(s.platform), "mcp.json")
	return observations{servers: emitJSONServers(s, configRel, "servers", "vscode", "")}
}

func (vscodeScanner) ScanProject(s *state, configRel string) observations {
	projectPath := s.abs(path.Dir(path.Dir(configRel)))
	return observations{servers: emitJSONServers(s, configRel, "servers", "vscode", projectPath)}
}
