package enforce

import (
	"fmt"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

// codexConfigPaths returns the Codex config files to consult, in order.
//
// The Windows entry sits beside where obot-sentry writes the Codex hook
// (%ProgramData%\OpenAI\Codex\requirements.toml), which is why the machine
// managed config belongs there rather than at a Unix path that has nowhere to
// run.
func (e Env) codexConfigPaths() []string {
	paths := []string{
		e.homePath(".codex", "config.toml"),
		e.homePath(".codex", "managed_config.toml"),
	}
	if e.windows() {
		return append(paths, e.envPath("ProgramData", `C:\ProgramData`, "OpenAI", "Codex", "managed_config.toml"))
	}
	return append(paths, e.machinePath("/etc/codex/managed_config.toml"))
}

// resolveCodex resolves a Codex server name against the mcp_servers table of each
// Codex config file.
func resolveCodex(env Env, serverName string, tr *tracer) Resolution {
	// Codex folds punctuation to underscores on the way to a tool namespace, so a
	// hyphenated config key arrives as probe_npx_stdio and an exact miss is retried
	// against the normalized form.
	names := lookup{names: []string{serverName}, form: formCodex}
	m, out := resolveScopes(codexScopes(env), names, tr)
	switch out {
	case outcomeFound:
		// The matched key, not serverName, which for a folded name appears nowhere in
		// the user's configuration.
		return resolved(env, m.key, m.entry)
	case outcomeAmbiguous:
		return ambiguous(localagent.Codex, serverName)
	default:
		return notFound(localagent.Codex, serverName, fmt.Sprintf(
			"MCP server %q was not found in any Codex MCP configuration", serverName))
	}
}

// codexScopes returns the Codex config files as ranked scopes. Codex takes no
// project-scoped MCP configuration, so cwd plays no part.
func codexScopes(env Env) []scope {
	paths := env.codexConfigPaths()
	scopes := make([]scope, 0, len(paths))
	for i, path := range paths {
		scopes = append(scopes, scope{
			path: path,
			key:  codexServersKey,
			rank: i,
			load: codexServers(path),
		})
	}
	return scopes
}
