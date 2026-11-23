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
	"strings"
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

// claudeConfigDir returns the configuration directory that holds the calling
// Claude Code session's plugins: CLAUDE_CONFIG_DIR, or ~/.claude without it. ok
// is false for a relative value, which Claude Code resolves against the
// directory it was started from.
//
// A variable that is set to nothing is relative too, not unset: Claude Code
// then keeps its plugins, transcripts and credentials in the directory it was
// started from. Its settings can also set the variable to nothing after it has
// read .claude.json from the directory it was started with. (Both verified
// against Claude Code 2.1.323.)
//
// .claude.json is not looked for there; see claudeConfigDirUnsettled.
func (e Env) claudeConfigDir() (dir string, ok bool) {
	dir, set := e.lookupEnv(claudeConfigDirEnv)
	if !set {
		return e.homePath(".claude"), true
	}
	return dir, filepath.IsAbs(dir)
}

// claudeConfigDirUnsettled is the reason a user or local server is left
// unresolved while the hook's environment sets CLAUDE_CONFIG_DIR.
//
// Claude Code reads .claude.json, which holds both scopes, from the directory
// the variable named when it was started. Its settings can then change the
// variable: an env block in a --settings file or in the user settings file is
// applied after .claude.json has been read, and the hooks inherit the new
// value. Plugins, transcripts and credentials follow the new value;
// .claude.json does not. A session started with the variable and one whose
// settings set it afterwards give the hook the same environment and the same
// payload, transcript_path included, so the hook cannot tell which .claude.json
// the session read, and a decoy in the directory it is shown would vouch for a
// server that is not the one running. (Verified against Claude Code 2.1.323.)
func claudeConfigDirUnsettled(env Env, serverName, source string) string {
	return fmt.Sprintf(
		"%[1]s is set (%[2]q), and Claude Code's settings can set it after Claude Code has read its %[3]s MCP servers from another directory, so which .claude.json defines MCP server %[4]q cannot be determined; start Claude Code without %[1]s, or provide the server from a project, plugin or managed configuration",
		claudeConfigDirEnv, env.getenv(claudeConfigDirEnv), source, serverName)
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
	if builtin, ok := claudeBuiltinSources[serverName]; ok && source == builtin {
		return Resolution{ServerName: serverName}
	}
	configDir, ok := env.claudeConfigDir()
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

	case claudeSourcePlugin:
		if !claudeSideloadDisabled(ctx, loader, env) {
			return unresolved(serverName, fmt.Sprintf(
				"Claude Code's managed settings do not set %s, so plugin MCP server %q may come from a plugin loaded for this session only rather than an installed one",
				ClaudeDisableSideloadKey, serverName))
		}
	case claudeSourceUser, claudeSourceLocal, claudeSourceProject:
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
		claudePath = env.homePath(".claude.json")
		claude     claudeJSON
		claudeRes  loadResult
	)
	if source == claudeSourceUser || source == claudeSourceLocal {
		if _, set := env.lookupEnv(claudeConfigDirEnv); set {
			return unresolved(serverName, claudeConfigDirUnsettled(env, serverName, source))
		}
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

	var (
		scopes     []scope
		incomplete bool
	)
	if source == claudeSourcePlugin {
		scopes = claudePluginScopes(ctx, loader, configDir, serverName, 0, &incomplete)
	} else {
		// The local and project scopes depend on the directory the session was
		// started or resumed in.
		project, ok := claudeProjectFor(ctx, loader, req.ProjectDir)
		if !ok {
			return unresolved(serverName, fmt.Sprintf(
				"Claude Code did not give the hook a usable %s, so the directory it loaded %s MCP servers for is not known",
				ClaudeProjectDirEnv, source))
		}
		if source == claudeSourceLocal {
			scopes = claudeLocalScopes(env, project, claudePath, claude, claudeRes)
		} else {
			scopes = claudeProjectFileScopes(loader, project)
		}
	}
	m, out := resolveScopes(ctx, scopes, names, tr)
	if incomplete {
		return unresolved(serverName, fmt.Sprintf(
			"Claude Code's plugins name more installations or server files than the hook reads, so the definition of MCP server %q cannot be settled", serverName))
	}
	switch out {
	case outcomeAmbiguous:
		return ambiguous(req.Agent, serverName)
	case outcomeUndecodable:
		return claudeUndecodable(serverName, m.key)
	case outcomeFound:
	default:
		return claudeNotFound(serverName, source)
	}
	return resolved(env, m.key, m.entry)
}

// claudeBuiltinSources maps each of Claude Code's built-in MCP servers, by its
// exact name, to the one source it reports. computer-use and claude-in-chrome are
// registered as stdio servers in the dynamic scope; the desktop host supplies
// Claude Preview, Claude Browser and workspace as SDK servers. Claude Code
// refuses these names from configuration files and --mcp-config, so under its
// own source a name is the built-in; from any other source it is resolved as
// reported. An Agent SDK host can still register an SDK server under one of the
// SDK built-ins' names, and cannot be told apart from the desktop host.
//
// The names are builtinAgentMCPServers' Claude Code entries, which must agree
// with obot.
var claudeBuiltinSources = map[string]string{
	"computer-use":     "dynamic",
	"claude-in-chrome": "dynamic",
	"Claude Preview":   "sdk",
	"Claude Browser":   "sdk",
	"workspace":        "sdk",
}

// claudeResolveIn resolves the server in one source's scopes.
func claudeResolveIn(ctx context.Context, env Env, req ResolveRequest, serverName, source string, names lookup, tr *tracer, scopes []scope) Resolution {
	m, out := resolveScopes(ctx, scopes, names, tr)
	switch out {
	case outcomeFound:
		return resolved(env, m.key, m.entry)
	case outcomeAmbiguous:
		return ambiguous(req.Agent, serverName)
	case outcomeUndecodable:
		return claudeUndecodable(serverName, m.key)
	}
	return claudeNotFound(serverName, source)
}

// claudeUndecodable reports a file Claude Code may have loaded the server from
// that the hook cannot decode, such as one nested deeper than encoding/json
// allows. Skipping it would let a file Claude Code did not use answer instead.
func claudeUndecodable(serverName, path string) Resolution {
	return unresolved(serverName, fmt.Sprintf(
		"Claude Code may have loaded MCP server %q from %s, which the hook cannot decode, so its definition cannot be settled",
		serverName, path))
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

// ClaudeDisableSideloadKey is the managed setting that makes Claude Code refuse
// plugins loaded for one session: --plugin-dir, --plugin-dir-no-mcp,
// --plugin-url, CLAUDE_CODE_PLUGIN_DIRS, and an Agent SDK host's initialize
// plugins (each verified against Claude Code 2.1.311). Such a plugin reports the
// same mcp_server as an installed one and replaces an installed plugin of the
// same name, so plugin servers are resolved only while the setting is on;
// hook-install turns it on with enforcement.
const ClaudeDisableSideloadKey = "disableSideloadFlags"

// claudeSideloadDisabled reports whether the managed settings files set
// disableSideloadFlags to true. They are applied in Claude Code's order, a
// later file overriding an earlier one; a set that cannot be read in full, or a
// file that cannot be parsed, leaves the setting unknown, and so not on.
//
// Claude Code honors the setting only from managed settings, where users cannot
// turn it off. MDM profiles, the Windows registry, and server-delivered settings
// are not read.
func claudeSideloadDisabled(ctx context.Context, loader *configLoader, env Env) bool {
	files, complete := claudeManagedSettingsFiles(env)
	if !complete {
		return false
	}
	disabled := false
	for _, path := range files {
		var raw json.RawMessage
		switch loader.loadStrictJSON(ctx, path, &raw, maxConfigBytes) {
		case loadAbsent:
			continue
		case loadOK:
		default:
			return false
		}
		members, ok := exactObject(raw)
		if !ok {
			return false
		}
		value, present := members[ClaudeDisableSideloadKey]
		if !present {
			continue
		}
		var on bool
		if json.Unmarshal(value, &on) != nil {
			return false
		}
		disabled = on
	}
	return disabled
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
			return nil, loadUndecodable
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
	// projectDir is the directory the session was started or resumed in.
	// Claude Code reads .mcp.json from it and from every directory above it.
	projectDir string
	// root keys the session's projects{} entry: the project directory's git root —
	// the main checkout's, for a linked worktree — or, outside a repository, the
	// project directory itself.
	root string
}

// ClaudeProjectDirEnv is the variable Claude Code sets for every hook to the
// directory the session was started or resumed in, the one it loaded
// project-scoped configuration for.
const ClaudeProjectDirEnv = "CLAUDE_PROJECT_DIR"

// claudeProjectFor establishes the project context of a session whose project
// directory is projectDir. ok is false when projectDir is not usable.
//
// The hook's cwd is where the session is now, which is not necessarily where its
// configuration came from: a session that cd's into a subdirectory, a nested
// repository or a directory added with --add-dir keeps the configuration it
// loaded at start, and a resumed session loads the configuration of the
// directory it was resumed in, which can be anywhere. Claude Code passes that
// directory to every hook as CLAUDE_PROJECT_DIR, set after any value in its own
// environment or settings, so neither can replace it.
func claudeProjectFor(ctx context.Context, loader *configLoader, projectDir string) (claudeProject, bool) {
	if projectDir == "" || len(projectDir) > maxWorkingDirBytes || !filepath.IsAbs(projectDir) {
		return claudeProject{}, false
	}
	dir := filepath.Clean(projectDir)
	return claudeProject{projectDir: dir, root: claudeProjectRoot(ctx, loader, dir)}, true
}

// claudeProjectRoot returns the directory that keys a session's projects{} entry.
//
// Claude Code walks up from the project directory to the nearest .git. A .git
// directory makes that directory the root. A .git file is a linked worktree or a
// submodule: a worktree, whose git directory names a commondir, is keyed by the
// main checkout so all of a repository's worktrees share one entry; a submodule
// keeps its own. A symlinked .git is followed, as Claude Code follows it: a
// checkout made by a tool like repo is still its own root.
func claudeProjectRoot(ctx context.Context, loader *configLoader, projectDir string) string {
	for _, dir := range ancestors(projectDir) {
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
	return projectDir
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

// claudeLocalScopes returns the session's projects{} entry in .claude.json.
// Claude Code looks up exactly one key, the project root, so an entry for a
// parent directory or a sibling project governs nothing here. Two keys can spell
// that root differently ("C:/proj" and "c:/proj", or a doubled separator), and
// which one Claude Code reads cannot be reproduced reliably, so every key naming
// the root is a peer scope: they must agree on a server, or it is ambiguous.
func claudeLocalScopes(env Env, project claudeProject, claudePath string, claude claudeJSON, claudeRes loadResult) []scope {
	keys := claudeProjectKeys(env, claude, project.root)
	if len(keys) == 0 {
		return []scope{{path: claudePath, key: "projects", load: fixedServers(nil, claudeRes)}}
	}
	scopes := make([]scope, len(keys))
	for i, key := range keys {
		scopes[i] = scope{
			path: claudePath,
			key:  fmt.Sprintf("projects[%q].%s", key, mcpServersKey),
			load: fixedServers(claudeServers(claude.Projects[key].MCPServers), claudeRes),
		}
	}
	return scopes
}

// claudeProjectFileScopes returns each .mcp.json from the project directory up,
// highest precedence first.
//
// Claude Code reads .mcp.json from the project directory and every directory above
// it short of the filesystem root, merging them so a deeper file overrides a
// shallower one. Every one of them is live, so every one is a scope.
func claudeProjectFileScopes(loader *configLoader, project claudeProject) []scope {
	if project.projectDir == "" {
		return nil
	}
	files := projectFiles(project.projectDir)
	if len(files) == 0 && filepath.Dir(project.projectDir) != project.projectDir {
		// Name the file an operator expects to be read, so the trace says it is
		// absent rather than leaving it out. Not at the filesystem root, whose
		// .mcp.json Claude Code never reads: there the placeholder would load it.
		files = []string{filepath.Join(project.projectDir, ".mcp.json")}
	}
	scopes := make([]scope, len(files))
	for i, path := range files {
		scopes[i] = scope{path: path, key: mcpServersKey, rank: i, load: claudeJSONServers(loader, path)}
	}
	return scopes
}

// claudeProjectKeys returns every projects{} key naming dir, sorted so the
// trace is stable across runs, since map order is not.
func claudeProjectKeys(env Env, claude claudeJSON, dir string) []string {
	want := env.comparableDir(dir)
	if want == "" {
		return nil
	}
	var keys []string
	for key := range claude.Projects {
		if env.comparableDir(key) == want {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
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
// Whitespace is part of a directory's name ("/work/app " is not "/work/app"), so
// it is not trimmed.
//
// The walk is not capped: Claude Code's walks for .mcp.json files and for the git
// root go all the way up, and stopping short would skip a file it loads or key
// the session by the wrong root. The path length cap on the project directory
// bounds it.
func ancestors(cwd string) []string {
	if cwd == "" || !filepath.IsAbs(cwd) {
		return nil
	}
	dir := filepath.Clean(cwd)
	dirs := make([]string, 0, 8)
	for {
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
// name the same directory. Like ancestors, it keeps whitespace.
func (e Env) comparableDir(path string) string {
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
