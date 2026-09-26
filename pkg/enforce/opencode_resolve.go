package enforce

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

// openCodeConfig is the part of an OpenCode configuration document used during
// enforcement. OpenCode declares MCP servers under "mcp"; that is the only
// key read here, because accepting any other spelling would resolve a server
// OpenCode itself never starts.
type openCodeConfig struct {
	MCP map[string]json.RawMessage `json:"mcp"`
}

// openCodeMCPEntry is one OpenCode server definition. A local server runs a
// command array; a remote server has a URL. Headers are deliberately not read:
// they carry credentials that must never reach a decision request.
type openCodeMCPEntry struct {
	Type        string            `json:"type"`
	URL         string            `json:"url"`
	Command     json.RawMessage   `json:"command"`
	Environment map[string]string `json:"environment"`
	Enabled     *bool             `json:"enabled"`
}

// resolveOpenCode resolves an OpenCode MCP server from the same configuration
// files the OpenCode client reads. It never guesses a server from a lossy tool
// name: a missing or conflicting definition stays unresolved so Obot refuses
// the call.
func resolveOpenCode(ctx context.Context, loader *configLoader, env Env, req ResolveRequest, serverName string, tr *tracer) Resolution {
	scopes := openCodeScopes(ctx, loader, env, req.CWD)
	match, outcome := resolveScopes(ctx, scopes, lookup{names: []string{serverName}, form: formOpenCode}, tr)
	switch outcome {
	case outcomeFound:
		return resolved(env, match.key, match.entry)
	case outcomeAmbiguous:
		return ambiguous(localagent.OpenCode, serverName)
	default:
		return notFound(localagent.OpenCode, serverName, fmt.Sprintf(
			"MCP server %q was not found in OpenCode configuration", serverName))
	}
}

// openCodeServerCandidates returns every configured server namespace that could
// have produced action, in the "<server>_" form OpenCode exposes tools under.
//
// The second result reports a namespace collision: two distinct configured names
// that fold to the same string, such as "a.b" and "a_b". The action cannot tell
// them apart, so the caller must refuse rather than choose.
//
// This reads OpenCode's own configuration rather than any caller-supplied
// server hint. A client that named its own candidate could omit a longer
// allowlisted server and make the call resolve as a shorter one, so the
// enumeration here is the only input that can be trusted.
func openCodeServerCandidates(ctx context.Context, env Env, action, cwd string) (candidates []string, ambiguous bool, err error) {
	loader := newConfigLoader()
	servers, err := openCodeConfiguredServers(ctx, loader, env, cwd)
	if err != nil {
		return nil, false, err
	}

	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	slices.Sort(names)

	// Count the raw names behind each namespace first: a name that folds onto
	// another is a collision, not a match.
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		namespace := formOpenCode(name)
		if namespace == "" {
			continue
		}
		if _, ok := seen[namespace]; ok {
			// Two configuration keys, one namespace. Refuse this namespace
			// rather than resolving to whichever key sorted first.
			if strings.HasPrefix(action, namespace+"_") {
				return nil, true, nil
			}
			continue
		}
		seen[namespace] = struct{}{}
		if strings.HasPrefix(action, namespace+"_") {
			candidates = append(candidates, namespace)
		}
	}
	return candidates, false, nil
}

// openCodeConfiguredServers merges the visible configuration into one server
// table. A scope that cannot be read fails closed rather than falling through to
// a lower-precedence definition, because falling through could resolve a server
// that this project had deliberately replaced.
func openCodeConfiguredServers(ctx context.Context, loader *configLoader, env Env, cwd string) (serverSet, error) {
	merged := serverSet{}
	for _, scope := range openCodeScopes(ctx, loader, env, cwd) {
		set, res := scope.load(ctx)
		switch res {
		case loadOK:
			for name, entry := range set {
				merged[name] = entry
			}
		case loadUnusable:
			return nil, fmt.Errorf("%s could not be read", scope.path)
		}
	}
	return merged, nil
}

// openCodeScopes puts the nearest project configuration ahead of the global one,
// matching the precedence the OpenCode client applies.
func openCodeScopes(ctx context.Context, loader *configLoader, env Env, cwd string) []scope {
	scopes := make([]scope, 0, 2)
	if path, ok := nearestOpenCodeConfig(ancestors(cwd)); ok {
		scopes = append(scopes, scope{
			path: path,
			key:  "mcp",
			rank: 0,
			load: openCodeConfigServers(loader, path, env),
		})
	}
	userPath, user, userRes := loadOpenCodeUserConfig(ctx, loader, env)
	return append(scopes, scope{
		path: userPath,
		key:  "mcp",
		rank: 1,
		load: fixedServers(decodeOpenCodeServers(user, env), userRes),
	})
}

func openCodeConfigServers(loader *configLoader, path string, env Env) func(context.Context) (serverSet, loadResult) {
	return func(ctx context.Context) (serverSet, loadResult) {
		if ctx.Err() != nil {
			return nil, loadUnusable
		}
		var doc openCodeConfig
		res := loader.loadJSON(ctx, path, &doc)
		if res != loadOK {
			return nil, res
		}
		return decodeOpenCodeServers(doc, env), loadOK
	}
}

func decodeOpenCodeServers(doc openCodeConfig, env Env) serverSet {
	set := make(serverSet, len(doc.MCP))
	for name, message := range doc.MCP {
		// A malformed entry stays in the table so it can mask a
		// lower-priority definition, and resolves to an identity-free server.
		var entry openCodeMCPEntry
		if err := json.Unmarshal(message, &entry); err != nil {
			set[name] = mcpEntry{}
			continue
		}
		if entry.Enabled != nil && !*entry.Enabled {
			set[name] = mcpEntry{Disabled: true}
			continue
		}

		// OpenCode's schema is exact: a local server has a command array, a
		// remote server has a URL. An entry with neither, or with the wrong
		// shape for its declared type, is one OpenCode would not start, so it
		// must not resolve to an identity either.
		converted := mcpEntry{Environment: entry.Environment}
		switch entry.Type {
		case "remote":
			converted.URL = strings.TrimSpace(entry.URL)
		case "local", "":
			converted.Command, converted.Args = decodeOpenCodeCommand(entry.Command)
			converted.URL = strings.TrimSpace(entry.URL)
		default:
			set[name] = mcpEntry{}
			continue
		}
		if converted.URL == "" && converted.Command == "" {
			set[name] = converted
			continue
		}
		// An undefined variable must make the entry unresolved rather than
		// silently changing what would run, so the expansion records why.
		set[name] = expandOpenCodeEntry(converted, env)
	}
	return set
}

// openCodeEnvPattern matches the ${NAME} and ${NAME:-fallback} forms OpenCode
// accepts inside a server command or URL.
var openCodeEnvPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// expandOpenCodeEntry substitutes environment references in an entry. A bare
// ${NAME} that is unset records a ConfigError rather than collapsing to an empty
// string, because an empty command or URL is a different server than the one the
// operator wrote.
func expandOpenCodeEntry(entry mcpEntry, env Env) mcpEntry {
	note := func(name string) {
		if entry.ConfigError == "" {
			entry.ConfigError = fmt.Sprintf("environment variable %s is not set", name)
		}
	}
	entry.URL = openCodeEnvPattern.ReplaceAllStringFunc(entry.URL, func(match string) string {
		groups := openCodeEnvPattern.FindStringSubmatch(match)
		name, fallback := groups[1], groups[2]
		if value := env.getenv(name); value != "" {
			return value
		}
		// ":-" is the only form with a fallback; its presence in the raw match is
		// what distinguishes it from a bare ${NAME}.
		if strings.Contains(match, ":-") {
			return fallback
		}
		note(name)
		return ""
	})
	entry.Command = openCodeEnvPattern.ReplaceAllStringFunc(entry.Command, func(match string) string {
		groups := openCodeEnvPattern.FindStringSubmatch(match)
		name, fallback := groups[1], groups[2]
		if value := env.getenv(name); value != "" {
			return value
		}
		if strings.Contains(match, ":-") {
			return fallback
		}
		note(name)
		return ""
	})
	for i, arg := range entry.Args {
		entry.Args[i] = openCodeEnvPattern.ReplaceAllStringFunc(arg, func(match string) string {
			groups := openCodeEnvPattern.FindStringSubmatch(match)
			name, fallback := groups[1], groups[2]
			if value := env.getenv(name); value != "" {
				return value
			}
			if strings.Contains(match, ":-") {
				return fallback
			}
			note(name)
			return ""
		})
	}
	return entry
}

func decodeOpenCodeCommand(raw json.RawMessage) (string, []string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	// Only an array is a valid local command. A bare string is not a shape
	// OpenCode runs, so it must not become a command identity.
	var command []string
	if json.Unmarshal(raw, &command) == nil && len(command) > 0 {
		return command[0], command[1:]
	}
	return "", nil
}

func loadOpenCodeUserConfig(ctx context.Context, loader *configLoader, env Env) (string, openCodeConfig, loadResult) {
	dir := openCodeConfigDir(env)
	paths := []string{
		filepath.Join(dir, "opencode.jsonc"),
		filepath.Join(dir, "opencode.json"),
	}
	var doc openCodeConfig
	for _, path := range paths {
		res := loader.loadJSON(ctx, path, &doc)
		if res != loadAbsent {
			return path, doc, res
		}
	}
	return paths[0], doc, loadAbsent
}

func openCodeConfigDir(env Env) string {
	if value := strings.TrimSpace(env.getenv("OPENCODE_CONFIG_DIR")); filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	if value := strings.TrimSpace(env.getenv("XDG_CONFIG_HOME")); filepath.IsAbs(value) {
		return filepath.Join(value, "opencode")
	}
	return env.homePath(".config", "opencode")
}

func nearestOpenCodeConfig(dirs []string) (string, bool) {
	for _, dir := range dirs {
		for _, name := range []string{"opencode.jsonc", "opencode.json"} {
			candidate := filepath.Join(dir, name)
			if _, err := os.Lstat(candidate); err == nil {
				return candidate, true
			}
		}
	}
	return "", false
}
