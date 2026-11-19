package enforce

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
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
	// claudeMCPConfigMaxBytes is Claude Code's own cap on .mcp.json and
	// managed-mcp.json: it skips a larger file.
	claudeMCPConfigMaxBytes = 2 << 20
	// claudeJSONMaxBytes bounds the .claude.json read. Claude Code reads that file
	// whole, and it grows with use, so the general limit would skip files the
	// session loaded.
	claudeJSONMaxBytes = 64 << 20
	// maxClaudeManagedDropIns bounds the managed-settings.d files read per call.
	// Claude Code itself reads every drop-in, so past this many the managed set is
	// treated as unreadable rather than truncated.
	maxClaudeManagedDropIns = 512
	// claudeConfigDirEnv relocates Claude Code's user configuration, .claude.json
	// included, away from the home directory.
	claudeConfigDirEnv = "CLAUDE_CONFIG_DIR"
)

// claudeJSON is the part of .claude.json the resolver reads. It is decoded by
// exact key (see UnmarshalJSON), as Claude Code reads it.
type claudeJSON struct {
	MCPServers map[string]json.RawMessage
	Projects   map[string]claudeProjectConfig
}

// claudeProjectConfig is the part of a .claude.json projects{} entry the
// resolver reads.
type claudeProjectConfig struct {
	MCPServers map[string]json.RawMessage
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
	res := l.loadStrictJSON(ctx, path, &doc, claudeJSONMaxBytes)
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

// Claude Code's mcp_server.source values for servers whose definition the
// resolver can read. Any other source (dynamic for --mcp-config, sdk, agent, or
// one added later) names a definition the hook cannot see.
const (
	claudeSourceUser       = "user"
	claudeSourceLocal      = "local"
	claudeSourceProject    = "project"
	claudeSourcePlugin     = "plugin"
	claudeSourceEnterprise = "enterprise"
	claudeSourceManaged    = "managed"
	claudeSourceClaudeAI   = "claudeai"
)

// claudeUnreadableSources explains the sources whose definitions the hook cannot
// read.
var claudeUnreadableSources = map[string]string{
	"dynamic": "--mcp-config or the SDK's server list",
	"sdk":     "the SDK host that registered it",
	"agent":   "an agent definition",
}

// resolveClaudeCode resolves a Claude Code server from the payload's mcp_server
// field: the server's configuration key and the scope Claude Code loaded it from.
// Only that scope is read, and the key is matched exactly. Precedence between
// scopes is Claude Code's to apply, and it already has: the source it reports is
// the one whose definition the session runs.
func resolveClaudeCode(ctx context.Context, loader *configLoader, env Env, req ResolveRequest, serverName string, tr *tracer) Resolution {
	source := req.ServerSource
	if source == "" {
		return unresolved(serverName,
			"Claude Code did not report which configuration the MCP server came from (the hook payload has no mcp_server); update Claude Code")
	}
	if isBuiltinAgentMCP(req.Agent, serverName) && claudeBuiltinSource(source) {
		return Resolution{ServerName: serverName}
	}
	claudePath, configDir, ok := env.claudeConfigPaths()
	if !ok {
		return unresolved(serverName, fmt.Sprintf(
			"%s is set to a relative path (%q); set it to an absolute path",
			claudeConfigDirEnv, env.getenv(claudeConfigDirEnv)))
	}
	names := lookup{names: []string{serverName}}

	switch source {
	case claudeSourceClaudeAI:
		// A claude.ai account connector is configured in the account, not on this
		// machine; its display name is the whole of its identity here.
		tr.hit("hook payload", "mcp_server", "claude.ai connector")
		res := Resolution{ServerName: serverName}
		res.Identity.Connector = serverName
		return res

	case claudeSourceEnterprise:
		path := env.managedMCPPath()
		return claudeResolveIn(ctx, env, req, serverName, source, names, tr,
			[]scope{{path: path, key: mcpServersKey, load: claudeJSONServers(loader, path)}})

	case claudeSourceManaged:
		settings, complete := claudeManagedSettingsScopes(loader, env, tr)
		if !complete {
			return unresolved(serverName, fmt.Sprintf(
				"Claude Code's managed settings directory %s could not be read in full",
				env.claudeManagedPath(claudeManagedDropInDir)))
		}
		return claudeResolveIn(ctx, env, req, serverName, source, names, tr, settings)

	case claudeSourceUser, claudeSourceLocal, claudeSourceProject, claudeSourcePlugin:
	default:
		where, known := claudeUnreadableSources[source]
		if !known {
			where = fmt.Sprintf("a source the hook does not recognize (%q)", source)
		}
		return unresolved(serverName, fmt.Sprintf(
			"Claude Code loaded MCP server %q from %s, whose configuration the hook cannot read", serverName, where))
	}

	// Only the user and local scopes live in .claude.json, which can run to tens
	// of megabytes, so it is not parsed for the others.
	var (
		claude    claudeJSON
		claudeRes loadResult
	)
	if source == claudeSourceUser || source == claudeSourceLocal {
		if claude, claudeRes = loader.loadClaudeJSON(ctx, claudePath); claudeRes != loadOK {
			return unresolved(serverName, fmt.Sprintf(
				"Claude Code's %s, which holds %s MCP servers, could not be read", claudePath, source))
		}
	}
	if source == claudeSourceUser {
		return claudeResolveIn(ctx, env, req, serverName, source, names, tr, []scope{{
			path: claudePath,
			key:  mcpServersKey,
			load: fixedServers(claudeServers(claude.MCPServers), claudeRes),
		}})
	}

	// The local, project, and plugin scopes depend on the directory the session
	// launched in. Each directory it may have launched in is resolved, and those
	// whose configuration defines the server must agree on it. One that does not
	// define it cannot be where the session loaded it.
	projects := claudeProjectsFor(ctx, loader, env, configDir, req.CWD, req.TranscriptPath)
	var (
		m          match
		found      bool
		incomplete bool
	)
	for _, project := range projects {
		var scopes []scope
		switch source {
		case claudeSourceLocal:
			scopes = claudeLocalScopes(env, project, claudePath, claude, claudeRes)
		case claudeSourceProject:
			scopes = claudeProjectFileScopes(loader, project)
		case claudeSourcePlugin:
			scopes = claudePluginScopes(ctx, loader, env, configDir, project, 0, &incomplete)
		}
		pm, out := resolveScopes(ctx, scopes, names, tr)
		if incomplete {
			return unresolved(serverName, fmt.Sprintf(
				"Claude Code's plugins name more installations or server files than the hook reads, so the definition of MCP server %q cannot be settled", serverName))
		}
		switch {
		case out == outcomeAmbiguous:
			return ambiguous(req.Agent, serverName)
		case out != outcomeFound:
		case !found:
			m, found = pm, true
		case !sameEntry(pm.entry, m.entry):
			return unresolved(serverName, fmt.Sprintf(
				"the session started in %s or a directory between it and %s, and Claude Code's %s MCP configuration defines %q differently depending on which, so the hook cannot tell which one ran; rename one of them",
				projects[len(projects)-1].launchDir, projects[0].launchDir, source, serverName))
		}
	}
	if !found {
		return claudeNotFound(serverName, source)
	}
	return resolved(env, m.key, m.entry)
}

// claudeBuiltinSource reports whether source is one Claude Code's built-in
// servers report. computer-use and claude-in-chrome are registered as stdio
// servers in the dynamic scope, and the desktop host supplies Claude Preview
// and Claude Browser as SDK servers. Claude Code refuses a built-in name from a
// configuration file or --mcp-config, so under these sources the name is the
// built-in. Under any other source it is not, and is resolved as reported.
func claudeBuiltinSource(source string) bool {
	return source == "dynamic" || source == "sdk"
}

// claudeResolveIn resolves the server in one source's scopes.
func claudeResolveIn(ctx context.Context, env Env, req ResolveRequest, serverName, source string, names lookup, tr *tracer, scopes []scope) Resolution {
	m, out := resolveScopes(ctx, scopes, names, tr)
	switch out {
	case outcomeFound:
		return resolved(env, m.key, m.entry)
	case outcomeAmbiguous:
		return ambiguous(req.Agent, serverName)
	}
	return claudeNotFound(serverName, source)
}

// claudeNotFound reports a server Claude Code loaded from source that the
// resolver did not find there: a definition that changed since the session
// loaded it, or one it reads from a place the resolver does not.
func claudeNotFound(serverName, source string) Resolution {
	return unresolved(serverName, fmt.Sprintf(
		"Claude Code loaded MCP server %q from its %s configuration, but the hook did not find it there", serverName, source))
}

// claudeManagedSettingsScopes returns the managed settings files that can carry
// managedMcpServers, highest precedence first. Claude Code applies
// managed-settings.json, then each drop-in in managed-settings.d in byte order of
// file name, a later file overriding an earlier one; so the last drop-in ranks
// first and managed-settings.json last.
//
// MDM profiles, the Windows registry, and server-delivered settings can also
// carry managedMcpServers and are not read; a server delivered only through one
// of them is unresolved, never misidentified.
//
// complete is false when the drop-ins could not all be read.
func claudeManagedSettingsScopes(loader *configLoader, env Env, tr *tracer) (scopes []scope, complete bool) {
	files, complete := claudeManagedSettingsFiles(env)
	if !complete {
		tr.miss(env.claudeManagedPath(claudeManagedDropInDir), "", loadUnusable)
		return nil, false
	}
	for i, path := range slices.Backward(files) {
		scopes = append(scopes, scope{
			path: path,
			key:  claudeManagedServersKey,
			rank: len(files) - 1 - i,
			load: managedSettingsServers(loader, path),
		})
	}
	return scopes, true
}

// claudeManagedSettingsFiles lists managed-settings.json and its drop-ins in the
// order Claude Code applies them. A drop-in is a file directly in
// managed-settings.d whose name ends in ".json" (case-sensitively) and does not
// start with a dot.
//
// A drop-in directory that cannot be listed for lack of permission, or that is
// not a directory at all, is treated as absent: the hook runs as the same user as
// Claude Code, which skips it silently in both cases, so nothing in it can have
// run. Any other listing failure, or more drop-ins than the resolver reads,
// leaves the set incomplete.
func claudeManagedSettingsFiles(env Env) (files []string, complete bool) {
	files = []string{env.claudeManagedPath(claudeManagedSettings)}
	dir := env.claudeManagedPath(claudeManagedDropInDir)
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		return files, true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return files, errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		if len(files) > maxClaudeManagedDropIns {
			return files, false
		}
		files = append(files, filepath.Join(dir, name))
	}
	return files, true
}

// managedSettingsServers reads managedMcpServers from a managed settings file,
// keeping only the entries Claude Code accepts there. An entry it ignores
// overrides nothing: Claude Code validates each file before layering it, so an
// earlier file's definition still answers for that name.
func managedSettingsServers(loader *configLoader, path string) func(context.Context) (serverSet, loadResult) {
	return func(ctx context.Context) (serverSet, loadResult) {
		var raw json.RawMessage
		if res := loader.loadStrictJSON(ctx, path, &raw, maxConfigBytes); res != loadOK {
			return nil, res
		}
		members, ok := exactObject(raw)
		if !ok {
			return nil, loadUnusable
		}
		table, _ := objectMember(members, claudeManagedServersKey)
		accepted := make(map[string]json.RawMessage, len(table))
		for name, raw := range table {
			if managedSettingsServerAccepted(raw) {
				accepted[name] = raw
			}
		}
		return claudeServers(accepted), loadOK
	}
}

// managedSettingsServerAccepted reports whether Claude Code would load a
// managedMcpServers entry: managed settings deliver only "http" or "sse" servers
// at an https URL, and never a command. managed-mcp.json is not restricted this
// way.
func managedSettingsServerAccepted(raw json.RawMessage) bool {
	members, ok := exactObject(raw)
	if !ok {
		return false
	}
	if _, present := members["command"]; present {
		return false
	}
	typ, _, _ := exactString(members, "type")
	if typ != "http" && typ != "sse" {
		return false
	}
	rawURL, _, _ := exactString(members, "url")
	u, err := url.Parse(rawURL)
	return err == nil && strings.EqualFold(u.Scheme, "https") && u.Host != ""
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

// claudeProjectsFor establishes the project contexts a call made from cwd may
// have been loaded in, cwd's first.
//
// The hook's cwd is where the session is now, which is not necessarily where it
// started: a session that cd's into a subdirectory or a nested repository keeps
// the configuration it loaded at launch. Claude Code names the transcript
// directory after the launch directory, so the transcript path recovers it; when
// it does not, cwd stands in.
//
// A resumed session breaks that. It loads its configuration from the directory
// it was resumed in, but goes on writing the transcript it started, named after
// the directory the session first launched in. When that names a directory above
// cwd, the payload looks the same for a session that launched there and cd'd
// down as for one resumed from anywhere between there and cwd, so every one of
// those directories is a candidate.
func claudeProjectsFor(ctx context.Context, loader *configLoader, env Env, configDir, cwd, transcriptPath string) []claudeProject {
	dirs := ancestors(cwd)
	if len(dirs) == 0 {
		return []claudeProject{{}}
	}
	candidates := dirs[:1]
	if n, ok := claudeLaunchDir(env, dirs, configDir, transcriptPath); ok {
		candidates = dirs[:n+1]
	}
	projects := make([]claudeProject, len(candidates))
	for i, dir := range candidates {
		projects[i] = claudeProject{launchDir: dir, root: claudeProjectRoot(ctx, loader, dir)}
	}
	return projects
}

// claudeLaunchDir finds which of dirs, an absolute cwd and its ancestors, names
// the transcript directory the session writes to (projects/<slug>/<id>.jsonl in
// Claude Code's configuration directory), and returns its index.
func claudeLaunchDir(env Env, dirs []string, configDir, transcriptPath string) (int, bool) {
	slug, ok := claudeTranscriptSlug(env, configDir, transcriptPath)
	if !ok {
		return 0, false
	}
	for i, dir := range dirs {
		want := claudeProjectSlug(dir)
		if slug == want || env.windows() && strings.EqualFold(slug, want) {
			return i, true
		}
	}
	return 0, false
}

// claudeTranscriptSlug returns the project slug of a transcript the session wrote
// to configDir: the directory directly under configDir/projects, with the
// transcript at or below it, which also covers a subagent transcript nested under
// its session. A "projects" component anywhere else in the path is not the
// session's; taking one would let a crafted configuration directory name any
// launch directory.
func claudeTranscriptSlug(env Env, configDir, transcriptPath string) (string, bool) {
	if !filepath.IsAbs(transcriptPath) || !filepath.IsAbs(configDir) {
		return "", false
	}
	projects := filepath.Join(configDir, "projects") + string(filepath.Separator)
	path := filepath.Clean(transcriptPath)
	if len(path) <= len(projects) {
		return "", false
	}
	prefix := path[:len(projects)]
	if prefix != projects && (!env.windows() || !strings.EqualFold(prefix, projects)) {
		return "", false
	}
	slug, rest, ok := strings.Cut(path[len(projects):], string(filepath.Separator))
	return slug, ok && slug != "" && rest != ""
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

// gitWorktreeMain returns the directory that keys the linked worktree at dir,
// whose .git file is dotGit: the main checkout, or for a bare repository the
// repository itself.
//
// It ports Claude Code's validation, because a .git file is just a file: one
// shipped in an archive can name any repository's commondir. Claude Code
// redirects only when the git directory sits in <common>/worktrees and its gitdir
// backlink resolves to dir/.git; anything else keys dir by itself, and so must
// the resolver, or it would report another project's local servers for a
// session that never loaded them.
func gitWorktreeMain(ctx context.Context, loader *configLoader, dir, dotGit string) (string, bool) {
	gitDir, ok := readGitPointer(ctx, loader, dotGit, "gitdir:")
	if !ok {
		return "", false
	}
	gitDir = absFrom(dir, gitDir)
	common, ok := readGitPointer(ctx, loader, filepath.Join(gitDir, "commondir"), "")
	if !ok {
		return "", false
	}
	common = absFrom(gitDir, common)
	if filepath.Dir(gitDir) != filepath.Join(common, "worktrees") {
		return "", false
	}
	backlink, ok := readGitPointer(ctx, loader, filepath.Join(gitDir, "gitdir"), "")
	if !ok || !sameRealPath(absFrom(gitDir, backlink), filepath.Join(dir, ".git")) {
		return "", false
	}
	if filepath.Base(common) != ".git" {
		// A bare repository keys its worktrees by the repository directory, unless
		// that directory is itself a checkout.
		if _, err := os.Stat(filepath.Join(common, ".git")); err == nil {
			return "", false
		}
		return common, true
	}
	return filepath.Dir(common), true
}

// readGitPointer reads the one path a git pointer file holds, after prefix.
func readGitPointer(ctx context.Context, loader *configLoader, path, prefix string) (string, bool) {
	data, res := loader.readConfig(ctx, path)
	if res != loadOK {
		return "", false
	}
	value, ok := strings.CutPrefix(strings.TrimSpace(string(firstLine(data))), prefix)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

// sameRealPath reports whether a and b name one file once symlinks are resolved.
// b is resolved through its directory, since a worktree's .git is a file that
// may not resolve on its own the way a directory does.
func sameRealPath(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(filepath.Dir(b))
	if err != nil {
		return false
	}
	return ra == filepath.Join(rb, filepath.Base(b))
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

// claudeLocalScopes returns the session's projects{} entry in .claude.json.
// Claude Code looks up exactly one key, the project root, so an entry for a
// parent directory or a sibling project governs nothing here.
func claudeLocalScopes(env Env, project claudeProject, claudePath string, claude claudeJSON, claudeRes loadResult) []scope {
	key, ok := claudeProjectKey(env, claude, project.root)
	if !ok {
		return []scope{{path: claudePath, key: "projects", load: fixedServers(nil, claudeRes)}}
	}
	return []scope{{
		path: claudePath,
		key:  fmt.Sprintf("projects[%q].%s", key, mcpServersKey),
		load: fixedServers(claudeServers(claude.Projects[key].MCPServers), claudeRes),
	}}
}

// claudeProjectFileScopes returns each .mcp.json from the launch directory up,
// highest precedence first.
//
// Claude Code reads .mcp.json from the launch directory and every directory above
// it short of the filesystem root, merging them so a deeper file overrides a
// shallower one. Every one of them is live, so every one is a scope.
func claudeProjectFileScopes(loader *configLoader, project claudeProject) []scope {
	if project.launchDir == "" {
		return nil
	}
	files := projectFiles(project.launchDir)
	if len(files) == 0 && filepath.Dir(project.launchDir) != project.launchDir {
		// Name the file an operator expects to be read, so the trace says it is
		// absent rather than leaving it out. Not at the filesystem root, whose
		// .mcp.json Claude Code never reads: there the placeholder would load it.
		files = []string{filepath.Join(project.launchDir, ".mcp.json")}
	}
	scopes := make([]scope, len(files))
	for i, path := range files {
		scopes[i] = scope{path: path, key: mcpServersKey, rank: i, load: claudeJSONServers(loader, path)}
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
