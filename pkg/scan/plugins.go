package scan

import (
	"io/fs"
	"path"

	"github.com/obot-platform/obot/apiclient/types"
)

// pluginExtensions is the file-collection extension allowlist used when
// ingesting plugin install directories. Wider than skillExtensions
// because plugins commonly include manifest, schema, and config files
// alongside scripts.
var pluginExtensions = map[string]bool{
	".md":    true,
	".mdc":   true,
	".txt":   true,
	".sh":    true,
	".py":    true,
	".js":    true,
	".ts":    true,
	".json":  true,
	".jsonc": true,
	".yaml":  true,
	".yml":   true,
	".toml":  true,
}

// emitPluginOpts is the per-client input for emitPlugin. Per-client
// scanners populate this struct from their cache layouts and call
// emitPlugin to do the shared work of file ingestion, component
// detection, and nested-observation emission.
type emitPluginOpts struct {
	installRel  string // plugin directory, relative to the root
	manifestRel string // manifest path within installRel
	pluginType  string // wire plugin_type
	client      string // wire client tag
	marketplace string
	enabled     bool

	// nameFallback / versionFallback are used when the manifest is
	// missing or omits the corresponding field.
	nameFallback    string
	versionFallback string

	// nestedMCPRel optionally lists files (e.g. mcp.json, .mcp.json)
	// checked first for nested MCP server definitions. If empty or
	// missing, the manifest's top-level mcpServers is used.
	nestedMCPRel []string

	// mcpServerXform, when non-nil, is invoked on each nested MCP
	// server's parsed entry before observation conversion (used by
	// Claude Code for ${CLAUDE_PLUGIN_ROOT} substitution).
	mcpServerXform func(*mcpServerSpec)
}

// emitPlugin performs the shared plugin-emit work: claims the install
// subtree, parses the manifest, runs file collection, detects
// components, emits nested MCP and skill observations, and finally
// builds the DeviceScanPlugin envelope.
func emitPlugin(s *state, o emitPluginOpts) observations {
	s.claim(o.installRel)
	manifestPath := s.addFileOrAbs(o.manifestRel)
	manifest, _ := readJSON[map[string]any](s.fsys, o.manifestRel)

	name, version, description, author := manifestMetadata(manifest)
	if name == "" {
		name = o.nameFallback
	}
	if version == "" {
		version = o.versionFallback
	}

	supporting := s.listArtifactPaths(o.installRel, pluginExtensions)
	hasMCP, hasSkills, hasRules, hasCommands, hasHooks := detectComponents(s.fsys, o.installRel, manifest)

	servers, foundMCP := emitNestedMCPServers(s, o.installRel, o.nestedMCPRel, manifest, o.manifestRel, o.client, o.mcpServerXform)
	if foundMCP {
		hasMCP = true
	}

	var skills []skill
	if hasSkills {
		skills = nestedSkills(s, o.installRel, o.client)
	}

	return observations{
		plugins: []types.DeviceScanPlugin{{
			Client:        o.client,
			ConfigPath:    manifestPath,
			Name:          name,
			PluginType:    o.pluginType,
			Version:       version,
			Description:   description,
			Author:        author,
			Enabled:       o.enabled,
			Marketplace:   o.marketplace,
			Files:         supporting,
			HasMCPServers: hasMCP,
			HasSkills:     hasSkills,
			HasRules:      hasRules,
			HasCommands:   hasCommands,
			HasHooks:      hasHooks,
		}},
		servers: servers,
		skills:  skills,
	}
}

// emitNestedMCPServers resolves a plugin's nested MCP server block (see
// pluginMCPServersBlock) and emits one observation per entry, in name
// order. found reports whether any block was located.
func emitNestedMCPServers(s *state, installRel string, nestedMCPRel []string, manifest map[string]any, manifestRel, client string, xform func(*mcpServerSpec)) (servers []types.DeviceScanMCPServer, found bool) {
	mcpRaw, sourceRel := pluginMCPServersBlock(s.fsys, installRel, nestedMCPRel, manifest, manifestRel)
	if len(mcpRaw) == 0 {
		return nil, false
	}
	sourcePath := s.addFileOrAbs(sourceRel)
	for _, name := range sortedKeys(mcpRaw) {
		entry, ok := decodeMCPServerSpec(mcpRaw[name])
		if !ok {
			continue
		}
		if xform != nil {
			xform(&entry)
		}
		servers = append(servers, entry.toServer(name, client, sourcePath, ""))
	}
	return servers, true
}

// decodeMCPServerSpec re-marshals a parsed map[string]any into a typed
// mcpServerSpec. Used in plugin paths where the manifest is already
// parsed as a generic map (so we can iterate plugin metadata fields)
// but the nested mcpServers entries still need to land in typed form.
func decodeMCPServerSpec(raw any) (mcpServerSpec, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return mcpServerSpec{}, false
	}
	out := mcpServerSpec{}
	if t, ok := m["type"].(string); ok {
		out.Type = t
	}
	if t, ok := m["transport"].(string); ok {
		out.Transport = t
	}
	if c, ok := m["command"].(string); ok {
		out.Command = c
	}
	if u, ok := m["url"].(string); ok {
		out.URL = u
	}
	if u, ok := m["serverUrl"].(string); ok {
		out.ServerURL = u
	}
	if a, ok := m["args"].([]any); ok {
		args := make([]string, 0, len(a))
		for _, x := range a {
			if s, ok := x.(string); ok {
				args = append(args, s)
			}
		}
		out.Args = args
	}
	if env, ok := m["env"].(map[string]any); ok {
		out.Env = env
	}
	if h, ok := m["headers"].(map[string]any); ok {
		out.Headers = h
	}
	if e, ok := m["enabled"].(bool); ok {
		out.Enabled = &e
	}
	return out, true
}

// manifestMetadata pulls (name, version, description, author) out of a
// parsed plugin manifest. author may be a string or {"name": "…"}
// object.
func manifestMetadata(m map[string]any) (name, version, description, author string) {
	if m == nil {
		return
	}
	if s, ok := m["name"].(string); ok {
		name = s
	}
	if s, ok := m["version"].(string); ok {
		version = s
	}
	if s, ok := m["description"].(string); ok {
		description = s
	}
	switch a := m["author"].(type) {
	case string:
		author = a
	case map[string]any:
		if s, ok := a["name"].(string); ok {
			author = s
		}
	}
	return
}

// detectComponents returns the has_* booleans for the wire plugin
// observation. mcp is true if mcp.json / .mcp.json exists, or the
// manifest has a non-empty mcpServers dict; the others key off
// subdirectory presence.
func detectComponents(fsys fs.FS, installRel string, manifest map[string]any) (mcp, skills, rules, commands, hooks bool) {
	if fileExists(fsys, path.Join(installRel, "mcp.json")) || fileExists(fsys, path.Join(installRel, ".mcp.json")) {
		mcp = true
	}
	if !mcp && manifest != nil {
		if m, ok := manifest["mcpServers"].(map[string]any); ok && len(m) > 0 {
			mcp = true
		}
	}
	skills = dirExists(fsys, path.Join(installRel, "skills"))
	rules = dirExists(fsys, path.Join(installRel, "rules"))
	commands = dirExists(fsys, path.Join(installRel, "commands"))
	hooks = dirExists(fsys, path.Join(installRel, "hooks"))
	return
}

// nestedSkills walks <installRel>/skills/<name>/SKILL.md and emits a
// skill for each, attributed to the single hosting client. Used for
// skills that ship inside plugins.
func nestedSkills(s *state, installRel, client string) []skill {
	skillsRoot := path.Join(installRel, "skills")
	entries, err := fs.ReadDir(s.fsys, skillsRoot)
	if err != nil {
		return nil
	}
	var out []skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if sk, ok := ingestSkill(s, path.Join(skillsRoot, e.Name()), []string{client}, ""); ok {
			out = append(out, sk)
		}
	}
	return out
}

// fileExists reports whether rel exists and is a regular file.
func fileExists(fsys fs.FS, rel string) bool {
	info, err := fs.Stat(fsys, rel)
	return err == nil && !info.IsDir()
}

// pluginMCPServersBlock locates the nested MCP server definitions for a
// plugin. It tries each candidate file in nestedMCPRel and falls back
// to the manifest's mcpServers dict. Returns the raw server-name → raw
// map and the root-relative path of the file the entries came from
// (manifestRel on fallback, "" when nothing was found).
func pluginMCPServersBlock(fsys fs.FS, installRel string, nestedMCPRel []string, manifest map[string]any, manifestRel string) (map[string]any, string) {
	for _, fname := range nestedMCPRel {
		fileRel := path.Join(installRel, fname)
		data, ok := readJSON[map[string]any](fsys, fileRel)
		if !ok {
			continue
		}
		if m, ok := data["mcpServers"].(map[string]any); ok && len(m) > 0 {
			return m, fileRel
		}
		// Some configs store servers at root level rather than under
		// mcpServers.
		root := map[string]any{}
		for k, v := range data {
			entry, ok := v.(map[string]any)
			if !ok {
				continue
			}
			for _, field := range []string{"command", "url", "serverUrl"} {
				if _, has := entry[field]; has {
					root[k] = v
					break
				}
			}
		}
		if len(root) > 0 {
			return root, fileRel
		}
	}
	if manifest != nil {
		if m, ok := manifest["mcpServers"].(map[string]any); ok && len(m) > 0 {
			return m, manifestRel
		}
	}
	return nil, ""
}
