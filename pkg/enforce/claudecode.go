package enforce

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Claude Code configuration file locations, slash-separated as machinePath
// takes them. Linux is deliberately absent
// throughout: obot-sentry builds for darwin and windows only, and
// hookinstall.supportedPlatform rejects everything else.
//
// Claude Code hardcodes its managed directory on Windows and does not consult
// %PROGRAMFILES%. Neither may the resolver: the variable is inherited from the
// agent, so honoring it would let a user point the resolver at a managed-mcp.json
// of their own that Claude Code never reads, and have it vouch for a server that
// is not the one running.
const (
	claudeManagedDirDarwin  = "/Library/Application Support/ClaudeCode"
	claudeManagedDirWindows = "C:/Program Files/ClaudeCode"
	claudeManagedMCPFile    = "managed-mcp.json"
	claudeManagedSettings   = "managed-settings.json"
	claudeManagedDropInDir  = "managed-settings.d"
	// claudeManagedServersKey is the managed settings key that delivers MCP servers.
	claudeManagedServersKey = "managedMcpServers"
	// maxClaudeManagedDropIns bounds the managed-settings.d files read per call.
	maxClaudeManagedDropIns = 64
	// claudeAIConnectorPrefix namespaces a claude.ai account connector's tools.
	claudeAIConnectorPrefix = "claude_ai_"
	// claudeConfigDirEnv relocates Claude Code's user configuration, .claude.json
	// included, away from the home directory.
	claudeConfigDirEnv = "CLAUDE_CONFIG_DIR"
)

// claudeJSON is the part of .claude.json the resolver reads.
type claudeJSON struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
	Projects   map[string]struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	} `json:"projects"`
	// ClaudeAIMCPEverConnected lists the display names of claude.ai account
	// connectors this installation has connected to. It is the only local evidence
	// such a connector exists.
	ClaudeAIMCPEverConnected []string `json:"claudeAiMcpEverConnected"`
}

type cachedClaudeJSON struct {
	doc claudeJSON
	res loadResult
}

func (l *configLoader) loadClaudeJSON(ctx context.Context, path string) (claudeJSON, loadResult) {
	if ctx.Err() != nil {
		return claudeJSON{}, loadUnusable
	}
	if cached, ok := l.claudeDocs[path]; ok {
		return cached.doc, cached.res
	}
	var doc claudeJSON
	res := l.loadJSON(ctx, path, &doc)
	if ctx.Err() != nil {
		return claudeJSON{}, loadUnusable
	}
	l.claudeDocs[path] = cachedClaudeJSON{doc: doc, res: res}
	return doc, res
}

// claudeManagedPath returns a file in Claude Code's managed configuration
// directory.
func (e Env) claudeManagedPath(elem ...string) string {
	dir := e.machinePath(claudeManagedDirDarwin)
	if e.windows() {
		dir = e.machinePath(claudeManagedDirWindows)
	}
	return filepath.Join(append([]string{dir}, elem...)...)
}

// managedMCPPath returns the enterprise managed MCP configuration path.
func (e Env) managedMCPPath() string {
	return e.claudeManagedPath(claudeManagedMCPFile)
}

// claudeConfigPaths returns the .claude.json the calling Claude Code session
// reads and the configuration directory that holds its plugins. Both move with
// CLAUDE_CONFIG_DIR.
func (e Env) claudeConfigPaths() (claudeJSONPath, configDir string, ok bool) {
	dir := e.getenv(claudeConfigDirEnv)
	if dir == "" {
		return e.homePath(".claude.json"), e.homePath(".claude"), true
	}
	if !filepath.IsAbs(dir) {
		return "", "", false
	}
	return filepath.Join(dir, ".claude.json"), dir, true
}

// resolveClaudeCode resolves a Claude Code server name against its MCP
// configuration, then against the claude.ai account connectors.
//
// The order below is Claude Code's own (the merge in its MCP config loader):
// managed servers, then local (this project's projects{} entry), then every
// .mcp.json from the launch directory up, then user, then plugins. A later source
// never overrides an earlier one, so the first scope to declare a name is the one
// the session connected to.
func resolveClaudeCode(ctx context.Context, loader *configLoader, env Env, req ResolveRequest, serverName string, tr *tracer) Resolution {
	claudePath, configDir, ok := env.claudeConfigPaths()
	if !ok {
		return unresolved(serverName, fmt.Sprintf(
			"%s is set to a relative path (%q); set it to an absolute path",
			claudeConfigDirEnv, env.getenv(claudeConfigDirEnv)))
	}
	claude, claudeRes := loader.loadClaudeJSON(ctx, claudePath)

	// Most Claude Code keys survive namespacing untouched — hyphens and underscores
	// are legal in a tool namespace — so most lookups here match exactly. A key
	// holding anything else does not: "my.server" and "@acme/server" are namespaced
	// as "my_server" and "_acme_server", so an exact-only lookup would miss every
	// scope and deny a correctly configured server. formClaudeCode is what closes
	// that.
	names := lookup{names: []string{serverName}, form: formClaudeCode}

	// The managed sources come first and, while managed-mcp.json is present, are the
	// only ones: Claude Code then loads its servers and managedMcpServers and nothing
	// else. managed-mcp.json overrides managedMcpServers, so it ranks above them.
	m, out := resolveScopes(ctx, claudeManagedScopes(loader, env), names, tr)
	switch out {
	case outcomeFound:
		return resolved(env, m.key, m.entry)
	case outcomeAmbiguous:
		return ambiguous(req.Agent, serverName)
	}
	// A managed-mcp.json that cannot be read still locks: Claude Code keeps it in
	// exclusive control rather than letting user configuration through, so
	// resolving against user configuration here would name a server the session
	// could not have run.
	if _, res := loader.readConfig(ctx, env.managedMCPPath()); res != loadAbsent {
		return notFound(req.Agent, serverName, fmt.Sprintf(
			"MCP server %q is not in Claude Code's managed MCP configuration, which cannot be overridden", serverName))
	}

	project := claudeProjectFor(ctx, loader, env, req.CWD, req.TranscriptPath)
	scopes := claudeProjectScopes(loader, env, project, claudePath, claude, claudeRes, 0)
	scopes = append(scopes, scope{
		path: claudePath,
		key:  mcpServersKey,
		rank: len(scopes),
		load: fixedServers(decodeServers(claude.MCPServers), claudeRes),
	})
	if strings.HasPrefix(serverName, claudePluginNamespacePrefix) {
		scopes = append(scopes, claudePluginScopes(ctx, loader, env, configDir, project, len(scopes))...)
	}

	m, out = resolveScopes(ctx, scopes, names, tr)
	switch out {
	case outcomeFound:
		return resolved(env, m.key, m.entry)
	case outcomeAmbiguous:
		return ambiguous(req.Agent, serverName)
	}

	if connector, ok := resolveClaudeAIConnector(claude, claudeRes, claudePath, serverName, tr); ok {
		// The display name, not the tool-name hint that found it: the hint is the
		// namespace form (claude_ai_Linear for a connector listed as "claude.ai
		// Linear"), which appears in no allowlist entry an administrator could write.
		res := Resolution{ServerName: connector}
		res.Identity.Connector = connector
		return res
	}

	return notFound(req.Agent, serverName, fmt.Sprintf(
		"MCP server %q was not found in any Claude Code MCP configuration", serverName))
}

// claudeManagedScopes returns managed-mcp.json, then the managed settings files
// that can carry managedMcpServers.
//
// managed-settings.json and its drop-ins are peers: the resolver does not
// reproduce how Claude Code layers them, so two that define one server
// differently are ambiguous rather than settled by a guessed order. MDM profiles,
// the Windows registry, and server-delivered settings can also carry
// managedMcpServers and are not read; a server delivered only through one of them
// is unresolved, never misidentified.
func claudeManagedScopes(loader *configLoader, env Env) []scope {
	mcpPath := env.managedMCPPath()
	scopes := []scope{{path: mcpPath, key: mcpServersKey, load: jsonServers(loader, mcpPath)}}
	for _, path := range claudeManagedSettingsFiles(env) {
		scopes = append(scopes, scope{
			path: path,
			key:  claudeManagedServersKey,
			rank: 1,
			load: managedSettingsServers(loader, path),
		})
	}
	return scopes
}

// claudeManagedSettingsFiles lists managed-settings.json and the .json drop-ins
// beside it, in name order.
func claudeManagedSettingsFiles(env Env) []string {
	files := []string{env.claudeManagedPath(claudeManagedSettings)}
	dir := env.claudeManagedPath(claudeManagedDropInDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return files
	}
	for _, entry := range entries {
		if len(files) > maxClaudeManagedDropIns {
			break
		}
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".json") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	return files
}

// managedSettingsServers reads managedMcpServers from a managed settings file.
func managedSettingsServers(loader *configLoader, path string) func(context.Context) (serverSet, loadResult) {
	return func(ctx context.Context) (serverSet, loadResult) {
		var doc struct {
			Servers map[string]json.RawMessage `json:"managedMcpServers"`
		}
		res := loader.loadJSON(ctx, path, &doc)
		if res != loadOK {
			return nil, res
		}
		return decodeServers(doc.Servers), loadOK
	}
}

// claudeProject is the project context a Claude Code session loads its
// project-scoped configuration from.
type claudeProject struct {
	// launchDir is the directory the session was started in. Claude Code reads
	// .mcp.json from it and from every directory above it.
	launchDir string
	// root keys the session's projects{} entry: the launch directory's git root —
	// the main checkout's, for a linked worktree — or, outside a repository, the
	// launch directory itself.
	root string
}

// claudeProjectFor establishes the project context for a call made from cwd.
//
// The hook's cwd is where the session is now, which is not necessarily where it
// started: a session that cd's into a subdirectory or a nested repository keeps
// the configuration it loaded at launch. Claude Code names the transcript
// directory after the launch directory, so the transcript path recovers it; when
// it does not, cwd stands in.
func claudeProjectFor(ctx context.Context, loader *configLoader, env Env, cwd, transcriptPath string) claudeProject {
	dirs := ancestors(cwd)
	if len(dirs) == 0 {
		return claudeProject{}
	}
	launchDir := dirs[0]
	if dir, ok := claudeLaunchDir(env, dirs, transcriptPath); ok {
		launchDir = dir
	}
	return claudeProject{launchDir: launchDir, root: claudeProjectRoot(ctx, loader, launchDir)}
}

// claudeLaunchDir finds which of dirs, an absolute cwd and its ancestors, names
// the transcript directory the session writes to (projects/<slug>/<id>.jsonl in
// Claude Code's configuration directory).
func claudeLaunchDir(env Env, dirs []string, transcriptPath string) (string, bool) {
	if !filepath.IsAbs(transcriptPath) {
		return "", false
	}
	// The slug is the component after "projects". Matching on that rather than on
	// the parent directory alone also covers a subagent transcript, which lives
	// below its session's directory.
	parts := strings.FieldsFunc(transcriptPath, func(r rune) bool { return r == '/' || r == '\\' })
	var slugs []string
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "projects" {
			slugs = append(slugs, parts[i+1])
		}
	}
	for _, dir := range dirs {
		slug := claudeProjectSlug(dir)
		if slices.ContainsFunc(slugs, func(s string) bool {
			if env.windows() {
				return strings.EqualFold(s, slug)
			}
			return s == slug
		}) {
			return dir, true
		}
	}
	return "", false
}

// claudeSlugMax is the longest slug Claude Code writes before truncating.
const claudeSlugMax = 200

// claudeProjectSlug is the directory name Claude Code gives a project's
// transcripts: every UTF-16 code unit outside [a-zA-Z0-9] becomes "-", and a
// result over 200 characters is cut there and suffixed with a hash of the path.
func claudeProjectSlug(path string) string {
	units := utf16.Encode([]rune(path))
	slug := make([]byte, len(units))
	for i, u := range units {
		switch {
		case u >= 'a' && u <= 'z', u >= 'A' && u <= 'Z', u >= '0' && u <= '9':
			slug[i] = byte(u)
		default:
			slug[i] = '-'
		}
	}
	if len(slug) <= claudeSlugMax {
		return string(slug)
	}
	// JavaScript's 32-bit string hash, Math.abs'd and written in base 36. The abs
	// is taken in 64 bits because Math.abs(-2^31) is 2^31, not an overflow.
	var h int32
	for _, u := range units {
		h = 31*h + int32(u)
	}
	abs := int64(h)
	if abs < 0 {
		abs = -abs
	}
	return string(slug[:claudeSlugMax]) + "-" + strconv.FormatInt(abs, 36)
}

// claudeProjectRoot returns the directory that keys a session's projects{} entry.
//
// Claude Code walks up from the launch directory to the nearest .git. A .git
// directory makes that directory the root. A .git file is a linked worktree or a
// submodule: a worktree, whose git directory names a commondir, is keyed by the
// main checkout so all of a repository's worktrees share one entry; a submodule
// keeps its own. A symlinked .git is followed, as Claude Code follows it: a
// checkout made by a tool like repo is still its own root.
func claudeProjectRoot(ctx context.Context, loader *configLoader, launchDir string) string {
	for _, dir := range ancestors(launchDir) {
		dotGit := filepath.Join(dir, ".git")
		info, err := os.Stat(dotGit)
		if err != nil {
			continue
		}
		if info.IsDir() {
			return dir
		}
		if main, ok := gitWorktreeMain(ctx, loader, dir, dotGit); ok {
			return main
		}
		return dir
	}
	return launchDir
}

// gitWorktreeMain returns the main checkout of the linked worktree at dir, whose
// .git file is dotGit.
func gitWorktreeMain(ctx context.Context, loader *configLoader, dir, dotGit string) (string, bool) {
	data, res := loader.readConfig(ctx, dotGit)
	if res != loadOK {
		return "", false
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(firstLine(data))), "gitdir:")
	if !ok {
		return "", false
	}
	gitDir = absFrom(dir, strings.TrimSpace(gitDir))
	data, res = loader.readConfig(ctx, filepath.Join(gitDir, "commondir"))
	if res != loadOK {
		return "", false
	}
	common := absFrom(gitDir, strings.TrimSpace(string(firstLine(data))))
	if filepath.Base(common) != ".git" {
		// A bare repository's worktree has no main checkout to key by.
		return "", false
	}
	return filepath.Dir(common), true
}

func firstLine(data []byte) []byte {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	return line
}

// absFrom resolves path against base when it is relative.
func absFrom(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(base, path)
}

// maxProjectDepth bounds the ancestor walk.
const maxProjectDepth = 40

// claudeProjectScopes returns the project-scoped Claude Code sources, highest
// precedence first, with consecutive ranks starting at rank: the projects{} entry
// for the project root, then each .mcp.json from the launch directory up.
//
// Claude Code reads .mcp.json from the launch directory and every directory above
// it short of the filesystem root, merging them so a deeper file overrides a
// shallower one. Every one of them is live, so every one is a scope. Only one
// projects{} entry is: Claude Code looks up exactly one key, and an entry for a
// parent directory or a sibling project governs nothing here.
func claudeProjectScopes(loader *configLoader, env Env, project claudeProject, claudePath string, claude claudeJSON, claudeRes loadResult, rank int) []scope {
	if project.launchDir == "" {
		return nil
	}
	scopes := make([]scope, 0, 4)
	if key, ok := claudeProjectKey(env, claude, project.root); ok {
		scopes = append(scopes, scope{
			path: claudePath,
			key:  fmt.Sprintf("projects[%q].%s", key, mcpServersKey),
			rank: rank,
			load: fixedServers(decodeServers(claude.Projects[key].MCPServers), claudeRes),
		})
		rank++
	}

	files := projectFiles(project.launchDir)
	if len(files) == 0 && filepath.Dir(project.launchDir) != project.launchDir {
		// Name the file an operator expects to be read, so the trace says it is
		// absent rather than leaving it out. Not at the filesystem root, whose
		// .mcp.json Claude Code never reads: there the placeholder would load it.
		files = []string{filepath.Join(project.launchDir, ".mcp.json")}
	}
	for _, path := range files {
		scopes = append(scopes, scope{
			path: path,
			key:  mcpServersKey,
			rank: rank,
			load: jsonServers(loader, path),
		})
		rank++
	}
	return scopes
}

// claudeProjectKey returns the projects{} key naming dir. Two keys can name one
// directory through spelling alone; taking the lowest keeps resolution stable
// across runs, since map order is not.
func claudeProjectKey(env Env, claude claudeJSON, dir string) (string, bool) {
	want := env.comparableDir(dir)
	if want == "" {
		return "", false
	}
	var found string
	for key := range claude.Projects {
		if env.comparableDir(key) != want {
			continue
		}
		if found == "" || key < found {
			found = key
		}
	}
	return found, found != ""
}

// projectFiles returns each .mcp.json from dir up to, but not including, the
// filesystem root, nearest first. A .mcp.json that is a directory is not a file
// and is skipped.
func projectFiles(dir string) []string {
	var files []string
	for _, d := range ancestors(dir) {
		if filepath.Dir(d) == d {
			break
		}
		path := filepath.Join(d, ".mcp.json")
		if info, err := os.Lstat(path); err == nil && !info.IsDir() {
			files = append(files, path)
		}
	}
	return files
}

// ancestors returns an absolute cwd and each directory above it, nearest first.
// A relative cwd cannot identify a stable configuration scope and is ignored.
func ancestors(cwd string) []string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" || !filepath.IsAbs(cwd) {
		return nil
	}
	dir := filepath.Clean(cwd)
	dirs := make([]string, 0, 8)
	for range maxProjectDepth {
		dirs = append(dirs, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return dirs
}

// comparableDir is the form of a directory path used to decide whether two paths
// name the same directory.
func (e Env) comparableDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if e.windows() {
		// Claude Code writes projects{} keys with forward slashes on Windows.
		return strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	}
	return path
}

func resolveClaudeAIConnector(claude claudeJSON, claudeRes loadResult, claudePath, serverName string, tr *tracer) (string, bool) {
	if !strings.HasPrefix(serverName, claudeAIConnectorPrefix) {
		return "", false
	}
	if claudeRes != loadOK || len(claude.ClaudeAIMCPEverConnected) == 0 {
		tr.miss(claudePath, "claudeAiMcpEverConnected", claudeRes)
		return "", false
	}

	connected := make(map[string]struct{}, len(claude.ClaudeAIMCPEverConnected))
	for _, name := range claude.ClaudeAIMCPEverConnected {
		connected[name] = struct{}{}
	}

	// The stored entries are display names ("claude.ai MyServer"), which are never the
	// namespace form the call reports, so this always resolves through the form
	// rather than exactly.
	//
	// That is also what handles a duplicate display name, with no suffix rule of its
	// own. Claude Code disambiguates by renaming the second connector to
	// "claude.ai MyServer (2)", and formClaudeCode of that is "claude_ai_MyServer_2" —
	// so the reported name matches the stored entry outright. Stripping a trailing
	// "_<n>" off the reported name and looking for "claude.ai MyServer" instead, which
	// is the obvious-looking thing to do, finds the FIRST connector: the wrong one.
	names := lookup{names: []string{serverName}, form: formClaudeCode}
	if _, connector, ok := matchName(names, connected); ok {
		tr.hit(claudePath, "claudeAiMcpEverConnected", "connector "+connector)
		return connector, true
	}
	tr.miss(claudePath, "claudeAiMcpEverConnected", claudeRes)
	return "", false
}
