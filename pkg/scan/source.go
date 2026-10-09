package scan

import (
	"path"
	"strings"
)

// Source is something the scan reads: a config file a client writes, or
// a plugin install tree. Path and Scope are data because the pipeline
// needs them for routing and dedupe; Read is the client's own decoder,
// since formats and entry shapes don't generalize (Goose's enabled flag,
// Zed's context_servers, Codex's TOML).
//
// Read is called once per matching path — for Home scope with Path
// itself, for Project scope once per walk hit — and returns whatever it
// found. It may emit servers, plugins and skills together: Claude
// Desktop's extension registry yields all three from one file.
//
// Sources have a single reader today, so there is no Readers field:
// every config file here is written by exactly one client. When a
// vendor-neutral path grows several (`.mcp.json` is already a generic
// name), this gains Readers and build's per-reader fan-out generalizes
// from skills to servers.
type Source struct {
	// Path is root-relative. At Project scope it is a suffix matched
	// anywhere in the walk: ".cursor/mcp.json" matches any
	// */.cursor/mcp.json.
	Path  string
	Scope Scope
	// Dir marks a Path that names a directory, not a file. At Project
	// scope the walk matches it against directories and hands the
	// directory to Read without descending into it, for config that is a
	// tree of arbitrarily named files (Kiro's .kiro/agents).
	Dir  bool
	Read func(s *state, rel, projectPath string) observations
}

// projectOf returns the absolute path of the project enclosing a
// project-scope hit. The config's own path tells us how far up to go —
// ".cursor/mcp.json" is two segments below its project, ".mcp.json" one
// — so clients don't each repeat the same path.Dir chain.
func (src Source) projectOf(s *state, rel string) string {
	up := strings.Count(src.Path, "/") + 1
	dir := rel
	for range up {
		dir = path.Dir(dir)
	}
	return s.abs(dir)
}

// sources is the pipeline's registry, resolved for one platform. Adding
// a client means adding rows here and a decoder in its own file. Order
// is by client name so emit order is deterministic.
func sources(platform string) []Source {
	return append(
		claudeDesktopSources(platform),
		Source{
			Path:  antigravityMCPConfigRel,
			Scope: Home,
			Read:  antigravityServers,
		},
		Source{
			Path:  antigravityPluginsRel,
			Scope: Home,
			Read:  antigravityPlugins,
		},

		// Claude Code's global config carries both its own servers and a
		// projects map; project-scope .mcp.json is the standard shape.
		Source{
			Path:  claudeGlobalConfigRel,
			Scope: Home,
			Read:  claudeCodeHomeServers,
		},
		Source{
			Path:  claudePluginsRel,
			Scope: Home,
			Read:  claudeCodePlugins,
		},
		Source{
			Path:  ".mcp.json",
			Scope: Project,
			Read:  claudeCodeProjectServers,
		},

		Source{
			Path:  codexGlobalConfigRel,
			Scope: Home | Project,
			Read:  codexServers,
		},
		Source{
			Path:  codexPluginCacheRel,
			Scope: Home,
			Read:  codexPlugins,
		},

		Source{
			Path:  cursorGlobalConfigRel,
			Scope: Home | Project,
			Read:  cursorServers,
		},
		Source{
			Path:  cursorPluginCacheRel,
			Scope: Home,
			Read:  cursorPlugins,
		},

		Source{
			Path:  gooseGlobalConfigRel(platform),
			Scope: Home,
			Read:  gooseServers,
		},
		Source{
			Path:  hermesGlobalConfigRel,
			Scope: Home,
			Read:  hermesServers,
		},

		Source{
			Path:  kiroMCPConfigRel,
			Scope: Home | Project,
			Read:  kiroServers,
		},
		Source{
			Path:  kiroPowersRel,
			Scope: Home,
			Read:  kiroPowers,
		},
		Source{
			Path:  kiroAgentsRel,
			Scope: Home | Project,
			Dir:   true,
			Read:  kiroAgents,
		},

		Source{
			Path:  opencodeGlobalConfigJSONRel,
			Scope: Home,
			Read:  opencodeServers,
		},
		Source{
			Path:  opencodeGlobalConfigJSONCRel,
			Scope: Home,
			Read:  opencodeServers,
		},
		Source{
			Path:  "opencode.json",
			Scope: Project,
			Read:  opencodeServers,
		},
		Source{
			Path:  opencodeLocalPluginsRel,
			Scope: Home,
			Read:  opencodeLocalPlugins,
		},
		Source{
			Path:  opencodeNPMCacheRel,
			Scope: Home,
			Read:  opencodeNPMPlugins,
		},

		Source{
			Path:  path.Join(vscodeUserDir(platform), "mcp.json"),
			Scope: Home,
			Read:  vscodeServers,
		},
		Source{
			Path:  ".vscode/mcp.json",
			Scope: Project,
			Read:  vscodeServers,
		},

		Source{
			Path:  zedSettingsRel(platform),
			Scope: Home,
			Read:  zedHomeServers,
		},
		Source{
			Path:  ".zed/settings.json",
			Scope: Project,
			Read:  zedProjectServers,
		},
	)
}

// claudeDesktopSources are separate because Claude Desktop's layout
// repeats per config directory (a legacy per-user install and an MSIX
// install virtualize the same tree), so its rows are generated rather
// than listed.
func claudeDesktopSources(platform string) []Source {
	var out []Source
	for _, dir := range claudeDesktopDirs(platform) {
		out = append(out,
			Source{
				Path:  path.Join(dir, "extensions-installations.json"),
				Scope: Home,
				Read:  claudeDesktopRegistry,
			},
			Source{
				Path:  path.Join(dir, "claude_desktop_config.json"),
				Scope: Home,
				Read:  claudeDesktopServers,
			},
			Source{
				Path:  dir,
				Scope: Home,
				Read:  claudeDesktopCowork,
			},
		)
	}
	return out
}
