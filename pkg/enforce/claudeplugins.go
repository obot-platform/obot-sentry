package enforce

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Claude Code names a plugin's MCP server "plugin:<plugin>:<server>" and reports
// that name in mcp_server, so each installed plugin's servers are keyed that way
// and matched exactly.
const (
	claudePluginRegistry      = "installed_plugins.json"
	claudeMarketplaceRegistry = "known_marketplaces.json"
	claudeMarketplaceCatalog  = ".claude-plugin/marketplace.json"
	claudePluginManifest      = ".claude-plugin/plugin.json"
	claudePluginRootVar       = "${CLAUDE_PLUGIN_ROOT}"
	// maxClaudePluginInstalls bounds the installations considered per call, and
	// maxClaudePluginServerFiles the server files one manifest may name. Claude
	// Code has neither bound, and a server a later file or installation defines
	// can override one an earlier one does, so past either the plugin servers are
	// reported incomplete rather than truncated.
	maxClaudePluginInstalls    = 256
	maxClaudePluginServerFiles = 16
)

// claudePluginInstall is one installation recorded in installed_plugins.json.
type claudePluginInstall struct {
	Scope       string
	InstallPath string
	ProjectPath string
}

// claudePluginScopes returns one scope per plugin installation that may serve
// this project, all at rank: peers, because which installation a session loads
// depends on settings the resolver does not reproduce. Installations that define
// a server alike identify it; ones that disagree leave it ambiguous.
//
// A user-scoped installation serves every project. A project- or local-scoped one
// records the directory it was installed from, and serves only a session
// launched there or keyed to that project root.
//
// incomplete is set when an installation or a server file went unread for a
// bound (see maxClaudePluginInstalls), either here or when a scope loads; the
// scopes then do not settle which definition the session runs.
func claudePluginScopes(ctx context.Context, loader *configLoader, env Env, configDir string, project claudeProject, rank int, incomplete *bool) []scope {
	registryPath := filepath.Join(configDir, "plugins", claudePluginRegistry)
	var raw json.RawMessage
	res := loader.loadStrictJSON(ctx, registryPath, &raw, maxConfigBytes)
	var plugins map[string]json.RawMessage
	if res == loadOK {
		members, ok := exactObject(raw)
		if ok {
			plugins, ok = objectMember(members, "plugins")
		}
		if !ok {
			res = loadUnusable
		}
	}
	if res != loadOK || len(plugins) == 0 {
		return []scope{{path: registryPath, key: "plugins", rank: rank, load: fixedServers(nil, res)}}
	}

	markets := claudeMarketplaces(ctx, loader, configDir)
	var scopes []scope
	for _, id := range slices.Sorted(maps.Keys(plugins)) {
		name, market := id, ""
		if at := strings.LastIndex(id, "@"); at > 0 {
			name, market = id[:at], id[at+1:]
		}
		for _, install := range decodePluginInstalls(plugins[id]) {
			if len(scopes) == maxClaudePluginInstalls {
				*incomplete = true
				return scopes
			}
			if !filepath.IsAbs(install.InstallPath) || !pluginServesProject(env, install, project) {
				continue
			}
			s := scope{key: "plugin " + id, rank: rank}
			if dir, res, local := claudeLocalPluginDir(ctx, loader, markets, market, name); !local {
				s.path = filepath.Clean(install.InstallPath)
				s.load = pluginServers(loader, name, s.path, incomplete)
			} else if res == loadOK {
				s.path = dir
				s.load = pluginServers(loader, name, dir, incomplete)
			} else {
				s.path = cmp.Or(markets.catalogs[market], markets.path)
				s.load = fixedServers(nil, res)
			}
			scopes = append(scopes, s)
		}
	}
	return scopes
}

// claudeMarketplaceSet is what known_marketplaces.json says about the
// marketplaces Claude Code reads from a local folder or file.
type claudeMarketplaceSet struct {
	// res is how the registry loaded.
	res  loadResult
	path string
	// dirs holds each local marketplace's folder, and catalogs its catalog file.
	dirs, catalogs map[string]string
}

// claudeMarketplaces reads known_marketplaces.json for the local marketplaces:
// those added from a folder ("directory") or a catalog file ("file"). Claude
// Code records a local marketplace's folder as its installLocation, and for a
// "file" source the catalog is the file it was added from.
func claudeMarketplaces(ctx context.Context, loader *configLoader, configDir string) claudeMarketplaceSet {
	set := claudeMarketplaceSet{
		path:     filepath.Join(configDir, "plugins", claudeMarketplaceRegistry),
		dirs:     map[string]string{},
		catalogs: map[string]string{},
	}
	var raw json.RawMessage
	if set.res = loader.loadStrictJSON(ctx, set.path, &raw, maxConfigBytes); set.res != loadOK {
		return set
	}
	markets, ok := exactObject(raw)
	if !ok {
		set.res = loadUnusable
		return set
	}
	for market, raw := range markets {
		entry, ok := exactObject(raw)
		if !ok {
			continue
		}
		source, _ := objectMember(entry, "source")
		kind, _, _ := exactString(source, "source")
		dir, _, _ := exactString(entry, "installLocation")
		switch kind {
		case "directory":
			set.catalogs[market] = filepath.Join(dir, filepath.FromSlash(claudeMarketplaceCatalog))
		case "file":
			set.catalogs[market], _, _ = exactString(source, "path")
		default:
			continue
		}
		set.dirs[market] = dir
	}
	return set
}

// claudeLocalPluginDir returns the folder Claude Code loads a plugin from when
// its marketplace is local. Claude Code then reads the marketplace's catalog
// afresh and takes the first entry for the plugin. An entry whose source is a
// "./" path (or "."), or a bare name under the catalog's metadata.pluginRoot,
// loads in place from the marketplace folder, so an edit there takes effect
// without a reinstall. Any other source, including a string in another form, is
// loaded from the installed copy.
//
// local is false when the installed copy is what loads. Otherwise a res other
// than loadOK means Claude Code cannot locate the plugin and loads none of its
// servers, or, for an unreadable registry, that the resolver cannot tell where it
// loads from.
func claudeLocalPluginDir(ctx context.Context, loader *configLoader, markets claudeMarketplaceSet, market, name string) (dir string, res loadResult, local bool) {
	switch markets.res {
	case loadOK:
	case loadAbsent:
		return "", loadOK, false
	default:
		return "", markets.res, true
	}
	marketDir, ok := markets.dirs[market]
	if !ok {
		return "", loadOK, false
	}
	catalog := markets.catalogs[market]
	if !filepath.IsAbs(marketDir) || !filepath.IsAbs(catalog) {
		return "", loadUnusable, true
	}
	var raw json.RawMessage
	if res := loader.loadStrictJSON(ctx, catalog, &raw, maxConfigBytes); res != loadOK {
		return "", res, true
	}
	doc, ok := exactObject(raw)
	var entries []json.RawMessage
	if !ok || json.Unmarshal(doc["plugins"], &entries) != nil {
		return "", loadUnusable, true
	}
	root, ok := catalogPluginRoot(doc)
	if !ok {
		return "", loadUnusable, true
	}
	for _, raw := range entries {
		entry, ok := exactObject(raw)
		if !ok {
			continue
		}
		if entryName, _, _ := exactString(entry, "name"); entryName != name {
			continue
		}
		rel, ok := jsonString(entry["source"])
		if !ok {
			return "", loadOK, false
		}
		if root != "" && bareSourceName(rel) {
			rel = "./" + path.Join(root, rel)
		}
		if strings.HasPrefix(rel, `.\`) {
			// Not a form Claude Code was observed to take either way.
			return "", loadUnusable, true
		}
		if rel != "." && !strings.HasPrefix(rel, "./") {
			return "", loadOK, false
		}
		if dir, ok := marketplaceRelPath(marketDir, rel); ok {
			return dir, loadOK, true
		}
		return "", loadUnusable, true
	}
	// The catalog no longer lists the plugin, so Claude Code cannot load it.
	return "", loadAbsent, true
}

// catalogPluginRoot returns the catalog's metadata.pluginRoot, normalized the way
// Claude Code normalizes it: "." for the marketplace root, "" when there is none
// or it is not a relative path inside the marketplace. ok is false when it is not
// a string, which makes Claude Code reject the whole catalog.
func catalogPluginRoot(doc map[string]json.RawMessage) (string, bool) {
	metadata, ok := exactObject(doc["metadata"])
	if !ok {
		return "", true
	}
	root, present, ok := exactString(metadata, "pluginRoot")
	if !present {
		return "", true
	}
	if !ok {
		return "", false
	}
	if root == "" || strings.HasPrefix(root, "/") || strings.ContainsAny(root, `\:`) {
		return "", true
	}
	root = strings.TrimRight(strings.TrimPrefix(root, "./"), "/")
	if root == "" || root == "." {
		return ".", true
	}
	for _, part := range strings.Split(root, "/") {
		if part == "" || part == "." || part == ".." {
			return "", true
		}
	}
	return root, true
}

// bareSourceName reports whether a catalog source is a bare plugin name, which
// Claude Code resolves under metadata.pluginRoot.
func bareSourceName(source string) bool {
	if source == "" || strings.Contains(source, "..") {
		return false
	}
	for i, r := range source {
		alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !alnum && (i == 0 || r != '-' && r != '.' && r != '_') {
			return false
		}
	}
	return true
}

// marketplaceRelPath resolves a "./" catalog source inside the marketplace
// folder. Claude Code strips the leading "." and its slashes, and refuses a path
// with a "." or ".." segment; "./" alone is the marketplace folder itself.
func marketplaceRelPath(dir, rel string) (string, bool) {
	rel = strings.TrimLeft(strings.TrimPrefix(rel, "."), "/")
	if strings.ContainsAny(rel, `\:`) {
		return "", false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "." || part == ".." {
			return "", false
		}
	}
	return filepath.Join(dir, filepath.FromSlash(rel)), true
}

// decodePluginInstalls reads a plugin's installations: a list in the current
// registry, a single object in the version-1 one.
func decodePluginInstalls(raw json.RawMessage) []claudePluginInstall {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		list = []json.RawMessage{raw}
	}
	installs := make([]claudePluginInstall, 0, len(list))
	for _, item := range list {
		members, ok := exactObject(item)
		if !ok {
			continue
		}
		var install claudePluginInstall
		install.Scope, _, _ = exactString(members, "scope")
		install.InstallPath, _, _ = exactString(members, "installPath")
		install.ProjectPath, _, _ = exactString(members, "projectPath")
		installs = append(installs, install)
	}
	return installs
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
//
// Each source is validated before it is merged, as Claude Code validates each
// file, so an entry a later source declares in a form Claude Code skips does not
// displace an earlier source's valid one.
func pluginServers(loader *configLoader, name, dir string, incomplete *bool) func(context.Context) (serverSet, loadResult) {
	return func(ctx context.Context) (serverSet, loadResult) {
		merged := serverSet{}
		res := loadAbsent
		merge := func(path string) {
			servers, r := pluginServerFile(ctx, loader, path)
			res = max(res, r)
			maps.Copy(merged, servers)
		}

		merge(filepath.Join(dir, ".mcp.json"))

		var rawManifest json.RawMessage
		manifestRes := loader.loadStrictJSON(ctx, filepath.Join(dir, filepath.FromSlash(claudePluginManifest)), &rawManifest, maxConfigBytes)
		res = max(res, manifestRes)
		var declared json.RawMessage
		if manifest, ok := exactObject(rawManifest); manifestRes == loadOK && ok {
			declared = manifest[mcpServersKey]
		}
		if len(declared) > 0 {
			var specs []json.RawMessage
			if json.Unmarshal(declared, &specs) != nil {
				specs = []json.RawMessage{declared}
			}
			files := 0
			for _, spec := range specs {
				var rel string
				if json.Unmarshal(spec, &rel) == nil {
					if path, ok := pluginRelPath(dir, rel); ok {
						if files == maxClaudePluginServerFiles {
							*incomplete = true
							return nil, loadUnusable
						}
						files++
						merge(path)
					}
					continue
				}
				if inline, ok := exactObject(spec); ok {
					maps.Copy(merged, claudeServers(inline))
				}
			}
		}
		if res != loadOK {
			return nil, res
		}

		set := make(serverSet, len(merged))
		for server, entry := range merged {
			set["plugin:"+name+":"+server] = expandPluginRoot(entry, dir)
		}
		return set, loadOK
	}
}

// pluginServerFile reads a plugin server file, which may hold its table under
// mcpServers or be the table itself, and keeps the entries Claude Code loads.
// Like Claude Code's "doc.mcpServers || doc", a mcpServers member that is not an
// object leaves the file read as a flat table.
func pluginServerFile(ctx context.Context, loader *configLoader, path string) (serverSet, loadResult) {
	var raw json.RawMessage
	if res := loader.loadStrictJSON(ctx, path, &raw, maxConfigBytes); res != loadOK {
		return nil, res
	}
	doc, ok := exactObject(raw)
	if !ok {
		return nil, loadUnusable
	}
	if wrapped, ok := objectMember(doc, mcpServersKey); ok && wrapped != nil {
		return claudeServers(wrapped), loadOK
	}
	return claudeServers(doc), loadOK
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
