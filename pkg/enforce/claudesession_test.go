package enforce

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestClaudeCodeSessionPlugins covers plugins loaded for one session only, from
// --plugin-dir, --plugin-dir-no-mcp, --plugin-url, CLAUDE_CODE_PLUGIN_DIRS or an
// Agent SDK host. Their servers report the same name and source as an installed
// plugin's, and one named like an installed plugin replaces it, so a plugin
// server is unresolved unless the session is known to have none.
func TestClaudeCodeSessionPlugins(t *testing.T) {
	setup := func(t *testing.T) *fixture {
		f := newFixture(t, "darwin")
		install := f.mkdir(f.path("cache", "p"))
		f.write(filepath.Join(install, ".mcp.json"), `{"s":{"type":"http","url":"https://installed.example.com/mcp"}}`)
		f.pluginRegistry(f.homePath(".claude"), `{"version":2,"plugins":{"p@mkt":[{"scope":"user","installPath":`+quote(install)+`}]}}`)
		return f
	}

	t.Run("ordinary session", func(t *testing.T) {
		f := setup(t)
		assertURL(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "https://installed.example.com/mcp")

		// A prompt that merely mentions a flag is not the flag.
		f.procs.argv[fakeClaudePID] = []string{"claude", "-p", "explain --plugin-dir", "--input-format", "text"}
		assertURL(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "https://installed.example.com/mcp")
	})

	for name, args := range map[string][]string{
		"plugin dir":           {"claude", "--plugin-dir", "/tmp/mine"},
		"plugin dir with =":    {"claude", "--plugin-dir=/tmp/mine"},
		"plugin dir no mcp":    {"claude", "--plugin-dir-no-mcp", "/tmp/mine"},
		"plugin url":           {"claude", "--plugin-url", "https://example.com/p.zip"},
		"sdk host":             {"node", "cli.js", "--output-format", "stream-json", "--input-format", "stream-json"},
		"sdk host with =":      {"claude", "--input-format=stream-json"},
		"node-launched plugin": {"/usr/local/bin/node", "/opt/claude/cli.js", "--plugin-dir", "/tmp/mine"},
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			f.procs.argv[fakeClaudePID] = args
			assertUnresolved(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "loaded for this session only")
		})
	}

	t.Run("plugin dirs variable", func(t *testing.T) {
		f := setup(t)
		f.setenv(claudePluginDirsEnv, "/tmp/mine")
		assertUnresolved(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), claudePluginDirsEnv+" is set")
	})

	t.Run("no Claude Code process", func(t *testing.T) {
		f := setup(t)
		f.setenv(claudePIDEnv, "")
		assertUnresolved(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "could not identify the Claude Code process")
	})

	// CLAUDE_PID is trusted only for a process the hook descends from.
	t.Run("not an ancestor", func(t *testing.T) {
		f := setup(t)
		f.procs.argv[999] = []string{"claude"}
		f.setenv(claudePIDEnv, "999")
		assertUnresolved(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "could not confirm Claude Code process 999")
	})

	t.Run("arguments unreadable", func(t *testing.T) {
		f := setup(t)
		delete(f.procs.argv, fakeClaudePID)
		assertUnresolved(t, Resolve(f.Env, pluginReq("p", "s", f.path("proj"))), "could not read Claude Code process")
	})

	// Other sources do not depend on which plugins the session loaded.
	t.Run("other sources", func(t *testing.T) {
		f := setup(t)
		f.procs.argv[fakeClaudePID] = []string{"claude", "--plugin-dir", "/tmp/mine"}
		f.write(f.homePath(".claude.json"), `{"mcpServers":{"u":{"type":"http","url":"https://user.example.com/mcp"}}}`)
		assertURL(t, Resolve(f.Env, claudeCodeReq(claudeSourceUser, "u", f.path("proj"))), "https://user.example.com/mcp")
	})
}

// TestOSProcesses covers the live process table on the host running the tests:
// the test binary's parent and its own arguments.
func TestOSProcesses(t *testing.T) {
	procs := osProcesses{}
	if procs.self() != os.Getpid() {
		t.Fatalf("self = %d, want %d", procs.self(), os.Getpid())
	}
	parent, err := procs.parent(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if parent != os.Getppid() {
		t.Fatalf("parent = %d, want %d", parent, os.Getppid())
	}
	args, err := procs.args(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(args, os.Args) {
		t.Fatalf("args = %q, want %q", args, os.Args)
	}
}
