package enforce

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
	workbuddy "github.com/obot-platform/obot-sentry/pkg/workbuddy"
)

// workBuddyProject is the project-local MCP table WorkBuddy stores in the
// user-level .mcp.json file.
type workBuddyProject struct {
	MCPServers         map[string]json.RawMessage `json:"mcpServers"`
	DisabledMcpServers []string                   `json:"disabledMcpServers"`
}

// workBuddyMCPConfig is the part of the WorkBuddy user MCP document used
// during enforcement. WorkBuddy keeps this file separate from CodeBuddy CLI's
// ~/.codebuddy configuration.
type workBuddyMCPConfig struct {
	MCPServers         map[string]json.RawMessage  `json:"mcpServers"`
	DisabledMcpServers []string                    `json:"disabledMcpServers"`
	Projects           map[string]workBuddyProject `json:"projects"`
}

// resolveWorkBuddy resolves an MCP server against the same user, project, and
// project-local precedence WorkBuddy documents: local > project > user.
func resolveWorkBuddy(ctx context.Context, loader *configLoader, env Env, req ResolveRequest, serverName string, tr *tracer) Resolution {
	userPath, user, userRes := loadWorkBuddyUserConfig(ctx, loader, env)

	// WorkBuddy documents mcp__<server>__<tool> and preserves server-name
	// characters such as hyphens in the exposed namespace. Match exactly rather
	// than guessing a broader transform: a false match could resolve a call to a
	// server the agent never ran, while a miss safely remains unresolved.
	names := lookup{names: []string{serverName}}
	match, outcome := resolveScopes(ctx, workBuddyScopes(loader, env, req.CWD, userPath, user, userRes), names, tr)
	switch outcome {
	case outcomeFound:
		return resolved(env, match.key, match.entry)
	case outcomeAmbiguous:
		return ambiguous(localagent.WorkBuddy, serverName)
	default:
		return notFound(localagent.WorkBuddy, serverName, fmt.Sprintf(
			"MCP server %q was not found in any WorkBuddy MCP configuration", serverName))
	}
}

func workBuddyScopes(loader *configLoader, env Env, cwd, userPath string, user workBuddyMCPConfig, userRes loadResult) []scope {
	dirs := ancestors(cwd)
	projectFileDir, projectFilePath := nearestWorkBuddyProjectFile(dirs)
	keysByDir := make(map[string][]string, len(user.Projects))
	for key := range user.Projects {
		dir := env.comparableDir(key)
		if dir != "" {
			keysByDir[dir] = append(keysByDir[dir], key)
		}
	}
	for _, keys := range keysByDir {
		slices.Sort(keys)
	}

	scopes := make([]scope, 0, 2*len(dirs)+1)
	rank := 0
	for _, dir := range dirs {
		// Project-local configuration has higher precedence than the shared
		// project .mcp.json at the same directory. Multiple spellings of one
		// directory remain peers so conflicting definitions are rejected rather
		// than resolved by map iteration order.
		if keys := keysByDir[env.comparableDir(dir)]; len(keys) > 0 {
			for _, key := range keys {
				scopes = append(scopes, scope{
					path: userPath,
					key:  fmt.Sprintf("projects[%q].%s", key, mcpServersKey),
					rank: rank,
					load: fixedServers(workBuddyServerSet(
						user.Projects[key].MCPServers,
						user.Projects[key].DisabledMcpServers,
						env,
					), userRes),
				})
			}
			rank++
		}
		if dir == projectFileDir {
			scopes = append(scopes, scope{
				path: projectFilePath,
				key:  mcpServersKey,
				rank: rank,
				load: workBuddyJSONServers(loader, projectFilePath, env),
			})
			rank++
		}
	}

	return append(scopes, scope{
		path: userPath,
		key:  mcpServersKey,
		rank: rank,
		load: fixedServers(workBuddyServerSet(
			user.MCPServers,
			user.DisabledMcpServers,
			env,
		), userRes),
	})
}

var workBuddyEnvPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:-|-)([^}]*))?\}`)

// workBuddyServerSet decodes a WorkBuddy server table, preserves disabled
// names as zero-valued entries so they mask lower-priority scopes, and expands
// the environment-variable forms supported by the desktop runtime.
func workBuddyServerSet(raw map[string]json.RawMessage, disabled []string, env Env) serverSet {
	set := decodeServers(raw)
	for _, name := range disabled {
		set[name] = mcpEntry{Disabled: true}
	}
	for name, entry := range set {
		if entry.Disabled {
			continue
		}
		set[name] = expandWorkBuddyEntry(entry, env)
	}
	return set
}

func expandWorkBuddyEntry(entry mcpEntry, env Env) mcpEntry {
	missing := false
	expand := func(value string) string {
		return workBuddyEnvPattern.ReplaceAllStringFunc(value, func(token string) string {
			parts := workBuddyEnvPattern.FindStringSubmatch(token)
			if len(parts) != 4 {
				missing = true
				return token
			}
			if value := env.getenv(parts[1]); value != "" {
				return value
			}
			if parts[2] != "" {
				return parts[3]
			}
			missing = true
			return token
		})
	}

	entry.URL = expand(entry.URL)
	entry.Command = expand(entry.Command)
	for i := range entry.Args {
		entry.Args[i] = expand(entry.Args[i])
	}
	if len(entry.Environment) > 0 {
		expandedEnvironment := make(map[string]string, len(entry.Environment))
		for key, value := range entry.Environment {
			expandedEnvironment[key] = expand(value)
		}
		entry.Environment = expandedEnvironment
	}
	if missing {
		entry.ConfigError = "MCP server configuration contains an undefined environment variable"
	}
	return entry
}

func workBuddyJSONServers(loader *configLoader, path string, env Env) func(context.Context) (serverSet, loadResult) {
	return func(ctx context.Context) (serverSet, loadResult) {
		if ctx.Err() != nil {
			return nil, loadUnusable
		}
		var doc struct {
			MCPServers         map[string]json.RawMessage `json:"mcpServers"`
			DisabledMcpServers []string                   `json:"disabledMcpServers"`
		}
		res := loader.loadJSON(ctx, path, &doc)
		if res != loadOK {
			return nil, res
		}
		return workBuddyServerSet(doc.MCPServers, doc.DisabledMcpServers, env), loadOK
	}
}

// loadWorkBuddyUserConfig follows WorkBuddy's first-existing-file rule. It
// deliberately does not merge the preferred, deprecated, and legacy files: if
// the preferred file exists but omits a server, falling through to an older
// file could resolve a server WorkBuddy itself never loads. The shared path
// contract also honors the desktop sidecar's configuration-root variables.
func loadWorkBuddyUserConfig(ctx context.Context, loader *configLoader, env Env) (string, workBuddyMCPConfig, loadResult) {
	paths := workbuddy.NewConfig(env.Home, env.getenv).UserMCPPaths()
	path := paths[0]
	var doc workBuddyMCPConfig
	for _, candidate := range paths {
		path = candidate
		doc = workBuddyMCPConfig{}
		res := loader.loadJSON(ctx, path, &doc)
		if res != loadAbsent {
			return path, doc, res
		}
		if ctx.Err() != nil {
			return path, doc, loadUnusable
		}
	}
	return paths[0], doc, loadAbsent
}

// nearestWorkBuddyProjectFile returns the first project MCP file in the
// ancestor walk, honoring both the current .mcp.json name and deprecated
// mcp.json. With neither present it returns the expected .mcp.json path as an
// absent scope so traces still name the source the operator expects.
func nearestWorkBuddyProjectFile(dirs []string) (dir, path string) {
	for _, dir := range dirs {
		for _, name := range []string{".mcp.json", "mcp.json"} {
			candidate := filepath.Join(dir, name)
			if _, err := os.Lstat(candidate); err == nil {
				return dir, candidate
			}
		}
	}
	if len(dirs) == 0 {
		return "", ""
	}
	return dirs[0], filepath.Join(dirs[0], ".mcp.json")
}
