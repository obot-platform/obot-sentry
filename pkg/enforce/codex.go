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
// inside the app, run with no arguments, with nothing forwarded from Codex's own
// environment, and with only the environment Codex Desktop itself writes (see
// nodeREPLEnvAllowed). codexNodeREPLIsSole adds the configuration-wide
// conditions.
//
//   - macOS: /Applications/ChatGPT.app/Contents/Resources/.../node_repl, where
//     Codex Desktop now ships inside the ChatGPT app, or the same path under the
//     older standalone /Applications/Codex.app. The system Applications folder is
//     admin-writable only; ~/Applications is not accepted, because any process
//     running as the user could plant a bundle there.
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

	bundle, ok := codexNodeREPLBundleFor(env, command)
	if !ok {
		return false
	}
	for name, value := range entry.Environment {
		if !nodeREPLEnvAllowed(env, bundle, name, value) {
			return false
		}
	}
	return true
}

// codexNodeREPLMacBundles are the app bundles Codex Desktop's node_repl ships
// in on macOS. Codex Desktop now ships inside the ChatGPT app; the standalone
// Codex app is the older layout.
var codexNodeREPLMacBundles = []string{
	"/Applications/ChatGPT.app/Contents/Resources/",
	"/Applications/Codex.app/Contents/Resources/",
}

// nodeREPLBundle is the directory the bundled node_repl was found in. Paths in
// node_repl's environment that name code to run must lie within the same one.
type nodeREPLBundle struct {
	// root is cleaned, slash-separated, and ends in a slash.
	root    string
	windows bool
}

// clean normalizes p for a prefix comparison against root, so ".." segments
// and doubled separators cannot defeat it. It reports false for a path that is
// not absolute.
func (b nodeREPLBundle) clean(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsAny(p, "\x00\n") {
		return "", false
	}
	if b.windows {
		if !isWindowsAbs(p) {
			return "", false
		}
		return strings.ToLower(windowsClean(p)), true
	}
	if !strings.HasPrefix(p, "/") {
		return "", false
	}
	return path.Clean(p), true
}

// contains reports whether p lies strictly below the bundle root.
func (b nodeREPLBundle) contains(p string) bool {
	p, ok := b.clean(p)
	return ok && strings.HasPrefix(p, b.root)
}

// codexNodeREPLBundleFor returns the bundle that command is Codex Desktop's
// node_repl executable in, if it is one.
func codexNodeREPLBundleFor(env Env, command string) (nodeREPLBundle, bool) {
	switch env.GOOS {
	case "darwin":
		for _, root := range codexNodeREPLMacBundles {
			b := nodeREPLBundle{root: root}
			if b.contains(command) && path.Base(path.Clean(command)) == "node_repl" {
				return b, true
			}
		}
	case "windows":
		base := strings.TrimSpace(env.getenv("LOCALAPPDATA"))
		if !isWindowsAbs(base) {
			return nodeREPLBundle{}, false
		}
		b := nodeREPLBundle{root: strings.ToLower(windowsClean(base)) + "/openai/codex/runtimes/", windows: true}
		if cleaned, ok := b.clean(command); ok && b.contains(command) && path.Base(cleaned) == "node_repl.exe" {
			return b, true
		}
	}
	return nodeREPLBundle{}, false
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

// listSeparator is the PATH-style list separator on env's platform.
func (e Env) listSeparator() string {
	if e.windows() {
		return ";"
	}
	return ":"
}

// isDefaultCodexHome reports whether p names Codex's default configuration
// directory, ~/.codex: the only one this resolver reads.
func isDefaultCodexHome(env Env, p string) bool {
	p = strings.TrimSpace(p)
	return p != "" && filepath.Clean(p) == filepath.Clean(env.homePath(".codex"))
}

// nodeREPLEnvAllowed reports whether a variable in node_repl's configured
// environment leaves it running only what the app shipped.
//
// Codex Desktop configures node_repl with a set of NODE_REPL_* variables. The
// ones that name code to run must point inside the same bundle as node_repl
// itself (the code it trusts may also be Codex's own home); the rest carry data
// (timeouts, hashes, instruction text) and are accepted as they are. Every
// other NODE_* variable is refused, known (NODE_OPTIONS, NODE_PATH,
// NODE_REPL_EXTERNAL_MODULE) or not, rather than guessed about, so a variable a
// future Codex release adds keeps node_repl denied until it is listed here. The
// loader and search-path variables every runner check rejects are refused too.
func nodeREPLEnvAllowed(env Env, bundle nodeREPLBundle, name, value string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	if _, ok := commonConfiguredPackageEnv[name]; ok {
		return false
	}
	switch {
	case name == "NODE_REPL_NODE_PATH":
		// The node binary node_repl runs code with.
		return bundle.contains(value)
	case name == "NODE_REPL_NODE_MODULE_DIRS":
		// Where node_repl resolves modules from.
		return allListItems(value, env.listSeparator(), bundle.contains)
	case name == "NODE_REPL_TRUSTED_CODE_PATHS":
		// Code node_repl trusts: Codex's home, and in some Codex setups the
		// bundle's own modules as well.
		return allListItems(value, env.listSeparator(), func(p string) bool {
			return isDefaultCodexHome(env, p) || bundle.contains(p)
		})
	case name == "CODEX_HOME":
		return isDefaultCodexHome(env, value)
	case name == "NODE_REPL_TRUSTED_BROWSER_CLIENT_SHA256S":
		return allListItems(value, ",", isSHA256Hex)
	case name == "NODE_REPL_NATIVE_PIPE",
		name == "NODE_REPL_NATIVE_PIPE_CONNECT_TIMEOUT_MS",
		strings.HasPrefix(name, "NODE_REPL_INSTRUCTIONS_"):
		return true
	case strings.HasPrefix(name, "NODE_"):
		return false
	}
	return true
}

// allListItems reports whether value is a non-empty sep-separated list whose
// every item satisfies ok.
func allListItems(value, sep string, ok func(string) bool) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	for _, item := range strings.Split(value, sep) {
		if !ok(strings.TrimSpace(item)) {
			return false
		}
	}
	return true
}

// isSHA256Hex reports whether s is a hex-encoded SHA-256 digest.
func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
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
	if home := env.getenv("CODEX_HOME"); strings.TrimSpace(home) != "" && !isDefaultCodexHome(env, home) {
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
