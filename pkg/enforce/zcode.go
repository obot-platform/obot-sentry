package enforce

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
	"github.com/obot-platform/obot-sentry/pkg/toolkind"
	zcode "github.com/obot-platform/obot-sentry/pkg/zcode"
)

type zcodePreToolPayload struct {
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
}

type zcodeMCPEntry struct {
	Type      string            `json:"type"`
	URL       string            `json:"url"`
	ServerURL string            `json:"serverUrl"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	Enable    *bool             `json:"enable"`
	Enabled   *bool             `json:"enabled"`
}

type zcodeConfig struct {
	MCP struct {
		Servers map[string]json.RawMessage `json:"servers"`
	} `json:"mcp"`
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

var zcodeBuiltinKinds = map[string]string{
	"read":               toolkind.KindRead,
	"write":              toolkind.KindWrite,
	"edit":               toolkind.KindWrite,
	"patch":              toolkind.KindWrite,
	"delete":             toolkind.KindWrite,
	"bash":               toolkind.KindShell,
	"shell":              toolkind.KindShell,
	"terminal":           toolkind.KindShell,
	"task":               toolkind.KindTask,
	"agent":              toolkind.KindTask,
	"glob":               toolkind.KindGeneric,
	"grep":               toolkind.KindRead,
	"question":           toolkind.KindGeneric,
	"skill":              toolkind.KindGeneric,
	"webfetch":           toolkind.KindGeneric,
	"websearch":          toolkind.KindGeneric,
	"external_directory": toolkind.KindGeneric,
	"list":               toolkind.KindGeneric,
	"todowrite":          toolkind.KindGeneric,
}

func normalizeZCodePreTool(ctx context.Context, env Env, raw []byte) (Call, error) {
	var hook zcodePreToolPayload
	if err := decodePayload(raw, &hook); err != nil {
		return Call{}, err
	}
	toolName := strings.TrimSpace(hook.ToolName)
	if toolName == "" {
		return Call{}, fmt.Errorf("ZCode PreToolUse payload has no tool_name")
	}
	if err := boundedField("tool_name", toolName, maxToolNameBytes); err != nil {
		return Call{}, err
	}
	if err := boundedField("cwd", hook.CWD, maxWorkingDirBytes); err != nil {
		return Call{}, err
	}
	class := classifyZCodeAction(ctx, env, toolName, hook.CWD)
	return buildCall(ctx, env, localagent.ZCode, class, ResolveRequest{
		Agent:      localagent.ZCode,
		ServerName: class.ServerName,
		CWD:        hook.CWD,
	}), nil
}

func classifyZCodeAction(ctx context.Context, env Env, action, cwd string) classification {
	if kind, ok := zcodeBuiltinKinds[strings.ToLower(action)]; ok {
		return classification{Kind: kind, Tool: action}
	}
	candidates, ambiguous, err := zcodeServerCandidates(ctx, env, action, cwd)
	if err != nil {
		return classification{Kind: toolkind.KindMCP, Tool: action, InvalidReason: "the ZCode configuration could not be read: " + err.Error()}
	}
	if ambiguous {
		return classification{Kind: toolkind.KindMCP, Tool: action, InvalidReason: "two configured ZCode MCP server names collapse to the same tool namespace"}
	}
	if len(candidates) == 0 {
		return classification{Kind: toolkind.KindMCP, Tool: action, InvalidReason: fmt.Sprintf("ZCode action %q is not a known built-in and matches no configured MCP server", action)}
	}
	parts := make([]mcpSplit, 0, len(candidates))
	for _, candidate := range candidates {
		parts = append(parts, mcpSplit{Server: candidate.name, Tool: candidate.tool})
	}
	return classification{Kind: toolkind.KindMCP, Tool: parts[0].Tool, ServerName: parts[0].Server, Splits: parts}
}

type zcodeCandidate struct {
	name string
	tool string
}

func zcodeServerCandidates(ctx context.Context, env Env, action, cwd string) ([]zcodeCandidate, bool, error) {
	servers, err := zcodeConfiguredServers(ctx, newConfigLoader(), env, cwd)
	if err != nil {
		return nil, false, err
	}
	type candidate struct {
		name string
		tool string
	}
	byNamespace := map[string]candidate{}
	for name := range servers {
		for _, namespace := range zcodeNamespaces(name) {
			tool, ok := zcodeToolAfterNamespace(action, namespace)
			if !ok {
				continue
			}
			entry := candidate{name: name, tool: tool}
			if prior, exists := byNamespace[namespace]; exists && prior.name != name {
				return nil, true, nil
			}
			byNamespace[namespace] = entry
		}
	}
	out := make([]zcodeCandidate, 0, len(byNamespace))
	for _, candidate := range byNamespace {
		out = append(out, zcodeCandidate(candidate))
	}
	slices.SortFunc(out, func(a, b zcodeCandidate) int {
		if len(a.name) != len(b.name) {
			return len(b.name) - len(a.name)
		}
		return strings.Compare(a.name, b.name)
	})
	return out, false, nil
}

func zcodeToolAfterNamespace(action, namespace string) (string, bool) {
	candidateAction := action
	if strings.HasPrefix(strings.ToLower(candidateAction), "mcp__") {
		candidateAction = candidateAction[5:]
	}
	for _, separator := range []string{"__", "_"} {
		prefix := namespace + separator
		if strings.HasPrefix(candidateAction, prefix) && len(candidateAction) > len(prefix) {
			return strings.TrimPrefix(candidateAction, prefix), true
		}
	}
	return "", false
}

func zcodeNamespaces(name string) []string {
	clean := strings.TrimSpace(name)
	if clean == "" {
		return nil
	}
	form := formZCode(clean)
	out := []string{form}
	// Plugin MCP keys are documented as plugin:<plugin>:<server>. Keep the
	// protocol spelling as a candidate in addition to the model-facing folded
	// namespace; do not invent other punctuation variants.
	if strings.Contains(clean, ":") && clean != form {
		out = append([]string{clean}, out...)
	}
	return out
}

func resolveZCode(ctx context.Context, loader *configLoader, env Env, req ResolveRequest, serverName string, tr *tracer) Resolution {
	scopes, unreadable := zcodeScopes(ctx, loader, env, req.CWD)
	match, outcome := resolveScopes(ctx, scopes, lookup{names: []string{serverName}}, tr)
	if len(unreadable) > 0 {
		return unresolved(serverName, fmt.Sprintf("ZCode MCP configuration %q could not be read", unreadable[0]))
	}
	switch outcome {
	case outcomeFound:
		return resolved(env, match.key, match.entry)
	case outcomeAmbiguous:
		return ambiguous(localagent.ZCode, serverName)
	default:
		return notFound(localagent.ZCode, serverName, fmt.Sprintf("MCP server %q was not found in ZCode configuration", serverName))
	}
}

func zcodeScopes(ctx context.Context, loader *configLoader, env Env, cwd string) ([]scope, []string) {
	config := zcode.NewConfig(env.Home, env.getenv)
	scopes := make([]scope, 0, 4)
	unreadable := make([]string, 0, 2)
	add := func(path, key string, rank int, compat bool) {
		if path == "" {
			return
		}
		scopes = append(scopes, scope{
			path: path,
			key:  key,
			rank: rank,
			load: func(ctx context.Context) (serverSet, loadResult) {
				var doc zcodeConfig
				res := loader.loadJSON(ctx, path, &doc)
				if res == loadUnusable {
					unreadable = append(unreadable, path)
				}
				if res != loadOK {
					return nil, res
				}
				return zcodeDocumentServers(doc, env, compat), loadOK
			},
		})
	}

	userNative := config.UserConfigPath()
	present, hasServers, userRes := zcodePathHasServers(ctx, loader, userNative)
	switch {
	case userRes == loadUnusable:
		add(userNative, "mcp.servers", 0, false)
	case present && hasServers:
		add(userNative, "mcp.servers", 0, false)
	default:
		add(config.UserCompatPath(), "mcpServers", 0, true)
	}

	dirs := ancestors(cwd)
	projectDir := ""
	for _, dir := range dirs {
		var native string
		for _, candidate := range []string{
			filepath.Join(dir, ".zcode", "config.json"),
			filepath.Join(dir, zcode.ProjectAliasRel),
		} {
			if fileExists(candidate) {
				native = candidate
				break
			}
		}
		if native == "" {
			compat := filepath.Join(dir, ".agents", "mcp.json")
			if fileExists(compat) {
				projectDir = dir
				add(compat, "mcpServers", 1, true)
				break
			}
			continue
		}
		_, hasServers, nativeRes := zcodePathHasServers(ctx, loader, native)
		if nativeRes == loadUnusable || hasServers {
			projectDir = dir
			add(native, "mcp.servers", 1, false)
			break
		}
		compat := filepath.Join(dir, ".agents", "mcp.json")
		if fileExists(compat) {
			projectDir = dir
			add(compat, "mcpServers", 1, true)
			break
		}
	}
	scopes = append(scopes, zcodePluginScopes(ctx, loader, env, config, projectDir, cwd)...)
	return scopes, unreadable
}

func zcodePathHasServers(ctx context.Context, loader *configLoader, path string) (present, hasServers bool, res loadResult) {
	if !fileExists(path) {
		return false, false, loadAbsent
	}
	var doc zcodeConfig
	res = loader.loadJSON(ctx, path, &doc)
	if res != loadOK {
		return true, false, res
	}
	return true, len(doc.MCP.Servers) > 0 || len(doc.MCPServers) > 0, loadOK
}

func fileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && !info.IsDir()
}

func zcodePluginScopes(ctx context.Context, loader *configLoader, env Env, config zcode.Config, projectDir, cwd string) []scope {
	roots := []string{
		filepath.Join(config.Dir(), "plugins"),
		filepath.Join(config.Dir(), "cli", "plugins"),
	}
	if projectDir != "" {
		roots = append(roots, filepath.Join(projectDir, ".zcode", "plugins"))
	}
	seen := map[string]struct{}{}
	scopes := make([]scope, 0)
	for _, root := range roots {
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		_ = filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return filepath.SkipAll
			}
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				if current != root && zcodePluginSkipDir(entry.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Name() != "plugin.json" {
				return nil
			}
			parent := filepath.Base(filepath.Dir(current))
			if parent != ".zcode-plugin" && parent != ".claude-plugin" {
				return nil
			}
			installDir := filepath.Dir(filepath.Dir(current))
			key := filepath.Clean(installDir)
			if _, ok := seen[key]; ok {
				return nil
			}
			seen[key] = struct{}{}
			set, sourcePath, ok := zcodePluginServers(ctx, loader, env, config.Dir(), installDir, current, cwd)
			if !ok {
				return nil
			}
			scopes = append(scopes, scope{
				path: sourcePath,
				key:  "mcpServers",
				rank: 2,
				load: fixedServers(set, loadOK),
			})
			return nil
		})
	}
	return scopes
}

func zcodePluginSkipDir(name string) bool {
	switch name {
	case "node_modules", ".git", "dist", "build", "v2", "logs":
		return true
	default:
		return false
	}
}

func zcodePluginPath(installDir, relative string) (string, bool) {
	if strings.TrimSpace(relative) == "" {
		return "", false
	}
	path := filepath.Join(installDir, filepath.FromSlash(relative))
	relToRoot, err := filepath.Rel(installDir, path)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", false
	}
	return path, true
}

func zcodePluginRawServers(ctx context.Context, loader *configLoader, installDir string, raw json.RawMessage, manifestPath string) (map[string]json.RawMessage, string, bool) {
	if len(raw) == 0 {
		return nil, manifestPath, true
	}
	var inline map[string]json.RawMessage
	if json.Unmarshal(raw, &inline) == nil {
		return inline, manifestPath, true
	}
	var relative string
	if json.Unmarshal(raw, &relative) == nil && strings.TrimSpace(relative) != "" {
		path, ok := zcodePluginPath(installDir, relative)
		if !ok {
			return nil, filepath.Join(installDir, filepath.FromSlash(relative)), false
		}
		var doc zcodeConfig
		if res := loader.loadJSON(ctx, path, &doc); res != loadOK {
			return nil, path, false
		}
		return doc.MCPServers, path, true
	}
	var relatives []string
	if json.Unmarshal(raw, &relatives) == nil && len(relatives) > 0 {
		merged := map[string]json.RawMessage{}
		source := manifestPath
		for _, relative := range relatives {
			path, ok := zcodePluginPath(installDir, relative)
			if !ok {
				return nil, filepath.Join(installDir, filepath.FromSlash(relative)), false
			}
			var doc zcodeConfig
			if res := loader.loadJSON(ctx, path, &doc); res != loadOK {
				return nil, path, false
			}
			for name, entry := range doc.MCPServers {
				merged[name] = entry
			}
			source = path
		}
		return merged, source, true
	}
	return nil, manifestPath, false
}

func zcodePluginServers(ctx context.Context, loader *configLoader, env Env, dataBaseDir, installDir, manifestPath, cwd string) (serverSet, string, bool) {
	var manifest struct {
		Name       string          `json:"name"`
		MCPServers json.RawMessage `json:"mcpServers"`
		UserConfig map[string]struct {
			Default json.RawMessage `json:"default"`
		} `json:"userConfig"`
	}
	if res := loader.loadJSON(ctx, manifestPath, &manifest); res != loadOK || strings.TrimSpace(manifest.Name) == "" {
		return nil, "", false
	}
	raw, sourcePath, ok := zcodePluginRawServers(ctx, loader, installDir, manifest.MCPServers, manifestPath)
	if !ok {
		return nil, "", false
	}
	nested := filepath.Join(installDir, ".mcp.json")
	if info, err := os.Stat(nested); err == nil && !info.IsDir() {
		var doc zcodeConfig
		if res := loader.loadJSON(ctx, nested, &doc); res != loadOK {
			return nil, "", false
		}
		raw = doc.MCPServers
		sourcePath = nested
	}
	if len(raw) == 0 {
		return nil, "", false
	}
	set := make(serverSet, len(raw))
	for server, message := range raw {
		var entry zcodeMCPEntry
		if err := json.Unmarshal(message, &entry); err != nil {
			continue
		}
		if (entry.Enable != nil && !*entry.Enable) || (entry.Enabled != nil && !*entry.Enabled) {
			continue
		}
		converted := mcpEntry{
			URL:         zcodeFirstNonEmpty(entry.URL, entry.ServerURL),
			Command:     entry.Command,
			Args:        append([]string(nil), entry.Args...),
			Environment: entry.Env,
		}
		if converted.URL == "" {
			converted.Connector = "zcode-plugin:" + manifest.Name + ":" + server
		}
		pluginID := zcodePluginID(installDir, manifest.Name)
		set["plugin:"+manifest.Name+":"+server] = expandZCodePluginEntry(
			converted,
			env,
			installDir,
			filepath.Join(dataBaseDir, "cli", "plugins", "data", pluginID),
			cwd,
			manifest.Name,
			pluginID,
			manifest.UserConfig,
		)
	}
	return set, sourcePath, len(set) > 0
}

var zcodePluginEnvPattern = regexp.MustCompile(`\$\{([^}:]+)(?:(:-|-)([^}]*))?\}`)

func zcodePluginID(installDir, pluginName string) string {
	pluginDir := filepath.Dir(installDir)
	marketplaceDir := filepath.Dir(pluginDir)
	if !strings.Contains(filepath.ToSlash(marketplaceDir), "/cache/") {
		return pluginName
	}
	marketplace := filepath.Base(marketplaceDir)
	if marketplace == "." || marketplace == string(filepath.Separator) || marketplace == "" {
		return pluginName
	}
	return pluginName + "@" + marketplace
}

func expandZCodePluginEntry(entry mcpEntry, env Env, pluginRoot, pluginDataDir, projectDir, pluginName, pluginID string, defaults map[string]struct {
	Default json.RawMessage `json:"default"`
}) mcpEntry {
	missing := false
	lookup := func(name string) (string, bool) {
		switch name {
		case "ZCODE_PLUGIN_ROOT", "CLAUDE_PLUGIN_ROOT":
			return pluginRoot, true
		case "ZCODE_PLUGIN_DATA", "CLAUDE_PLUGIN_DATA":
			return pluginDataDir, true
		case "ZCODE_PLUGIN_NAME", "CLAUDE_PLUGIN_NAME":
			return pluginName, true
		case "ZCODE_PLUGIN_ID", "CLAUDE_PLUGIN_ID":
			return pluginID, true
		case "ZCODE_PROJECT_DIR", "CLAUDE_PROJECT_DIR":
			return projectDir, true
		}
		if strings.HasPrefix(name, "user_config.") {
			key := strings.TrimPrefix(name, "user_config.")
			value, ok := defaults[key]
			if !ok {
				return "", false
			}
			var text string
			if json.Unmarshal(value.Default, &text) == nil {
				return text, true
			}
			return string(value.Default), true
		}
		value := env.getenv(name)
		return value, value != ""
	}
	expand := func(value string) string {
		return zcodePluginEnvPattern.ReplaceAllStringFunc(value, func(token string) string {
			parts := zcodePluginEnvPattern.FindStringSubmatch(token)
			if len(parts) != 4 {
				missing = true
				return token
			}
			if value, ok := lookup(parts[1]); ok {
				return value
			}
			if parts[2] != "" {
				return parts[3]
			}
			missing = true
			return token
		})
	}
	entry.URL = expand(entry.URL)
	entry.Command = expand(entry.Command)
	for i := range entry.Args {
		entry.Args[i] = expand(entry.Args[i])
	}
	if len(entry.Environment) > 0 {
		expanded := make(map[string]string, len(entry.Environment))
		for key, value := range entry.Environment {
			expanded[key] = expand(value)
		}
		entry.Environment = expanded
	}
	if missing {
		entry.ConfigError = "ZCode plugin MCP configuration contains an undefined template variable"
	}
	return entry
}

func zcodeDocumentServers(doc zcodeConfig, env Env, compat bool) serverSet {
	raw := doc.MCPServers
	if !compat {
		raw = doc.MCP.Servers
		if raw == nil {
			raw = doc.MCPServers
		}
	}
	return zcodeServerSet(raw, env)
}

func zcodeFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func zcodeServerSet(raw map[string]json.RawMessage, env Env) serverSet {
	set := make(serverSet, len(raw))
	for name, message := range raw {
		var entry zcodeMCPEntry
		if err := json.Unmarshal(message, &entry); err != nil {
			set[name] = mcpEntry{}
			continue
		}
		if (entry.Enable != nil && !*entry.Enable) || (entry.Enabled != nil && !*entry.Enabled) {
			set[name] = mcpEntry{Disabled: true}
			continue
		}
		converted := mcpEntry{
			URL:         zcodeFirstNonEmpty(entry.URL, entry.ServerURL),
			Command:     entry.Command,
			Args:        append([]string(nil), entry.Args...),
			Environment: entry.Env,
		}
		// Native and .agents compatibility files are literal ZCode config;
		// ZCode does not expand shell-style variables there.
		set[name] = converted
	}
	return set
}

func zcodeConfiguredServers(ctx context.Context, loader *configLoader, env Env, cwd string) (serverSet, error) {
	scopes, unreadable := zcodeScopes(ctx, loader, env, cwd)
	if len(unreadable) > 0 {
		return nil, fmt.Errorf("%s could not be read", unreadable[0])
	}
	merged := serverSet{}
	for _, source := range scopes {
		set, res := source.load(ctx)
		if res == loadUnusable {
			return nil, fmt.Errorf("%s could not be read", source.path)
		}
		if res != loadOK {
			continue
		}
		for name, entry := range set {
			merged[name] = entry
		}
	}
	return merged, nil
}
