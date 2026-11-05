package enforce

import (
	"context"
	"fmt"
	"path"
	"strings"

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
func resolveCodex(ctx context.Context, loader *configLoader, env Env, serverName string, tr *tracer) Resolution {
	// Codex folds punctuation to underscores on the way to a tool namespace, so a
	// hyphenated config key arrives as probe_npx_stdio and an exact miss is retried
	// against the normalized form.
	names := lookup{names: []string{serverName}, form: formCodex}
	m, out := resolveScopes(ctx, codexScopes(loader, env), names, tr)
	switch out {
	case outcomeFound:
		if isCodexBundledNodeREPL(env, m.key, m.entry) {
			// Reported by name with no identity, exactly as a Claude Code built-in is:
			// the name is what Obot's built-in agent MCP toggle matches on.
			return Resolution{ServerName: m.key}
		}
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
func codexScopes(loader *configLoader, env Env) []scope {
	paths := env.codexConfigPaths()
	scopes := make([]scope, 0, len(paths))
	for i, path := range paths {
		scopes = append(scopes, scope{
			path: path,
			key:  codexServersKey,
			rank: i,
			load: codexServers(loader, path),
		})
	}
	return scopes
}

// codexNodeREPLKey is the config key Codex Desktop writes for node_repl, the
// JavaScript REPL its built-in browser control (Browser Use) runs on. Every
// browser action arrives as a call to its js tool.
const codexNodeREPLKey = "node_repl"

// isCodexBundledNodeREPL reports whether a matched Codex config entry is the
// node_repl server Codex Desktop itself installs, as opposed to anything else a
// user has given that name.
//
// Unlike a Claude Code built-in, node_repl is an ordinary [mcp_servers.node_repl]
// entry in ~/.codex/config.toml, and Codex will happily accept a user-defined
// server under the same key. So the name alone proves nothing, and what is
// checked instead is the entry itself: a command that is the executable shipped
// inside the Codex app, with no configured environment that could make that
// executable load other code.
//
//   - macOS: /Applications/Codex.app/Contents/Resources/.../node_repl. The
//     system Applications folder is admin-writable only; ~/Applications is not
//     accepted, because any process running as the user could plant a bundle
//     there.
//   - Windows: %LOCALAPPDATA%\OpenAI\Codex\runtimes\...\node_repl.exe. This is
//     where Codex Desktop installs its runtimes, and it is user-writable; there
//     is no admin-owned location to require instead.
//
// Every other platform has no Codex Desktop, and so no bundled node_repl.
func isCodexBundledNodeREPL(env Env, key string, entry mcpEntry) bool {
	if key != codexNodeREPLKey || strings.TrimSpace(entry.URL) != "" {
		return false
	}
	command := strings.TrimSpace(entry.Command)
	if command == "" || strings.ContainsAny(command, "\x00\n") {
		return false
	}
	for name := range entry.Environment {
		if nodeREPLEnvChangesExecution(name) {
			return false
		}
	}

	switch env.GOOS {
	case "darwin":
		return bundledUnder(path.Clean(command), "/Applications/Codex.app/Contents/Resources/", "node_repl", false)
	case "windows":
		base := strings.TrimSpace(env.getenv("LOCALAPPDATA"))
		if !isWindowsAbs(base) {
			return false
		}
		root := windowsClean(base) + `/OpenAI/Codex/runtimes/`
		return bundledUnder(windowsClean(command), root, "node_repl.exe", true)
	default:
		return false
	}
}

// bundledUnder reports whether the cleaned, slash-separated command lies
// strictly below root and names the executable base.
func bundledUnder(command, root, base string, foldCase bool) bool {
	if foldCase {
		command, root, base = strings.ToLower(command), strings.ToLower(root), strings.ToLower(base)
	}
	return strings.HasPrefix(command, root) && path.Base(command) == base
}

// windowsClean converts a Windows path to slash-separated form and cleans it, so
// a prefix comparison cannot be defeated by ".." segments or doubled separators.
func windowsClean(p string) string {
	return path.Clean(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"))
}

// isWindowsAbs reports whether p is a drive-rooted Windows path such as
// C:\Users\dev. UNC and drive-relative paths are not accepted.
func isWindowsAbs(p string) bool {
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z'))
}

// nodeREPLEnvChangesExecution reports whether a configured environment variable
// could make the bundled node_repl run something other than what Codex shipped:
// the loader and search-path variables every runner check rejects, and the Node
// variables that inject code or modules.
func nodeREPLEnvChangesExecution(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	if _, ok := commonConfiguredPackageEnv[name]; ok {
		return true
	}
	_, ok := npxExactPackageEnv[name]
	return ok
}
