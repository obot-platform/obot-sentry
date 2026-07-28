package enforce

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

// resolveCursor resolves a Cursor beforeMCPExecution display name.
func resolveCursor(env Env, req ResolveRequest, displayName string, tr *tracer) Resolution {
	// No form: Cursor's beforeMCPExecution reports the configuration key verbatim,
	// so only an exact match means anything. Probed on Cursor 3.13.25 against nine
	// deliberately awkward keys — mixed case, dots, an at-sign, a space, leading and
	// trailing hyphens — and every one came back byte-identical. This event names the
	// server; it does not build a tool namespace.
	names := lookup{names: cursorLookupNames(displayName)}
	m, out := resolveScopes(cursorScopes(env, req), names, tr)
	switch out {
	case outcomeFound:
		// The matched key, not the display name: Cursor's display name can carry a
		// user- prefix, and reporting it would name a server that appears nowhere in
		// the user's configuration.
		return resolved(env, m.key, m.entry)
	case outcomeAmbiguous:
		return ambiguous(localagent.Cursor, displayName)
	default:
		// The display name is reported even on a miss: it is the only thing naming the
		// server in a decision-log row, and it is what the "allow all tools in this MCP
		// server" button binds to.
		return notFound(localagent.Cursor, displayName, fmt.Sprintf(
			"MCP server %q was not found in any Cursor MCP configuration", displayName))
	}
}

// cursorScopes returns every Cursor mcp.json as a peer of the others, in order:
// each open workspace root, then the user-level file.
//
// Nothing in the payload says which scope ran, so no scope may outrank another.
// Two servers that share a name across scopes send byte-identical payloads, and
// ~/.cursor/mcp.json is user-writable, so settling such a name by search order
// would be a self-service bypass: shadow an allowlisted project server's name in
// user scope, call the user-scope one, and the allowlisted identity is what gets
// reported and permitted.
func cursorScopes(env Env, req ResolveRequest) []scope {
	paths := make([]string, 0, len(req.WorkspaceRoots)+1)
	for _, root := range req.WorkspaceRoots {
		if root = strings.TrimSpace(root); root != "" {
			paths = append(paths, filepath.Join(filepath.Clean(root), ".cursor", "mcp.json"))
		}
	}
	paths = append(paths, env.homePath(".cursor", "mcp.json"))

	scopes := make([]scope, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		scopes = append(scopes, scope{path: path, key: mcpServersKey, load: jsonServers(path)})
	}
	return scopes
}

func cursorLookupNames(displayName string) []string {
	if trimmed, ok := strings.CutPrefix(displayName, "user-"); ok && trimmed != "" {
		return []string{displayName, trimmed}
	}
	return []string{displayName}
}
