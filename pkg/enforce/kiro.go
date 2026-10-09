package enforce

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
	"github.com/obot-platform/obot-sentry/pkg/toolkind"
	"github.com/obot-platform/obot/apiclient/types"
	"gopkg.in/yaml.v3"
)

// Kiro's PreToolUse payload, as the Kiro 1.2 agent extension builds it:
// session_id, hook_event_name, cwd (the first workspace root), tool_name (the
// tool's id), and tool_input. It names no MCP server, so every MCP call is
// resolved from the tool id and Kiro's own configuration files.
type kiroPreToolPayload struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	CWD       string          `json:"cwd,omitempty"`
}

// Kiro tool ids that carry their MCP target in tool_input rather than in the id.
const (
	// kiroPowersTool runs a Power's MCP tool when action is "use", with the
	// power, server, and tool named in the input. Its other actions (activate,
	// readSteering, readSkill) read the Power's own files.
	kiroPowersTool = "kiro_powers"
	// kiroToolCall runs a deferred MCP tool by "<server>::<tool>" id. Kiro fires
	// hooks for the tool it runs rather than for this wrapper, so a hook should
	// never see it; it is handled so that one that does can't pass as built-in.
	kiroToolCall = "tool_call"
)

// kiroToolIDMax is Kiro's cap on an MCP tool id; longer ids are truncated.
const kiroToolIDMax = 64

// kiroJSWhitespace is the set JavaScript's \s matches, which is what Kiro's
// /[\s-]/g replaces. It is wider than Go's \s (no-break and Unicode spaces) and
// not the same as unicode.IsSpace (it includes U+FEFF and excludes U+0085), so
// it is spelled out rather than borrowed.
func kiroJSWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

// kiroSanitize is the transform Kiro applies to "<server>_<tool>" when it builds
// an MCP tool id (v3u in the extension): whitespace and hyphens become
// underscores, everything else outside [a-zA-Z0-9_] is dropped, and the result
// is lowercased. It is applied character by character, so it distributes over
// the join and a server's contribution can be computed on its own.
func kiroSanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '-' || kiroJSWhitespace(r):
			b.WriteByte('_')
		case r == '_', r >= '0' && r <= '9', r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		}
	}
	return b.String()
}

// kiroServerPrefix is the part of a tool id contributed by a server key.
func kiroServerPrefix(key string) string {
	return "mcp_" + kiroSanitize(key) + "_"
}

func normalizeKiroPreTool(ctx context.Context, env Env, raw []byte) (Call, error) {
	var hook kiroPreToolPayload
	if err := decodePayload(raw, &hook); err != nil {
		return Call{}, err
	}
	toolName := strings.TrimSpace(hook.ToolName)
	if toolName == "" {
		return Call{}, errors.New("pre-tool hook payload has no tool_name")
	}
	if err := boundedField("tool_name", toolName, maxToolNameBytes); err != nil {
		return Call{}, err
	}
	if err := boundedField("cwd", hook.CWD, maxWorkingDirBytes); err != nil {
		return Call{}, err
	}
	if err := boundedField("session_id", hook.SessionID, maxServerNameBytes); err != nil {
		return Call{}, err
	}

	call := Call{Request: types.EnforcementDecisionRequest{Agent: wireAgentKiro, Tool: toolName}}
	target, ok := kiroMCPTarget(toolName, hook.ToolInput)
	if !ok {
		call.Request.Kind = toolkind.KiroKind(toolName)
		return call, nil
	}
	call.Request.Kind = toolkind.KindMCP

	loader := newConfigLoader()
	roots, rootsIncomplete := kiroWorkspaceRoots(ctx, loader, env, hook.SessionID, hook.CWD)
	cfg := newKiroConfig(ctx, loader, env, roots, rootsIncomplete)
	tr := &tracer{}
	res, tool := resolveKiro(ctx, cfg, env, target, tr)
	res.Trace = tr.steps
	call.Trace = res.Trace
	call.Request.Tool = tool
	call.Request.ServerName = res.ServerName
	call.Request.Server = res.Identity
	call.Request.Unresolved = res.Unresolved
	if res.Unresolved {
		call.Request.UnresolvedReason = res.Reason
	}
	return call, nil
}

// kiroTarget is what a Kiro tool call says about its MCP server before any
// configuration is read: either an exact server key and tool (kiro_powers,
// tool_call), or a lossy tool id to match against configured keys.
type kiroTarget struct {
	// key is the exact configuration key, when the call names one.
	key string
	// tool is the tool within the server, when the call names it exactly.
	tool string
	// id is the sanitized tool id, when the call only carries that.
	id string
	// invalid is a reason the call is MCP but names nothing usable.
	invalid string
}

// kiroMCPTarget reports whether a Kiro tool call is an MCP call, and what it
// says about its target.
func kiroMCPTarget(toolName string, input json.RawMessage) (kiroTarget, bool) {
	switch toolName {
	case kiroPowersTool:
		var in struct {
			Action     string `json:"action"`
			PowerName  string `json:"powerName"`
			ServerName string `json:"serverName"`
			ToolName   string `json:"toolName"`
		}
		_ = json.Unmarshal(input, &in)
		if in.Action != "use" {
			return kiroTarget{}, false
		}
		power, server := strings.TrimSpace(in.PowerName), strings.TrimSpace(in.ServerName)
		if power == "" || server == "" {
			return kiroTarget{invalid: "the Kiro Powers call did not name its power and MCP server"}, true
		}
		return kiroTarget{key: kiroPowerServerKey(power, server), tool: strings.TrimSpace(in.ToolName)}, true
	case kiroToolCall:
		var in struct {
			ToolID string `json:"tool_id"`
		}
		_ = json.Unmarshal(input, &in)
		server, tool, ok := strings.Cut(strings.TrimSpace(in.ToolID), "::")
		if !ok || server == "" {
			return kiroTarget{invalid: "the deferred tool call did not name its MCP server"}, true
		}
		return kiroTarget{key: server, tool: tool}, true
	}
	if toolkind.KiroKind(toolName) != toolkind.KindMCP {
		return kiroTarget{}, false
	}
	return kiroTarget{id: toolName}, true
}

// kiroPowerServerKey is the name Kiro registers a Power's server under (Qne in
// the extension), both in the settings file's powers section and for an Agent
// Plugins power's own mcp.json.
func kiroPowerServerKey(power, server string) string {
	return "power-" + power + "-" + server
}

// kiroConfig is every place Kiro can declare an MCP server for one call,
// gathered once.
type kiroConfig struct {
	// files are the mcp.json scopes, ranked (see kiroScopes).
	files []scope
	// agents are the custom agent profiles' own mcpServers tables, all peers.
	agents []scope
	// incomplete names why some workspace root or agent profile could not be
	// read. Anything left out could redefine a server, so an MCP call can't be
	// resolved without it.
	incomplete string
}

// newKiroConfig gathers every scope for the given roots. rootsIncomplete is
// kiroWorkspaceRoots' report of roots it could not establish.
func newKiroConfig(ctx context.Context, loader *configLoader, env Env, roots []string, rootsIncomplete string) kiroConfig {
	agents, agentsIncomplete := kiroAgentScopes(loader, env, roots)
	return kiroConfig{
		files:      kiroScopes(ctx, loader, env, roots),
		agents:     agents,
		incomplete: kiroFirstNonEmpty(rootsIncomplete, agentsIncomplete),
	}
}

func (c kiroConfig) all() []scope {
	return append(slices.Clone(c.files), c.agents...)
}

// resolveKiro resolves a Kiro MCP call and returns the tool name to report.
func resolveKiro(ctx context.Context, cfg kiroConfig, env Env, target kiroTarget, tr *tracer) (Resolution, string) {
	if target.invalid != "" {
		return unresolved("", target.invalid), ""
	}
	if cfg.incomplete != "" {
		return unresolved(target.key, cfg.incomplete), kiroFirstNonEmpty(target.tool, target.id)
	}
	if target.key != "" {
		return resolveKiroKey(ctx, cfg, env, target.key, tr), target.tool
	}

	// The id is mcp_<sanitized server>_<sanitized tool>, so it names no server by
	// itself: a server is a candidate when its key's own contribution is a prefix
	// of the id. More than one candidate is two readings of one name, and like an
	// mcp__ name that splits two ways (resolveSplits), picking one could report
	// an allowlisted server for a call that went to another.
	keys := kiroCandidateKeys(ctx, cfg, target.id)
	switch len(keys) {
	case 0:
		for _, s := range cfg.all() {
			_, res := s.load(ctx)
			tr.miss(s.path, s.traceKey(""), res)
		}
		return unresolved("", fmt.Sprintf(
			"no Kiro MCP configuration declares a server whose tools are named %q", target.id)), target.id
	case 1:
		return resolveKiroKey(ctx, cfg, env, keys[0], tr), kiroToolOf(target.id, keys[0])
	default:
		distinct := map[string]bool{}
		for _, k := range keys {
			distinct[kiroServerPrefix(k)] = true
		}
		if len(distinct) == 1 {
			// Keys that sanitize alike ("my-server", "my_server") produce the same
			// ids; they are one reading only if they define the same server.
			if res, ok := kiroAgreeingKeys(ctx, cfg, env, keys, tr); ok {
				return res, kiroToolOf(target.id, keys[0])
			}
		}
		return ambiguousToolName(localagent.Kiro, target.id, keys), target.id
	}
}

func kiroFirstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// kiroToolOf is the tool half of id once key's prefix is removed. It is Kiro's
// sanitized, lowercased form of the tool name, possibly truncated or suffixed
// with _<n> where two tools collided: the real name is not recoverable offline.
func kiroToolOf(id, key string) string {
	if rest, ok := strings.CutPrefix(id, kiroServerPrefix(key)); ok {
		return rest
	}
	return id
}

// kiroIDMatches reports whether a tool id can have come from server key.
func kiroIDMatches(id, key string) bool {
	prefix := kiroServerPrefix(key)
	if strings.HasPrefix(id, prefix) && len(id) > len(prefix) {
		return true
	}
	// A key long enough that the id cap cut into its own prefix.
	return len(id) == kiroToolIDMax && strings.HasPrefix(prefix, id)
}

// kiroCandidateKeys returns every configured server key, across all of Kiro's
// scopes, that the tool id can have come from.
func kiroCandidateKeys(ctx context.Context, cfg kiroConfig, id string) []string {
	seen := map[string]bool{}
	for _, s := range cfg.all() {
		set, res := s.load(ctx)
		if res != loadOK {
			continue
		}
		for key := range set {
			if kiroIDMatches(id, key) {
				seen[key] = true
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// kiroAgreeingKeys resolves several keys and succeeds only when they all
// identify the same server.
func kiroAgreeingKeys(ctx context.Context, cfg kiroConfig, env Env, keys []string, tr *tracer) (Resolution, bool) {
	var first Resolution
	for i, key := range keys {
		res := resolveKiroKey(ctx, cfg, env, key, tr)
		if res.Unresolved {
			return Resolution{}, false
		}
		if i == 0 {
			first = res
			continue
		}
		if res.Identity != first.Identity {
			return Resolution{}, false
		}
	}
	return first, true
}

// resolveKiroKey resolves an exact server key.
//
// Kiro merges MCP configuration with the workspace over the user file, and a
// custom agent's own mcpServers over both, but the payload does not say which
// agent is running. So agent profiles are peers of whatever the files resolve
// to: a name an agent profile defines differently from the workspace or user
// file is ambiguous. Letting the profile win would let an inactive profile's
// allowlisted definition answer for the server that actually ran, and every
// one of these files is the user's to edit.
//
// A Power's server key (power-<power>-<server>) gets no precedence at all:
// nothing documents whether a same-named entry in mcp.json shadows the
// Power's own server, so every scope that declares the key is a peer and any
// disagreement is ambiguous.
func resolveKiroKey(ctx context.Context, cfg kiroConfig, env Env, key string, tr *tracer) Resolution {
	if cfg.incomplete != "" {
		return unresolved(key, cfg.incomplete)
	}
	names := lookup{names: []string{key}}
	if strings.HasPrefix(key, kiroPowerKeyPrefix) {
		peers := cfg.all()
		for i := range peers {
			peers[i].rank = 0
		}
		m, out := resolveScopes(ctx, peers, names, tr)
		switch out {
		case outcomeFound:
			return resolved(env, m.key, m.entry)
		case outcomeAmbiguous:
			return ambiguous(localagent.Kiro, key)
		default:
			return notFound(localagent.Kiro, key, fmt.Sprintf(
				"MCP server %q was not found in any Kiro MCP configuration", key))
		}
	}
	fileMatch, fileOut := resolveScopes(ctx, cfg.files, names, tr)
	agentMatch, agentOut := resolveScopes(ctx, cfg.agents, names, tr)
	if fileOut == outcomeAmbiguous || agentOut == outcomeAmbiguous {
		return ambiguous(localagent.Kiro, key)
	}
	switch {
	case fileOut == outcomeFound && agentOut == outcomeFound:
		if !sameEntry(fileMatch.entry, agentMatch.entry) {
			return ambiguous(localagent.Kiro, key)
		}
		return resolved(env, fileMatch.key, fileMatch.entry)
	case fileOut == outcomeFound:
		return resolved(env, fileMatch.key, fileMatch.entry)
	case agentOut == outcomeFound:
		return resolved(env, agentMatch.key, agentMatch.entry)
	default:
		return notFound(localagent.Kiro, key, fmt.Sprintf(
			"MCP server %q was not found in any Kiro MCP configuration", key))
	}
}

// kiroPowerKeyPrefix starts every server key Kiro registers for a Power.
const kiroPowerKeyPrefix = "power-"

// kiroMaxWorkspaceRoots bounds how many roots a session file can add.
const kiroMaxWorkspaceRoots = 64

// kiroSessionID matches the ids Kiro gives sessions (sess_<uuid>), and so
// keeps a hostile session_id from naming a path.
var kiroSessionID = regexp.MustCompile(`^sess_[A-Za-z0-9-]{1,128}$`)

// kiroWorkspaceRoots returns every workspace root of the session that made the
// call. The payload's cwd is only the first root, but Kiro merges the MCP
// configuration of all of them, so a server declared by another root has to be
// seen too or it could shadow an allowlisted name undetected.
//
// The roots come from the session's own record,
// ~/.kiro/sessions/<hash>/<session_id>/session.json, whose workspacePaths Kiro
// writes for every IDE and CLI session (Kiro 1.2 and kiro-cli 2.26). That
// file is Kiro's internal state rather than a documented interface. When no
// record exists, only cwd is used, which is the payload's own claim. When a
// record exists but can't be read, or lists more roots than
// kiroMaxWorkspaceRoots, the second result says so and the call is refused:
// carrying on with some roots would let a dropped one shadow a server.
func kiroWorkspaceRoots(ctx context.Context, loader *configLoader, env Env, sessionID, cwd string) ([]string, string) {
	var roots []string
	add := func(root string) {
		root = strings.TrimSpace(root)
		if !filepath.IsAbs(root) {
			return
		}
		root = filepath.Clean(root)
		if !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}
	add(cwd)
	if !kiroSessionID.MatchString(sessionID) {
		return roots, ""
	}
	matches, err := filepath.Glob(env.homePath(".kiro", "sessions", "*", sessionID, "session.json"))
	if err != nil {
		return roots, "the Kiro session record could not be searched for"
	}
	for _, path := range matches {
		var doc struct {
			WorkspacePaths []string `json:"workspacePaths"`
		}
		if res := loader.loadJSON(ctx, path, &doc); res != loadOK {
			return roots, fmt.Sprintf("the Kiro session record %s could not be read", path)
		}
		for _, root := range doc.WorkspacePaths {
			add(root)
		}
	}
	if len(roots) > kiroMaxWorkspaceRoots {
		return roots, fmt.Sprintf("the Kiro session has more than %d workspace roots to check", kiroMaxWorkspaceRoots)
	}
	return roots, ""
}

// kiroScopes returns Kiro's MCP configuration files in precedence order:
//
//  0. each workspace root's .kiro/settings/mcp.json, as peers: Kiro merges
//     them all, and nothing says which root wins a name two of them define;
//  1. the user's ~/.kiro/settings/mcp.json;
//  2. the servers Powers contribute, as peers: the user file's powers section,
//     where legacy (POWER.md) powers are registered, and each installed Agent
//     Plugins power's own mcp.json. Both name their servers power-<power>-<server>
//     (and resolveKiroKey treats those keys without precedence anyway).
func kiroScopes(ctx context.Context, loader *configLoader, env Env, roots []string) []scope {
	var scopes []scope
	for _, root := range roots {
		path := filepath.Join(root, ".kiro", "settings", "mcp.json")
		scopes = append(scopes, scope{path: path, key: mcpServersKey, rank: 0, load: jsonServers(loader, path)})
	}
	user := env.homePath(".kiro", "settings", "mcp.json")
	scopes = append(scopes,
		scope{path: user, key: mcpServersKey, rank: 1, load: jsonServers(loader, user)},
		scope{path: user, key: "powers.mcpServers", rank: 2, load: kiroPowersSectionServers(loader, user)},
	)
	for _, power := range kiroInstalledPowers(ctx, loader, env) {
		path := env.homePath(".kiro", "powers", "installed", power, "mcp.json")
		scopes = append(scopes, scope{path: path, key: mcpServersKey, rank: 2, load: kiroPluginPowerServers(loader, path, power)})
	}
	return scopes
}

// kiroPowersSectionServers loads the settings file's powers.mcpServers table.
func kiroPowersSectionServers(loader *configLoader, path string) func(context.Context) (serverSet, loadResult) {
	return func(ctx context.Context) (serverSet, loadResult) {
		var doc struct {
			Powers struct {
				MCPServers map[string]json.RawMessage `json:"mcpServers"`
			} `json:"powers"`
		}
		if res := loader.loadJSON(ctx, path, &doc); res != loadOK {
			return nil, res
		}
		return decodeServers(doc.Powers.MCPServers), loadOK
	}
}

// kiroPluginPowerServers loads an Agent Plugins power's mcp.json, keyed the way
// Kiro registers its servers.
func kiroPluginPowerServers(loader *configLoader, path, power string) func(context.Context) (serverSet, loadResult) {
	return func(ctx context.Context) (serverSet, loadResult) {
		set, res := jsonServers(loader, path)(ctx)
		if res != loadOK {
			return nil, res
		}
		out := make(serverSet, len(set))
		for name, entry := range set {
			out[kiroPowerServerKey(power, name)] = entry
		}
		return out, loadOK
	}
}

// kiroInstalledPowers lists the Agent Plugins powers Kiro loads: those named in
// ~/.kiro/powers/installed.json whose directory has a plugin.json. A legacy
// power's servers are already in the settings file's powers section.
func kiroInstalledPowers(ctx context.Context, loader *configLoader, env Env) []string {
	var doc struct {
		InstalledPowers []struct {
			Name string `json:"name"`
		} `json:"installedPowers"`
	}
	if loader.loadJSON(ctx, env.homePath(".kiro", "powers", "installed.json"), &doc) != loadOK {
		return nil
	}
	var out []string
	for _, p := range doc.InstalledPowers {
		name := p.Name
		// Kiro's own guard: one plain path segment.
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`+"\x00") {
			continue
		}
		if _, err := os.Stat(env.homePath(".kiro", "powers", "installed", name, "plugin.json")); err == nil {
			out = append(out, name)
		}
	}
	return out
}

// kiroMaxAgentFiles bounds how many custom agent profiles a hook reads, so a
// huge agents tree can't stall a tool call past the hook's budget. Past it, MCP
// calls are unresolved rather than resolved without the rest.
const kiroMaxAgentFiles = 256

// kiroAgentScopes returns every custom agent profile's own mcpServers table,
// all as peers: the user's ~/.kiro/agents and each workspace root's
// .kiro/agents, .json profiles and .md profiles with YAML frontmatter, searched
// recursively the way Kiro loads them. The second result is non-empty when the
// list is incomplete, because a directory could not be read or there were more
// profiles than kiroMaxAgentFiles.
func kiroAgentScopes(loader *configLoader, env Env, roots []string) ([]scope, string) {
	dirs := []string{env.homePath(".kiro", "agents")}
	for _, root := range roots {
		dirs = append(dirs, filepath.Join(root, ".kiro", "agents"))
	}
	var (
		scopes     []scope
		incomplete string
	)
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == dir && errors.Is(err, fs.ErrNotExist) {
					return nil // no agents directory here
				}
				incomplete = fmt.Sprintf("the Kiro agent profiles under %s could not all be read", dir)
				return fs.SkipAll
			}
			if d.IsDir() {
				return nil
			}
			var load func(context.Context) (serverSet, loadResult)
			switch filepath.Ext(path) {
			case ".json":
				load = jsonServers(loader, path)
			case ".md":
				load = kiroMarkdownAgentServers(loader, path)
			default:
				return nil
			}
			if len(scopes) == kiroMaxAgentFiles {
				incomplete = fmt.Sprintf("there are more than %d Kiro agent profiles to check", kiroMaxAgentFiles)
				return fs.SkipAll
			}
			scopes = append(scopes, scope{path: path, key: mcpServersKey, load: load})
			return nil
		})
	}
	return scopes, incomplete
}

// kiroMarkdownAgentServers loads the mcpServers table from a Markdown agent
// profile's YAML frontmatter.
func kiroMarkdownAgentServers(loader *configLoader, path string) func(context.Context) (serverSet, loadResult) {
	return func(ctx context.Context) (serverSet, loadResult) {
		data, res := loader.readConfig(ctx, path)
		if res != loadOK {
			return nil, res
		}
		text := bytes.TrimLeft(data, " \t\r\n")
		if !bytes.HasPrefix(text, []byte("---")) {
			return serverSet{}, loadOK
		}
		block, _, found := bytes.Cut(bytes.TrimLeft(text[3:], "\r\n"), []byte("\n---"))
		if !found {
			return serverSet{}, loadOK
		}
		var fm struct {
			MCPServers map[string]any `yaml:"mcpServers"`
		}
		if yaml.Unmarshal(block, &fm) != nil {
			return nil, loadUnusable
		}
		raw := make(map[string]json.RawMessage, len(fm.MCPServers))
		for name, v := range fm.MCPServers {
			if b, err := json.Marshal(v); err == nil {
				raw[name] = b
			}
		}
		return decodeServers(raw), loadOK
	}
}
