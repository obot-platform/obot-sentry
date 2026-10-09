package scan

import (
	"encoding/json"
	"io/fs"
	"path"
	"strings"

	"github.com/obot-platform/obot/apiclient/types"
	"gopkg.in/yaml.v3"
)

// Kiro (the IDE and kiro-cli) keeps everything per-user under ~/.kiro on
// every platform, with the same layout under <workspace>/.kiro:
//
//   - MCP: https://kiro.dev/docs/mcp/configuration/
//   - Skills: https://kiro.dev/docs/skills/
//   - Powers: https://kiro.dev/docs/powers/
//   - Agents: https://kiro.dev/docs/custom-agents/configuration-reference/
//
// The powers layout (installed.json, installed/<name>/) and the
// settings file's powers section aren't documented; they're read from
// the Kiro 1.2 agent extension.
const (
	kiroMCPConfigRel    = ".kiro/settings/mcp.json"
	kiroPowersRel       = ".kiro/powers"
	kiroPowersInstalled = ".kiro/powers/installed"
	kiroPowersDataRel   = ".kiro/powers/data"
	kiroPowersRegistry  = ".kiro/powers/installed.json"
	kiroAgentsRel       = ".kiro/agents"
	kiroPluginManifest  = "plugin.json"
	kiroLegacyManifest  = "POWER.md"
	kiroPowerPluginType = "kiro_power"
	kiroClientName      = "kiro"
)

// kiroServerSpec adds Kiro's `disabled` flag to the shared JSON shape.
// Kiro writes `disabled: true` rather than `enabled: false`.
type kiroServerSpec struct {
	mcpServerSpec
	Disabled bool `json:"disabled"`
}

// kiroServers reads a Kiro mcp.json. The user-level file carries a
// second server table under powers.mcpServers, where Kiro registers the
// servers of legacy (POWER.md) powers as power-<power>-<server>; Kiro
// only reads that section from the user-level file.
func kiroServers(s *state, rel, projectPath string) observations {
	cfg, ok := readJSON[struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
		Powers     struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		} `json:"powers"`
	}](s.fsys, rel)
	if !ok {
		return observations{}
	}
	configPath := s.addFileOrAbs(rel)

	servers := kiroEmitServers(cfg.MCPServers, configPath, projectPath)
	if projectPath == "" {
		servers = append(servers, kiroEmitServers(cfg.Powers.MCPServers, configPath, "")...)
	}
	return observations{servers: servers}
}

// kiroEmitServers converts one raw server table, in name order,
// skipping disabled and undecodable entries. Entries are decoded one at
// a time so a single malformed server doesn't drop its siblings.
func kiroEmitServers(raw map[string]json.RawMessage, configPath, projectPath string) []types.DeviceScanMCPServer {
	out := make([]types.DeviceScanMCPServer, 0, len(raw))
	for _, name := range sortedKeys(raw) {
		var e kiroServerSpec
		if err := json.Unmarshal(raw[name], &e); err != nil || e.Disabled || e.disabled() {
			continue
		}
		// Kiro connects to a URL with Streamable HTTP and only falls
		// back to SSE when that fails, so an untyped remote is the
		// former rather than the shared JSON default of sse.
		if e.Type == "" && e.Transport == "" && firstNonEmpty(e.URL, e.ServerURL) != "" {
			e.Type = "streamable-http"
		}
		out = append(out, e.toServer(name, kiroClientName, configPath, projectPath))
	}
	return out
}

// kiroPowers emits the powers listed in ~/.kiro/powers/installed.json.
// Kiro loads only those entries, so a directory under installed/ that
// the registry doesn't name is not a power, and neither is anything in
// the registry clones next to it; the whole tree is claimed so the walk
// can't re-derive observations from either.
//
// A power is either an Agent Plugin (plugin.json, with its MCP servers
// in the power's own mcp.json) or the legacy format (POWER.md, whose
// servers Kiro copies into the settings file's powers section, where
// kiroServers reports them — so they aren't emitted twice here).
func kiroPowers(s *state, _, _ string) observations {
	s.claim(kiroPowersRel)
	reg, ok := readJSON[struct {
		InstalledPowers []struct {
			Name       string `json:"name"`
			RegistryID string `json:"registryId"`
		} `json:"installedPowers"`
	}](s.fsys, kiroPowersRegistry)
	if !ok {
		return observations{}
	}

	var obs observations
	seen := map[string]bool{}
	for _, p := range reg.InstalledPowers {
		if !kiroPlainSegment(p.Name) || seen[p.Name] {
			continue
		}
		seen[p.Name] = true
		installRel := path.Join(kiroPowersInstalled, p.Name)
		if !dirExists(s.fsys, installRel) {
			continue
		}
		if fileExists(s.fsys, path.Join(installRel, kiroPluginManifest)) {
			obs.add(kiroPluginPower(s, installRel, p.Name, p.RegistryID))
		} else {
			obs.add(kiroLegacyPower(s, installRel, p.Name, p.RegistryID))
		}
	}
	return obs
}

// kiroPluginPower emits an Agent Plugins power. Its mcp.json uses the
// standard mcpServers shape with an explicit type on every entry.
func kiroPluginPower(s *state, installRel, name, registryID string) observations {
	obs := emitPlugin(s, emitPluginOpts{
		installRel:     installRel,
		manifestRel:    path.Join(installRel, kiroPluginManifest),
		pluginType:     kiroPowerPluginType,
		client:         kiroClientName,
		marketplace:    registryID,
		enabled:        true,
		nameFallback:   name,
		nestedMCPRel:   []string{"mcp.json"},
		mcpServerXform: substituteKiroPluginVars(s.abs(installRel), s.abs(path.Join(kiroPowersDataRel, name))),
	})
	kiroMarkSteering(s, obs.plugins, path.Join(installRel, "dev.kiro", "steering"))
	return obs
}

// kiroLegacyPower emits a POWER.md power. The manifest is Markdown with
// YAML frontmatter, which emitPlugin can't parse, so its metadata is
// filled in here; with no JSON manifest and no nestedMCPRel, emitPlugin
// emits no servers (see kiroPowers for why).
func kiroLegacyPower(s *state, installRel, name, registryID string) observations {
	manifestRel := path.Join(installRel, kiroLegacyManifest)
	var description string
	if data, err := fs.ReadFile(s.fsys, manifestRel); err == nil {
		var fmName string
		fmName, description = parseFrontmatter(data)
		if fmName != "" {
			name = fmName
		}
	}
	obs := emitPlugin(s, emitPluginOpts{
		installRel:   installRel,
		manifestRel:  manifestRel,
		pluginType:   kiroPowerPluginType,
		client:       kiroClientName,
		marketplace:  registryID,
		enabled:      true,
		nameFallback: name,
	})
	for i := range obs.plugins {
		obs.plugins[i].Description = description
	}
	kiroMarkSteering(s, obs.plugins, path.Join(installRel, "steering"))
	return obs
}

// kiroMarkSteering reports a power's steering files, Kiro's rules, as
// HasRules.
func kiroMarkSteering(s *state, plugins []types.DeviceScanPlugin, steeringRel string) {
	if !dirExists(s.fsys, steeringRel) {
		return
	}
	for i := range plugins {
		plugins[i].HasRules = true
	}
}

// substituteKiroPluginVars mirrors how Kiro launches an Agent Plugins
// server: ${PLUGIN_ROOT} and ${PLUGIN_DATA} expand in args and env, and
// a ./-relative command resolves against the power's directory.
func substituteKiroPluginVars(root, data string) func(*mcpServerSpec) {
	return func(e *mcpServerSpec) {
		sub := strings.NewReplacer("${PLUGIN_ROOT}", root, "${PLUGIN_DATA}", data).Replace
		if rest, ok := strings.CutPrefix(e.Command, "./"); ok {
			e.Command = root + "/" + rest
		}
		for i, a := range e.Args {
			e.Args[i] = sub(a)
		}
		for k, v := range e.Env {
			if str, ok := v.(string); ok {
				e.Env[k] = sub(str)
			}
		}
	}
}

// kiroPlainSegment matches Kiro's own guard on installed.json names: a
// single path segment, so an entry can't point outside installed/.
func kiroPlainSegment(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, "/\\\x00") &&
		(len(name) < 2 || name[1] != ':')
}

// kiroAgentsMaxDepth and kiroAgentsMaxFiles bound one agents directory, so a
// vendored or generated tree under a workspace's .kiro/agents can't stall the
// scan. The file cap counts every file visited, which is what bounds the work.
const (
	kiroAgentsMaxDepth = 4
	kiroAgentsMaxFiles = 256
)

// kiroAgents reads the custom agent profiles under ~/.kiro/agents, or a
// workspace's .kiro/agents, recursively: .json files, and .md files whose
// YAML frontmatter holds the same fields. Either may declare its own
// mcpServers, which Kiro starts when that agent is in use.
func kiroAgents(s *state, dirRel, projectPath string) observations {
	var obs observations
	files := 0
	_ = fs.WalkDir(s.fsys, dirRel, func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// The scanner's own guards: no dependency or build trees, and
			// a bounded depth, since this now runs in every workspace.
			if rel != dirRel && walkSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			if strings.Count(strings.TrimPrefix(rel, dirRel), "/") >= kiroAgentsMaxDepth {
				return fs.SkipDir
			}
			return nil
		}
		if files >= kiroAgentsMaxFiles {
			return fs.SkipAll
		}
		files++
		var servers map[string]json.RawMessage
		switch path.Ext(rel) {
		case ".json":
			cfg, ok := readJSON[struct {
				MCPServers map[string]json.RawMessage `json:"mcpServers"`
			}](s.fsys, rel)
			if !ok {
				return nil
			}
			servers = cfg.MCPServers
		case ".md":
			servers = kiroFrontmatterServers(s.fsys, rel)
		default:
			return nil
		}
		if len(servers) == 0 {
			return nil
		}
		obs.servers = append(obs.servers, kiroEmitServers(servers, s.addFileOrAbs(rel), projectPath)...)
		return nil
	})
	return obs
}

// kiroFrontmatterServers returns a Markdown agent's frontmatter
// mcpServers, re-encoded as JSON so both agent formats share one
// decoder.
func kiroFrontmatterServers(fsys fs.FS, rel string) map[string]json.RawMessage {
	data, err := fs.ReadFile(fsys, rel)
	if err != nil {
		return nil
	}
	text := strings.TrimLeft(string(data), " \t\r\n")
	if !strings.HasPrefix(text, "---") {
		return nil
	}
	block, _, found := strings.Cut(strings.TrimLeft(text[3:], "\r\n"), "\n---")
	if !found {
		return nil
	}
	var fm struct {
		MCPServers map[string]any `yaml:"mcpServers"`
	}
	if yaml.Unmarshal([]byte(block), &fm) != nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(fm.MCPServers))
	for name, v := range fm.MCPServers {
		if raw, err := json.Marshal(v); err == nil {
			out[name] = raw
		}
	}
	return out
}

// kiroAppDataDir is the IDE's Electron user-data directory, written on
// first run. The IDE's own dot-directory is ~/.kiro, shared with
// kiro-cli.
func kiroAppDataDir(platform string) string {
	switch platform {
	case "darwin":
		return "Library/Application Support/Kiro"
	case "windows":
		return "AppData/Roaming/Kiro"
	default:
		return ".config/Kiro"
	}
}

// kiroCLIDataDir is where kiro-cli keeps its state database.
func kiroCLIDataDir(platform string) string {
	switch platform {
	case "darwin":
		return "Library/Application Support/kiro-cli"
	case "windows":
		return "AppData/Local/kiro-cli"
	default:
		return ".local/share/kiro-cli"
	}
}
