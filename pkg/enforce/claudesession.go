package enforce

import (
	"fmt"
	"strconv"
	"strings"
)

// A plugin server's mcp_server reads "plugin:<plugin>:<server>" with source
// plugin whether Claude Code loaded the plugin from installed_plugins.json or
// for this session only: from --plugin-dir, --plugin-dir-no-mcp or --plugin-url,
// from CLAUDE_CODE_PLUGIN_DIRS, or from an Agent SDK host's initialize request
// on stdin. A session plugin named like an installed one replaces it (verified
// against Claude Code 2.1.311), so its server would resolve to the installed
// copy's definition. A plugin server is resolved only for a session that can
// have loaded none of those.
const (
	claudePIDEnv        = "CLAUDE_PID"
	claudePluginDirsEnv = "CLAUDE_CODE_PLUGIN_DIRS"
	// maxClaudeAncestors bounds the walk from the hook up to the Claude Code
	// process, which launches the hook directly or through a shell.
	maxClaudeAncestors = 16
)

// claudeSessionPluginFlags are the Claude Code flags that load a plugin for the
// session.
var claudeSessionPluginFlags = []string{"--plugin-dir", "--plugin-dir-no-mcp", "--plugin-url"}

// claudeSessionPlugins reports why the calling Claude Code session may have
// loaded plugins that installed_plugins.json does not record, or "" when it
// cannot have.
//
// Claude Code gives a hook its own process ID in CLAUDE_PID. The variable is
// trusted only for a process the hook descends from, whose arguments are then
// read. A session the resolver cannot inspect this way may have loaded any
// plugin.
func claudeSessionPlugins(env Env) string {
	if env.getenv(claudePluginDirsEnv) != "" {
		return claudePluginDirsEnv + " is set"
	}
	pid, err := strconv.Atoi(env.getenv(claudePIDEnv))
	if err != nil || pid <= 0 {
		return "the hook could not identify the Claude Code process (" + claudePIDEnv + " is not set)"
	}
	procs := env.processes()
	ancestor := false
	for p, i := procs.self(), 0; i < maxClaudeAncestors && p > 0; i++ {
		parent, err := procs.parent(p)
		if err != nil {
			break
		}
		if parent == pid {
			ancestor = true
			break
		}
		if parent == p {
			break
		}
		p = parent
	}
	if !ancestor {
		return fmt.Sprintf("the hook could not confirm Claude Code process %d is the one that ran it", pid)
	}
	args, err := procs.args(pid)
	if err != nil || len(args) == 0 {
		return fmt.Sprintf("the hook could not read Claude Code process %d's arguments", pid)
	}
	for i, arg := range args[1:] {
		for _, flag := range claudeSessionPluginFlags {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return "Claude Code was started with " + flag
			}
		}
		// An Agent SDK host talks to Claude Code over stream-json, and can load
		// plugins through its initialize request.
		if arg == "--input-format=stream-json" || arg == "--input-format" && i+2 < len(args) && args[i+2] == "stream-json" {
			return "Claude Code was started by an Agent SDK host, which can load plugins for the session"
		}
	}
	return ""
}
