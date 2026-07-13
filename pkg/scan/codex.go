package scan

import (
	"cmp"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/obot-platform/obot/apiclient/types"
)

// Codex stores user-level configuration under ~/.codex on every
// platform: https://developers.openai.com/codex/config-basic
const (
	codexGlobalConfigRel   = ".codex/config.toml"
	codexPluginCacheRel    = ".codex/plugins/cache"
	codexPluginManifestSub = ".codex-plugin/plugin.json"
)

// codexConfig is Codex's TOML shape: a top-level mcp_servers map of
// named entries. Codex has a unique header model captured separately
// (see codexEntry.toServer).
type codexConfig struct {
	MCPServers map[string]codexEntry `toml:"mcp_servers"`
}

type codexEntry struct {
	Type              string         `toml:"type"`
	Transport         string         `toml:"transport"`
	Command           string         `toml:"command"`
	Args              []string       `toml:"args"`
	URL               string         `toml:"url"`
	Env               map[string]any `toml:"env"`
	HTTPHeaders       map[string]any `toml:"http_headers"`
	EnvHTTPHeaders    map[string]any `toml:"env_http_headers"`
	BearerTokenEnvVar string         `toml:"bearer_token_env_var"`
	Enabled           *bool          `toml:"enabled"`
}

type codexScanner struct{}

func (codexScanner) Name() string { return "codex" }

func (codexScanner) Presence(string) presenceDef {
	return presenceDef{binaries: []string{"codex"}, configPaths: []string{".codex"}}
}

func (codexScanner) GlobalConfigs(string) []string { return []string{codexGlobalConfigRel} }

func (codexScanner) ProjectConfigs() []string { return []string{".codex/config.toml"} }

func (c codexScanner) ScanHome(s *state) observations {
	var obs observations
	if cfg, ok := readTOML[codexConfig](s.fsys, codexGlobalConfigRel); ok {
		configPath := s.addFileOrAbs(codexGlobalConfigRel)
		obs.servers = codexEmit(cfg.MCPServers, configPath, "")
	}
	obs.add(c.scanPlugins(s))
	return obs
}

func (codexScanner) ScanProject(s *state, configRel string) observations {
	cfg, ok := readTOML[codexConfig](s.fsys, configRel)
	if !ok {
		return observations{}
	}
	configPath := s.addFileOrAbs(configRel)
	projectPath := s.abs(path.Dir(path.Dir(configRel)))
	return observations{servers: codexEmit(cfg.MCPServers, configPath, projectPath)}
}

func codexEmit(servers map[string]codexEntry, configPath, projectPath string) []types.DeviceScanMCPServer {
	out := make([]types.DeviceScanMCPServer, 0, len(servers))
	for _, name := range sortedKeys(servers) {
		e := servers[name]
		if e.Enabled != nil && !*e.Enabled {
			continue
		}
		out = append(out, e.toServer(name, configPath, projectPath))
	}
	return out
}

// toServer converts a [mcp_servers.<name>] table into wire shape.
// Codex header semantics: http_headers (literal map), env_http_headers
// (header_name → env_var), and bearer_token_env_var (yields an
// "Authorization" header). Only header *names* propagate to HeaderKeys.
func (e codexEntry) toServer(name, configPath, projectPath string) types.DeviceScanMCPServer {
	transport := codexTransport(e.Type, e.Transport, e.URL)

	headerNames := map[string]bool{}
	for k := range e.HTTPHeaders {
		headerNames[k] = true
	}
	for k := range e.EnvHTTPHeaders {
		headerNames[k] = true
	}
	if e.BearerTokenEnvVar != "" {
		headerNames["Authorization"] = true
	}

	return types.DeviceScanMCPServer{
		Client:      "codex",
		ProjectPath: projectPath,
		File:        configPath,
		Name:        name,
		Transport:   transport,
		Command:     e.Command,
		Args:        e.Args,
		URL:         e.URL,
		EnvKeys:     sortedKeys(e.Env),
		HeaderKeys:  sortedKeys(headerNames),
		ConfigHash:  mcpConfigHash(name, transport, e.Command, e.Args, e.URL),
	}
}

// codexTransport differs from the standard rule because Codex defaults
// remote (URL-only) servers to streamable-http rather than sse.
func codexTransport(typeField, transportField, urlField string) string {
	if explicit := firstNonEmpty(typeField, transportField); explicit != "" {
		return canonicalTransport(explicit)
	}
	if urlField != "" {
		return "streamable-http"
	}
	return "stdio"
}

// scanPlugins walks .codex/plugins/cache/<marketplace>/<plugin>/<ver>/
// and emits a plugin observation for the highest version of each plugin
// that has a manifest at .codex-plugin/plugin.json.
func (codexScanner) scanPlugins(s *state) observations {
	mkts, err := fs.ReadDir(s.fsys, codexPluginCacheRel)
	if err != nil {
		return observations{}
	}
	var obs observations
	for _, mkt := range mkts {
		if !mkt.IsDir() {
			continue
		}
		mktRel := path.Join(codexPluginCacheRel, mkt.Name())
		plugins, err := fs.ReadDir(s.fsys, mktRel)
		if err != nil {
			slog.Debug("codex: skipping marketplace", "path", mktRel, "err", err)
			continue
		}
		for _, p := range plugins {
			if !p.IsDir() {
				continue
			}
			pluginRel := path.Join(mktRel, p.Name())
			// Claim every version dir, not just the one emitted, so
			// stale versions don't leak observations through the walk.
			s.claim(pluginRel)
			versionRel, version, ok := pickHighestVersionDir(s.fsys, pluginRel)
			if !ok {
				continue
			}
			manifestRel := path.Join(versionRel, codexPluginManifestSub)
			if !fileExists(s.fsys, manifestRel) {
				continue
			}
			obs.add(emitPlugin(s, emitPluginOpts{
				installRel:      versionRel,
				manifestRel:     manifestRel,
				pluginType:      "codex_plugin",
				client:          "codex",
				marketplace:     mkt.Name(),
				enabled:         true,
				nameFallback:    p.Name(),
				versionFallback: version,
				nestedMCPRel:    []string{"mcp.json", ".mcp.json"},
			}))
		}
	}
	return obs
}

// pickHighestVersionDir returns the version subdirectory with the
// highest version-ordered name, the directory's basename (the version
// string), and ok. Non-directory entries are ignored.
func pickHighestVersionDir(fsys fs.FS, pluginRel string) (string, string, bool) {
	entries, err := fs.ReadDir(fsys, pluginRel)
	if err != nil {
		slog.Debug("codex: skipping plugin", "path", pluginRel, "err", err)
		return "", "", false
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() {
			versions = append(versions, e.Name())
		}
	}
	if len(versions) == 0 {
		return "", "", false
	}
	top := slices.MaxFunc(versions, compareVersionNames)
	return path.Join(pluginRel, top), top, true
}

// compareVersionNames orders version-directory names: dotted segments
// compare pairwise (missing segments count as zero, so 2.1.5 > 2.1), a
// release outranks any prerelease of the same version (1.2.3 >
// 1.2.3-rc1), and prerelease token streams compare pairwise
// (1.2.3-rc2 > 1.2.3-rc1).
func compareVersionNames(a, b string) int {
	aVersion, aPrerelease, _ := strings.Cut(a, "-")
	bVersion, bPrerelease, _ := strings.Cut(b, "-")

	aSegments := strings.Split(aVersion, ".")
	bSegments := strings.Split(bVersion, ".")
	for i := range max(len(aSegments), len(bSegments)) {
		var aSegment, bSegment string
		if i < len(aSegments) {
			aSegment = aSegments[i]
		}
		if i < len(bSegments) {
			bSegment = bSegments[i]
		}
		if c := compareVersionSegment(aSegment, bSegment); c != 0 {
			return c
		}
	}

	switch {
	case aPrerelease == "" && bPrerelease == "":
		return 0
	case aPrerelease == "":
		return 1
	case bPrerelease == "":
		return -1
	}
	aTokens := alphaNumRe.FindAllString(aPrerelease, -1)
	bTokens := alphaNumRe.FindAllString(bPrerelease, -1)
	for i := range min(len(aTokens), len(bTokens)) {
		if c := compareVersionSegment(aTokens[i], bTokens[i]); c != 0 {
			return c
		}
	}
	return len(aTokens) - len(bTokens)
}

var alphaNumRe = regexp.MustCompile(`[A-Za-z]+|\d+`)

// compareVersionSegment compares two version segments: numerically when
// both are numbers (empty counts as zero), lexically when both are
// words; a word ranks above a number.
func compareVersionSegment(a, b string) int {
	aNum, aErr := strconv.Atoi(cmp.Or(a, "0"))
	bNum, bErr := strconv.Atoi(cmp.Or(b, "0"))
	switch {
	case aErr == nil && bErr == nil:
		return cmp.Compare(aNum, bNum)
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}
