package scan

import (
	workbuddy "github.com/obot-platform/obot-sentry/pkg/workbuddy"
	"github.com/obot-platform/obot/apiclient/types"
)

// workBuddyConfig is the user-level WorkBuddy MCP document. In addition to
// the user-scope mcpServers map, WorkBuddy stores local-scope entries under
// projects[absolute-project-path].mcpServers in the same file.
type workBuddyConfig struct {
	MCPServers         map[string]mcpServerSpec `json:"mcpServers"`
	DisabledMcpServers []string                 `json:"disabledMcpServers"`
	Projects           map[string]struct {
		MCPServers         map[string]mcpServerSpec `json:"mcpServers"`
		DisabledMcpServers []string                 `json:"disabledMcpServers"`
	} `json:"projects"`
}

const workBuddyUserConfigRel = workbuddy.DefaultUserMCPPathRel

var workBuddyUserConfigFallbacks = workbuddy.DefaultUserMCPPathRels()

// workBuddyServers reads WorkBuddy's user-level MCP configuration and emits
// both user-scope and project-local entries. WorkBuddy uses a first-existing
// file rule for these compatibility names; reading all of them would report
// servers the product itself never loads. Project keys are native-form
// absolute paths; re-anchor them onto a WSL/scan root when possible so the
// inventory uses the same project identity as the filesystem walk.
func workBuddyServers(s *state, _ string, _ string) observations {
	rel := workBuddyUserConfigRel
	for _, candidate := range workBuddyUserConfigFallbacks {
		if fileExists(s.fsys, candidate) {
			rel = candidate
			break
		}
	}
	cfg, ok := readJSON[workBuddyConfig](s.fsys, rel)
	if !ok {
		return observations{}
	}
	configPath := s.addFileOrAbs(rel)
	disabled := make(map[string]struct{}, len(cfg.DisabledMcpServers))
	for _, name := range cfg.DisabledMcpServers {
		disabled[name] = struct{}{}
	}
	servers := make([]types.DeviceScanMCPServer, 0, len(cfg.MCPServers))
	for _, name := range sortedKeys(cfg.MCPServers) {
		if _, isDisabled := disabled[name]; isDisabled {
			continue
		}
		if entry := cfg.MCPServers[name]; !entry.disabled() {
			servers = append(servers, entry.toServer(name, "workbuddy", configPath, ""))
		}
	}
	for _, projectKey := range sortedKeys(cfg.Projects) {
		projectPath := projectKey
		if rel, ok := s.relToHome(projectKey); ok {
			projectPath = s.abs(rel)
		}
		project := cfg.Projects[projectKey]
		projectDisabled := make(map[string]struct{}, len(project.DisabledMcpServers))
		for _, name := range project.DisabledMcpServers {
			projectDisabled[name] = struct{}{}
		}
		for _, name := range sortedKeys(project.MCPServers) {
			if _, isDisabled := projectDisabled[name]; isDisabled {
				continue
			}
			if entry := project.MCPServers[name]; !entry.disabled() {
				servers = append(servers, entry.toServer(name, "workbuddy", configPath, projectPath))
			}
		}
	}
	return observations{servers: servers}
}

func workBuddyProjectServers(s *state, rel, projectPath string) observations {
	cfg, ok := readJSON[workBuddyConfig](s.fsys, rel)
	if !ok {
		return observations{}
	}
	configPath := s.addFileOrAbs(rel)
	disabled := make(map[string]struct{}, len(cfg.DisabledMcpServers))
	for _, name := range cfg.DisabledMcpServers {
		disabled[name] = struct{}{}
	}
	servers := make([]types.DeviceScanMCPServer, 0, len(cfg.MCPServers))
	for _, name := range sortedKeys(cfg.MCPServers) {
		if _, isDisabled := disabled[name]; isDisabled {
			continue
		}
		if entry := cfg.MCPServers[name]; !entry.disabled() {
			servers = append(servers, entry.toServer(name, "workbuddy", configPath, projectPath))
		}
	}
	return observations{servers: servers}
}
