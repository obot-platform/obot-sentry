package scan

import (
	"path"
	"strings"

	workbuddy "github.com/obot-platform/obot-sentry/pkg/workbuddy"
	zcode "github.com/obot-platform/obot-sentry/pkg/zcode"
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
// A Source normally has one reader, but Readers allows a vendor-neutral
// path (for example `.mcp.json`) to be decoded for more than one client.
// Readers are invoked in declaration order after Read, and every reader
// receives the same source path and project scope.
type Source struct {
	// Path is root-relative. At Project scope it is a suffix matched
	// anywhere in the walk: ".cursor/mcp.json" matches any
	// */.cursor/mcp.json.
	Path    string
	Scope   Scope
	Read    func(s *state, rel, projectPath string) observations
	Readers []func(s *state, rel, projectPath string) observations
}

func (src Source) read(s *state, rel, projectPath string) observations {
	var out observations
	if src.Read != nil {
		out.add(src.Read(s, rel, projectPath))
	}
	for _, reader := range src.Readers {
		if reader != nil {
			out.add(reader(s, rel, projectPath))
		}
	}
	return out
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
			Readers: []func(s *state, rel, projectPath string) observations{
				workBuddyProjectServers,
			},
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
			Path:  workbuddy.DefaultUserMCPPathRel,
			Scope: Home,
			Read:  workBuddyServers,
		},

		Source{
			Path:  zcode.UserConfigRel,
			Scope: Home,
			Read:  zcodeServers,
		},
		Source{
			Path:  zcode.UserCompatRel,
			Scope: Home,
			Read:  zcodeCompatServers,
		},
		Source{
			Path:  ".zcode",
			Scope: Home,
			Read:  zcodePlugins,
		},
		Source{
			Path:  zcode.ProjectConfigRel,
			Scope: Project,
			Read:  zcodeServers,
		},
		Source{
			Path:  zcode.ProjectAliasRel,
			Scope: Project,
			Read:  zcodeServers,
		},
		Source{
			Path:  zcode.ProjectCompatRel,
			Scope: Project,
			Read:  zcodeCompatServers,
		},
		Source{
			Path:  ".zcode/plugins",
			Scope: Project,
			Read:  zcodePlugins,
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
