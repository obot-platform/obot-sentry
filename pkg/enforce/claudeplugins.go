package enforce

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
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
	InstallPath string
}

// claudePluginScopes returns one scope per plugin installation, all at rank:
// peers, because which installation a session loads depends on settings the
// resolver does not reproduce. Installations that define a server alike identify
// it; ones that disagree leave it ambiguous.
//
// Every installation is a candidate, whatever its scope. A project- or
// local-scoped one records the directory it was installed from, but Claude Code
// loads it in any session whose settings enable the plugin, so a project's
// committed settings can switch on a plugin installed for another project.
//
// Only installations of a plugin that can define server, whose name begins
// "plugin:<plugin>:", are considered: no other plugin can define or override it.
// incomplete is set when one of them, or one of its server files, went unread
// for a bound (see maxClaudePluginInstalls), either here or when a scope loads;
// the scopes then do not settle which definition the session runs.
func claudePluginScopes(ctx context.Context, loader *configLoader, configDir, server string, rank int, incomplete *bool) []scope {
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
		if !strings.HasPrefix(server, "plugin:"+name+":") {
			continue
		}
		for _, install := range decodePluginInstalls(plugins[id]) {
			if len(scopes) == maxClaudePluginInstalls {
				*incomplete = true
				return scopes
			}
			if !filepath.IsAbs(install.InstallPath) {
				continue
			}
			s := scope{key: "plugin " + id, rank: rank}
			catalog := claudeCatalogManifest(loader, markets, market, name)
			if dir, res, local := claudeLocalPluginDir(ctx, loader, markets, market, name); !local {
				s.path = filepath.Clean(install.InstallPath)
				s.load = pluginServers(loader, name, s.path, catalog, incomplete)
			} else if res == loadOK {
				s.path = dir
				s.load = pluginServers(loader, name, dir, catalog, incomplete)
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
// marketplaces Claude Code knows.
type claudeMarketplaceSet struct {
	// res is how the registry loaded.
	res  loadResult
	path string
	// dirs holds the folder of each local marketplace, one Claude Code reads from
	// a local folder or file. catalogs holds every marketplace's catalog file.
	dirs, catalogs map[string]string
}

// claudeMarketplaces reads known_marketplaces.json for each marketplace's
// catalog, and for the local marketplaces: those added from a folder
// ("directory") or a catalog file ("file"). Claude Code records a local
// marketplace's folder as its installLocation, and for a "file" source the
// catalog is the file it was added from. Any other marketplace is fetched, and
// its catalog is the copy Claude Code keeps (see claudeFetchedCatalog).
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
			set.catalogs[market] = ""
			if filepath.IsAbs(dir) {
				set.catalogs[market] = claudeFetchedCatalog(dir)
			}
			continue
		}
		set.dirs[market] = dir
	}
	return set
}

// claudeFetchedCatalog returns the catalog Claude Code keeps for a marketplace it
// fetched, whose installLocation is location: the catalog inside the clone for
// one added from a repository, and location itself for one added from a catalog
// URL, which Claude Code saves as that one file.
func claudeFetchedCatalog(location string) string {
	if info, err := os.Stat(location); err == nil && !info.IsDir() {
		return location
	}
	return filepath.Join(location, filepath.FromSlash(claudeMarketplaceCatalog))
}

// claudeCatalogEntry reads a marketplace catalog for its first entry naming the
// plugin, which is the one Claude Code takes, and the catalog's
// metadata.pluginRoot. loadAbsent means the catalog is missing or does not list
// the plugin, and loadUnusable that Claude Code rejects the catalog; either way
// Claude Code cannot load the plugin.
func claudeCatalogEntry(ctx context.Context, loader *configLoader, catalog, name string) (entry map[string]json.RawMessage, root string, res loadResult) {
	var raw json.RawMessage
	if res := loader.loadStrictJSON(ctx, catalog, &raw, maxConfigBytes); res != loadOK {
		return nil, "", res
	}
	doc, ok := exactObject(raw)
	var entries []json.RawMessage
	if !ok || json.Unmarshal(doc["plugins"], &entries) != nil {
		return nil, "", loadUnusable
	}
	root, ok = catalogPluginRoot(doc)
	if !ok {
		return nil, "", loadUnusable
	}
	for _, raw := range entries {
		entry, ok := exactObject(raw)
		if !ok {
			continue
		}
		if entryName, _, _ := exactString(entry, "name"); entryName == name {
			return entry, root, loadOK
		}
	}
	// The catalog no longer lists the plugin, so Claude Code cannot load it.
	return nil, root, loadAbsent
}

// claudeCatalogManifest returns how to read the plugin's marketplace entry as
// its manifest, which is what Claude Code does for a plugin that ships no
// plugin.json: the entry's mcpServers are then the plugin's, merged over its
// .mcp.json. Claude Code reads the catalog afresh for each session, from the
// local marketplace's own file or from the copy it keeps of a fetched one, so an
// edit to either changes what the plugin runs without a reinstall.
//
// A nil entry with loadOK means there is no catalog to consult: the registry is
// absent or does not know the marketplace, and the plugin's own files are all
// that can define its servers. loadAbsent or loadUnusable means Claude Code
// cannot load the plugin, and loadUndecodable that the hook cannot tell what the
// entry holds.
func claudeCatalogManifest(loader *configLoader, markets claudeMarketplaceSet, market, name string) func(context.Context) (map[string]json.RawMessage, loadResult) {
	return func(ctx context.Context) (map[string]json.RawMessage, loadResult) {
		switch markets.res {
		case loadOK:
		case loadAbsent:
			return nil, loadOK
		default:
			return nil, loadUndecodable
		}
		catalog, known := markets.catalogs[market]
		if !known {
			return nil, loadOK
		}
		if !filepath.IsAbs(catalog) {
			// Claude Code resolves a relative path against a directory the hook cannot
			// determine.
			return nil, loadUndecodable
		}
		entry, _, res := claudeCatalogEntry(ctx, loader, catalog, name)
		if res != loadOK {
			return nil, res
		}
		return entry, loadOK
	}
}

// claudeLocalPluginDir returns the folder Claude Code loads a plugin from when
// its marketplace is local. Claude Code then reads the marketplace's catalog
// afresh and takes the first entry for the plugin. An entry whose source is a
// "./" path (or "."), or a bare name under the catalog's metadata.pluginRoot,
// loads in place from the marketplace folder, so an edit there takes effect
// without a reinstall. Any other source, including a string in another form, is
// loaded from the installed copy.
//
// local is false when the installed copy is what loads. Otherwise loadAbsent or
// loadUnusable means Claude Code cannot locate the plugin and loads none of its
// servers, and loadUndecodable that the resolver cannot tell where it loads from.
// Only cases verified to load nothing may be skipped: a skipped installation
// leaves a same-named plugin's installation to answer alone.
func claudeLocalPluginDir(ctx context.Context, loader *configLoader, markets claudeMarketplaceSet, market, name string) (dir string, res loadResult, local bool) {
	switch markets.res {
	case loadOK:
	case loadAbsent:
		return "", loadOK, false
	default:
		return "", loadUndecodable, true
	}
	marketDir, ok := markets.dirs[market]
	if !ok {
		return "", loadOK, false
	}
	catalog := markets.catalogs[market]
	if !filepath.IsAbs(marketDir) || !filepath.IsAbs(catalog) {
		// Claude Code resolves a relative path against a directory the hook cannot
		// determine.
		return "", loadUndecodable, true
	}
	entry, root, res := claudeCatalogEntry(ctx, loader, catalog, name)
	if res != loadOK {
		return "", res, true
	}
	rel, ok := jsonString(entry["source"])
	if !ok {
		return "", loadOK, false
	}
	if root != "" && bareSourceName(rel) {
		rel = "./" + path.Join(root, rel)
	}
	if strings.HasPrefix(rel, `.\`) {
		// Not a form Claude Code was observed to take either way, and on Windows
		// a relative path it may load in place.
		return "", loadUndecodable, true
	}
	if rel != "." && !strings.HasPrefix(rel, "./") {
		return "", loadOK, false
	}
	if dir, ok := marketplaceRelPath(marketDir, rel); ok {
		return dir, loadOK, true
	}
	return "", loadUnusable, true
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
		install.InstallPath, _, _ = exactString(members, "installPath")
		installs = append(installs, install)
	}
	return installs
}

// pluginServers loads one installation's servers the way Claude Code merges
// them: the plugin's .mcp.json first, then whatever its manifest's mcpServers
// names — a file, an inline table, or a list of either — each overriding what
// came before. Keys come back in Claude Code's "plugin:<plugin>:<server>" form.
//
// The manifest is the plugin's plugin.json. A plugin that ships none takes its
// marketplace entry as its manifest (see claudeCatalogManifest), whatever the
// entry's "strict" says; one that ships a plugin.json never does.
//
// The installation is usable if any of its files is: a plugin that declares
// servers in only one of the places is the common case.
//
// Each source is validated before it is merged, as Claude Code validates each
// file, so an entry a later source declares in a form Claude Code skips does not
// displace an earlier source's valid one.
//
// A manifest can also name what the hook does not read: an .mcpb bundle, whose
// server overrides a same-named one in .mcp.json, or a bundle's URL. Any string
// that is not a server file inside the plugin leaves the installation
// undecodable rather than letting the files the hook does read answer for it.
func pluginServers(loader *configLoader, name, dir string, catalog func(context.Context) (map[string]json.RawMessage, loadResult), incomplete *bool) func(context.Context) (serverSet, loadResult) {
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
		var (
			declared    json.RawMessage
			fromCatalog bool
		)
		switch manifestRes {
		case loadOK:
			if manifest, ok := exactObject(rawManifest); ok {
				declared = manifest[mcpServersKey]
			}
		case loadAbsent:
			entry, catalogRes := catalog(ctx)
			switch catalogRes {
			case loadOK:
			case loadUndecodable:
				return nil, loadUndecodable
			default:
				// Claude Code cannot find the plugin's manifest, and loads none of
				// its servers.
				return nil, loadAbsent
			}
			if entry != nil {
				res = max(res, loadOK)
				declared = entry[mcpServersKey]
				fromCatalog = true
			}
		}
		if len(declared) > 0 {
			var specs []json.RawMessage
			if json.Unmarshal(declared, &specs) != nil {
				specs = []json.RawMessage{declared}
			}
			files := 0
			for _, spec := range specs {
				if rel, ok := jsonString(spec); ok {
					path, ref := pluginServerRef(dir, rel)
					switch {
					case ref == pluginRefFile:
						if files == maxClaudePluginServerFiles {
							*incomplete = true
							return nil, loadUnusable
						}
						files++
						merge(path)
					case ref == pluginRefSkipped && !fromCatalog:
					default:
						return nil, loadUndecodable
					}
					continue
				}
				inline, ok := exactObject(spec)
				if !ok {
					if fromCatalog {
						return nil, loadUndecodable
					}
					continue
				}
				if fromCatalog && !claudeServerTableValid(inline) {
					// Claude Code takes a marketplace entry's mcpServers whole or not
					// at all: one entry it rejects drops every file and table the
					// entry names, leaving .mcp.json to answer. Which side of that
					// line an entry falls on decides which definition runs, so a
					// table the hook would only partly accept is not guessed at.
					return nil, loadUndecodable
				}
				maps.Copy(merged, claudeServers(inline))
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

// claudeServerTableValid reports whether Claude Code accepts every entry of an
// inline server table.
func claudeServerTableValid(table map[string]json.RawMessage) bool {
	for _, raw := range table {
		if _, ok := claudeServerEntry(raw); !ok {
			return false
		}
	}
	return true
}

// pluginServerFile reads a plugin server file, which may hold its table under
// mcpServers or be the table itself, and keeps the entries Claude Code loads.
// Claude Code reads "doc.mcpServers || doc", so the file is a flat table only
// when mcpServers is absent or falsy. Any other mcpServers that is not an object,
// such as an array, is read by Claude Code in ways the hook does not reproduce,
// so the file is undecodable rather than read as a flat table it is not.
func pluginServerFile(ctx context.Context, loader *configLoader, path string) (serverSet, loadResult) {
	var raw json.RawMessage
	if res := loader.loadStrictJSON(ctx, path, &raw, maxConfigBytes); res != loadOK {
		return nil, res
	}
	doc, ok := exactObject(raw)
	if !ok {
		return nil, loadUndecodable
	}
	member, present := doc[mcpServersKey]
	if !present || jsonFalsy(member) {
		return claudeServers(doc), loadOK
	}
	wrapped, ok := exactObject(member)
	if !ok {
		return nil, loadUndecodable
	}
	return claudeServers(wrapped), loadOK
}

// jsonFalsy reports whether a JSON value is falsy to JavaScript: null, false,
// "", or a number that is zero, including one too small to represent.
func jsonFalsy(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0:
		return false
	case string(raw) == "null", string(raw) == "false":
		return true
	case raw[0] == '"':
		value, ok := jsonString(raw)
		return ok && value == ""
	case raw[0] == '-' || raw[0] >= '0' && raw[0] <= '9':
		// ParseFloat returns zero with a range error for an underflow, which
		// JavaScript reads as zero too.
		f, _ := strconv.ParseFloat(string(raw), 64)
		return f == 0
	}
	return false
}

// pluginRef is what a string in a manifest's mcpServers refers to.
type pluginRef int

const (
	// pluginRefOpaque is anything the hook does not read: an .mcpb bundle, a URL,
	// or a path in a form Claude Code does not take as a server file.
	pluginRefOpaque pluginRef = iota
	// pluginRefFile is a "./" path to a .json server file inside the plugin.
	pluginRefFile
	// pluginRefSkipped is a "./" path to a .json file that climbs with "..".
	// Claude Code ignores it in a plugin.json and loads the rest of the plugin.
	pluginRefSkipped
)

// pluginServerRef classifies a manifest's server reference, and resolves one
// that names a server file.
func pluginServerRef(dir, rel string) (string, pluginRef) {
	if !strings.HasPrefix(rel, "./") || !strings.HasSuffix(rel, ".json") {
		return "", pluginRefOpaque
	}
	if strings.Contains(rel, "..") {
		return "", pluginRefSkipped
	}
	return filepath.Join(dir, filepath.FromSlash(rel)), pluginRefFile
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
