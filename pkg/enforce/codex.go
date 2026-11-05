package enforce

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
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
		if isCodexBundledNodeREPL(env, m.key, m.entry) && codexNodeREPLIsSole(ctx, loader, env) {
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
// inside the Codex app, run with no arguments, and with no environment — set or
// forwarded — that could make that executable load other code. codexNodeREPLIsSole
// adds the configuration-wide conditions.
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
	// Codex Desktop writes args = []. Any argument could be a script path or a
	// Node flag that runs something other than the REPL.
	if len(entry.Args) != 0 {
		return false
	}
	// env_vars forwards variables from Codex's own environment, which the user
	// controls, so NODE_OPTIONS could arrive that way instead of through env.
	if len(entry.EnvVars) != 0 {
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

// codexNodeREPLNativePipe is the one Node-prefixed variable Codex Desktop itself
// sets on node_repl (on Windows): the pipe it talks to the app over.
const codexNodeREPLNativePipe = "NODE_REPL_NATIVE_PIPE"

// nodeREPLEnvChangesExecution reports whether a configured environment variable
// could make the bundled node_repl run something other than what Codex shipped:
// the loader and search-path variables every runner check rejects, and every
// NODE_* variable but the pipe Codex sets. Node reads many of those
// (NODE_OPTIONS, NODE_PATH, NODE_REPL_EXTERNAL_MODULE, ...), and an unknown one
// is refused rather than guessed about.
func nodeREPLEnvChangesExecution(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	if _, ok := commonConfiguredPackageEnv[name]; ok {
		return true
	}
	return strings.HasPrefix(name, "NODE_") && name != codexNodeREPLNativePipe
}

// codexNodeREPLIsSole reports whether the matched node_repl entry is the only
// definition Codex could be running, so that what was checked is what runs.
//
// The resolver ranks Codex's files and takes the first match, but Codex layers
// them and merges tables field by field: a pristine entry in config.toml plus a
// [mcp_servers.node_repl.env] table in the user-writable managed_config.toml
// would run with that env while the check saw none. And a second key that
// folds to node_repl in the tool namespace (node-repl) leaves it unclear which
// server a call reached. So every Codex file must be readable, exactly one key
// across all of them may name node_repl in any form, and CODEX_HOME must not
// point Codex at a configuration directory this resolver never read.
func codexNodeREPLIsSole(ctx context.Context, loader *configLoader, env Env) bool {
	if home := strings.TrimSpace(env.getenv("CODEX_HOME")); home != "" &&
		filepath.Clean(home) != filepath.Clean(env.homePath(".codex")) {
		return false
	}
	definitions := 0
	for _, s := range codexScopes(loader, env) {
		set, res := s.load(ctx)
		switch res {
		case loadOK:
		case loadAbsent:
			continue
		default:
			return false
		}
		for key := range set {
			if key == codexNodeREPLKey || formCodex(key) == codexNodeREPLKey {
				definitions++
			}
		}
	}
	return definitions == 1
}
