package enforce

import (
	"context"
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// Claude Code names a plugin's MCP server "plugin:<plugin>:<server>", which
// namespaces as "plugin_<plugin>_<server>". The plugin and server halves cannot
// be told apart from that — either may contain an underscore — so matching runs
// forward like every other Claude Code lookup: build each installed plugin's
// keys and namespace them.
const (
	claudePluginNamespacePrefix = "plugin_"
	claudePluginRegistry        = "installed_plugins.json"
	claudePluginManifest        = ".claude-plugin/plugin.json"
	claudePluginRootVar         = "${CLAUDE_PLUGIN_ROOT}"
	// maxClaudePluginInstalls bounds the installations considered per call.
	maxClaudePluginInstalls = 256
	// maxClaudePluginServerFiles bounds the server files one manifest may name.
	maxClaudePluginServerFiles = 16
)

// claudePluginInstall is one installation recorded in installed_plugins.json.
type claudePluginInstall struct {
	Scope       string `json:"scope"`
	InstallPath string `json:"installPath"`
	ProjectPath string `json:"projectPath"`
}

// claudePluginScopes returns one scope per plugin installation that may serve
// this project, all at rank: peers, because which installation a session loads
// depends on settings the resolver does not reproduce. Installations that define
// a server alike identify it; ones that disagree leave it ambiguous.
//
// A user-scoped installation serves every project. A project- or local-scoped one
// records the directory it was installed from, and serves only a session
// launched there or keyed to that project root.
func claudePluginScopes(ctx context.Context, loader *configLoader, env Env, configDir string, project claudeProject, rank int) []scope {
	registryPath := filepath.Join(configDir, "plugins", claudePluginRegistry)
	var doc struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	res := loader.loadJSON(ctx, registryPath, &doc)
	if res != loadOK || len(doc.Plugins) == 0 {
		return []scope{{path: registryPath, key: "plugins", rank: rank, load: fixedServers(nil, res)}}
	}

	var scopes []scope
	for _, id := range slices.Sorted(maps.Keys(doc.Plugins)) {
		name := id
		if at := strings.LastIndex(id, "@"); at > 0 {
			name = id[:at]
		}
		for _, install := range decodePluginInstalls(doc.Plugins[id]) {
			if len(scopes) == maxClaudePluginInstalls {
				return scopes
			}
			if !filepath.IsAbs(install.InstallPath) || !pluginServesProject(env, install, project) {
				continue
			}
			dir := filepath.Clean(install.InstallPath)
			scopes = append(scopes, scope{
				path: dir,
				key:  "plugin " + id,
				rank: rank,
				load: pluginServers(loader, name, dir),
			})
		}
	}
	return scopes
}

// decodePluginInstalls reads a plugin's installations: a list in the current
// registry, a single object in the version-1 one.
func decodePluginInstalls(raw json.RawMessage) []claudePluginInstall {
	var list []claudePluginInstall
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var one claudePluginInstall
	if json.Unmarshal(raw, &one) == nil {
		return []claudePluginInstall{one}
	}
	return nil
}

func pluginServesProject(env Env, install claudePluginInstall, project claudeProject) bool {
	switch install.Scope {
	case "project", "local":
	default:
		return true
	}
	if install.ProjectPath == "" || project.launchDir == "" {
		return false
	}
	at := env.comparableDir(install.ProjectPath)
	return at == env.comparableDir(project.launchDir) || at == env.comparableDir(project.root)
}

// pluginServers loads one installation's servers the way Claude Code merges
// them: the plugin's .mcp.json first, then whatever its manifest's mcpServers
// names — a file, an inline table, or a list of either — each overriding what
// came before. Keys come back in Claude Code's "plugin:<plugin>:<server>" form.
//
// The installation is usable if any of its files is: a plugin that declares
// servers in only one of the places is the common case.
func pluginServers(loader *configLoader, name, dir string) func(context.Context) (serverSet, loadResult) {
	return func(ctx context.Context) (serverSet, loadResult) {
		raw := map[string]json.RawMessage{}
		res := loadAbsent
		merge := func(path string) {
			servers, r := pluginServerFile(ctx, loader, path)
			res = max(res, r)
			maps.Copy(raw, servers)
		}

		merge(filepath.Join(dir, ".mcp.json"))

		var manifest struct {
			MCPServers json.RawMessage `json:"mcpServers"`
		}
		manifestRes := loader.loadJSON(ctx, filepath.Join(dir, filepath.FromSlash(claudePluginManifest)), &manifest)
		res = max(res, manifestRes)
		if manifestRes == loadOK && len(manifest.MCPServers) > 0 {
			var specs []json.RawMessage
			if json.Unmarshal(manifest.MCPServers, &specs) != nil {
				specs = []json.RawMessage{manifest.MCPServers}
			}
			files := 0
			for _, spec := range specs {
				var rel string
				if json.Unmarshal(spec, &rel) == nil {
					if path, ok := pluginRelPath(dir, rel); ok && files < maxClaudePluginServerFiles {
						files++
						merge(path)
					}
					continue
				}
				var inline map[string]json.RawMessage
				if json.Unmarshal(spec, &inline) == nil {
					maps.Copy(raw, inline)
				}
			}
		}
		if res != loadOK {
			return nil, res
		}

		set := make(serverSet, len(raw))
		for server, entry := range decodeServers(raw) {
			set["plugin:"+name+":"+server] = expandPluginRoot(entry, dir)
		}
		return set, loadOK
	}
}

// pluginServerFile reads a plugin server file, which may hold its table under
// mcpServers or be the table itself.
func pluginServerFile(ctx context.Context, loader *configLoader, path string) (map[string]json.RawMessage, loadResult) {
	var doc map[string]json.RawMessage
	res := loader.loadJSON(ctx, path, &doc)
	if res != loadOK {
		return nil, res
	}
	if wrapped, ok := doc[mcpServersKey]; ok {
		var servers map[string]json.RawMessage
		if json.Unmarshal(wrapped, &servers) != nil {
			return nil, loadUnusable
		}
		return servers, loadOK
	}
	return doc, loadOK
}

// pluginRelPath resolves a manifest's server file reference. Claude Code takes
// only a "./" path to a .json file that does not climb out of the plugin.
func pluginRelPath(dir, rel string) (string, bool) {
	if !strings.HasPrefix(rel, "./") || !strings.HasSuffix(rel, ".json") || strings.Contains(rel, "..") {
		return "", false
	}
	return filepath.Join(dir, filepath.FromSlash(rel)), true
}

// expandPluginRoot substitutes ${CLAUDE_PLUGIN_ROOT}, which Claude Code expands to
// the installation directory, so a bundled server reports the path it runs from.
func expandPluginRoot(entry mcpEntry, dir string) mcpEntry {
	expand := func(s string) string { return strings.ReplaceAll(s, claudePluginRootVar, dir) }
	entry.URL = expand(entry.URL)
	entry.Command = expand(entry.Command)
	if entry.Args != nil {
		args := make([]string, len(entry.Args))
		for i, arg := range entry.Args {
			args[i] = expand(arg)
		}
		entry.Args = args
	}
	return entry
}
