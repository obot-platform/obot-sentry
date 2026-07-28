package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/mdmconfig"
	"github.com/obot-platform/obot/apiclient/types"
	"github.com/spf13/cobra"
)

// enforceRoot builds a root command whose MDM loader is stubbed, so tests never
// depend on the real deployment configuration of the host running the suite.
func enforceRoot(t *testing.T, cfg mdmconfig.Config) *cobra.Command {
	t.Helper()
	for _, key := range []string{
		"OBOT_SENTRY_SERVER_URL",
		"OBOT_SENTRY_ENROLLMENT_KEY",
		"ENFORCE_SERVER_URL",
		"ENFORCE_ENROLLMENT_KEY",
		"ENFORCE_AGENT",
		"ENFORCE_EVENT",
	} {
		t.Setenv(key, "")
	}
	return newRoot(func() (mdmconfig.Config, error) { return cfg, nil })
}

// runCommand executes args, returning stdout, stderr, and the error the root
// command surfaced.
func runCommand(t *testing.T, root *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// homeFixture points the process's home directory at a temp tree, so the
// resolvers read fixture configuration instead of the developer's own.
func homeFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEnforcePrintNormalizedDryRun(t *testing.T) {
	home := homeFixture(t)
	writeFixtureFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{
		"linear": {"command": "npx", "args": ["-y", "linear-mcp@1.2.3"]}
	}}`)
	input := writeTempFile(t, `{"tool_name":"mcp__linear__search_issues","cwd":"`+home+`"}`)

	stdout, stderr, err := runCommand(t, enforceRoot(t, mdmconfig.Config{}),
		"enforce", "--agent", "claude-code", "--event", "PreToolUse",
		"--input", input, "--print-normalized", "--dry-run")
	if err != nil {
		t.Fatalf("enforce --dry-run: %v", err)
	}

	var req types.EnforcementDecisionRequest
	if err := json.Unmarshal([]byte(stdout), &req); err != nil {
		t.Fatalf("stdout is not the normalized request: %v (%q)", err, stdout)
	}
	if req.Agent != "claude_code" || req.Tool != "search_issues" || req.Kind != "mcp" || req.ServerName != "linear" {
		t.Errorf("normalized request = %+v, want the resolved Linear call", req)
	}
	if req.Server.Package == nil || req.Server.Package.Name != "linear-mcp" || req.Server.Package.Version != "1.2.3" {
		t.Errorf("package = %+v, want linear-mcp 1.2.3", req.Server.Package)
	}
	if req.Unresolved {
		t.Errorf("the call was reported unresolved: %s", req.UnresolvedReason)
	}
	if !strings.Contains(stderr, "would: ALLOW (policy not consulted; --dry-run)") {
		t.Errorf("stderr = %q, want the dry-run verdict", stderr)
	}
}

func TestEnforceDeniesWithNoServerConfigured(t *testing.T) {
	home := homeFixture(t)
	input := writeTempFile(t, `{"tool_name":"Bash","cwd":"`+home+`"}`)

	stdout, stderr, err := runCommand(t, enforceRoot(t, mdmconfig.Config{}),
		"enforce", "--agent", "claude-code", "--event", "PreToolUse", "--input", input)
	if err != nil {
		t.Fatalf("the hook exited non-zero: %v", err)
	}

	var out struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("stdout is not a hook response: %v (%q)", err, stdout)
	}
	if out.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("permissionDecision = %q, want deny", out.HookSpecificOutput.PermissionDecision)
	}
	if !strings.Contains(out.HookSpecificOutput.PermissionDecisionReason, "no Obot server URL is configured") {
		t.Errorf("deny reason = %q, want it to name the missing configuration", out.HookSpecificOutput.PermissionDecisionReason)
	}
	if !strings.Contains(stderr, "no Obot server URL is configured") {
		t.Errorf("stderr = %q, want the blocking reason", stderr)
	}
}

func TestEnforceUnsupportedAgentExitsNonZero(t *testing.T) {
	homeFixture(t)
	input := writeTempFile(t, `{"tool_name":"Bash"}`)

	stdout, stderr, err := runCommand(t, enforceRoot(t, mdmconfig.Config{}),
		"enforce", "--agent", "vscode", "--event", "PreToolUse", "--input", input)

	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) {
		t.Fatalf("err = %v, want an ExitCodeError", err)
	}
	if exitErr.Code != 2 {
		t.Errorf("exit code = %d, want 2", exitErr.Code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing on the protocol channel", stdout)
	}
	if !strings.Contains(stderr, `unsupported enforcement agent "vscode"`) {
		t.Errorf("stderr = %q, want the reason", stderr)
	}
}

func TestEnforceMissingInputFileExitsNonZero(t *testing.T) {
	homeFixture(t)
	stdout, _, err := runCommand(t, enforceRoot(t, mdmconfig.Config{}),
		"enforce", "--agent", "claude-code", "--event", "PreToolUse",
		"--input", filepath.Join(t.TempDir(), "absent.json"))

	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) {
		t.Fatalf("err = %v, want an ExitCodeError", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing on the protocol channel", stdout)
	}
}

func TestEnforceUnparseableInvocationExitsBlocking(t *testing.T) {
	homeFixture(t)
	input := writeTempFile(t, `{"tool_name":"Bash"}`)

	for _, args := range [][]string{
		{"enforce", "--agent", "claude-code", "--event", "PreToolUse", "--input", input, "--future-flag"},
		{"enforce", "--agent", "claude-code", "--event", "PreToolUse", "--input", input, "stray-argument"},
	} {
		stdout, _, err := runCommand(t, enforceRoot(t, mdmconfig.Config{}), args...)

		var exitErr *ExitCodeError
		if !errors.As(err, &exitErr) {
			t.Fatalf("%v: err = %v, want an ExitCodeError", args, err)
		}
		if exitErr.Code != 2 {
			t.Errorf("%v: exit code = %d, want 2 (a blocking hook error)", args, exitErr.Code)
		}
		if stdout != "" {
			t.Errorf("%v: stdout = %q, want nothing on the protocol channel", args, stdout)
		}
	}
}

func TestEnforceManagedByMarker(t *testing.T) {
	home := homeFixture(t)
	input := writeTempFile(t, `{"tool_name":"Bash","cwd":"`+home+`"}`)

	if _, _, err := runCommand(t, enforceRoot(t, mdmconfig.Config{}),
		"enforce", "--agent", "claude-code", "--event", "PreToolUse",
		"--input", input, "--managed-by", "obot-sentry", "--dry-run"); err != nil {
		t.Fatalf("the marker hook-install writes was rejected: %v", err)
	}

	if _, _, err := runCommand(t, enforceRoot(t, mdmconfig.Config{}),
		"enforce", "--agent", "claude-code", "--event", "PreToolUse",
		"--input", input, "--managed-by", "someone-else", "--dry-run"); err == nil {
		t.Fatal("a foreign --managed-by value was accepted")
	}
}

func TestEnforceResolvePrintsTheTrace(t *testing.T) {
	home := homeFixture(t)
	config := filepath.Join(home, ".codex", "config.toml")
	writeFixtureFile(t, config, `
[mcp_servers.probe-npx-stdio]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-everything"]
`)

	// Codex folds punctuation in the tool namespace, so this is the name a real
	// call reports — and the trace has to show it matching the hyphenated key.
	stdout, _, err := runCommand(t, enforceRoot(t, mdmconfig.Config{}),
		"enforce", "resolve", "--agent", "codex", "--server", "probe_npx_stdio", "--cwd", home)
	if err != nil {
		t.Fatalf("enforce resolve: %v\n%s", err, stdout)
	}

	if !strings.Contains(stdout, config) || !strings.Contains(stdout, "FOUND") {
		t.Errorf("trace does not show the match:\n%s", stdout)
	}
	if !strings.Contains(stdout, "server name: probe-npx-stdio") {
		t.Errorf("trace does not report the matched configuration key:\n%s", stdout)
	}
	if !strings.Contains(stdout, "resolved: npm / @modelcontextprotocol/server-everything / any version") {
		t.Errorf("trace does not report the resolved package:\n%s", stdout)
	}
}

func TestEnforceResolveUnresolvedExitsNonZero(t *testing.T) {
	home := homeFixture(t)
	writeFixtureFile(t, filepath.Join(home, ".codex", "config.toml"), `
[mcp_servers.local-binary]
command = "/opt/homebrew/bin/some-server"
`)

	stdout, _, err := runCommand(t, enforceRoot(t, mdmconfig.Config{}),
		"enforce", "resolve", "--agent", "codex", "--server", "local-binary", "--cwd", home)

	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) {
		t.Fatalf("err = %v, want an ExitCodeError", err)
	}
	if exitErr.Code != 1 {
		t.Errorf("exit code = %d, want 1", exitErr.Code)
	}
	// The reason is on stdout, where the rest of the diagnostic is.
	if !strings.Contains(stdout, "unresolved: stdio command") {
		t.Errorf("stdout does not explain the failure:\n%s", stdout)
	}
	if !strings.Contains(stdout, "server name: local-binary") {
		t.Errorf("stdout does not name the server an administrator would allowlist:\n%s", stdout)
	}
}

func TestEnforceResolveRejectsUnsupportedAgent(t *testing.T) {
	homeFixture(t)
	if _, _, err := runCommand(t, enforceRoot(t, mdmconfig.Config{}),
		"enforce", "resolve", "--agent", "vscode", "--server", "anything"); err == nil {
		t.Fatal("enforce resolve accepted vscode")
	}
	if _, _, err := runCommand(t, enforceRoot(t, mdmconfig.Config{}),
		"enforce", "resolve", "--agent", "claude-code"); err == nil {
		t.Fatal("enforce resolve accepted an empty --server")
	}
}
