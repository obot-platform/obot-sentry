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

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }

// hookPayload renders a pre-tool hook payload naming tool and cwd. The cwd is
// encoded rather than interpolated into the JSON: a Windows path is full of
// backslashes, and a backslash opens an escape inside a JSON string.
func hookPayload(t *testing.T, tool, cwd string) string {
	t.Helper()
	data, err := json.Marshal(map[string]string{"tool_name": tool, "cwd": cwd})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
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
	input := writeTempFile(t, hookPayload(t, "mcp__linear__search_issues", home))

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
	input := writeTempFile(t, hookPayload(t, "Bash", home))

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
	if strings.Contains(out.HookSpecificOutput.PermissionDecisionReason, "no Obot server URL is configured") {
		t.Errorf("deny response exposed infrastructure detail: %q", out.HookSpecificOutput.PermissionDecisionReason)
	}
	if stderr != "obot-sentry enforce: blocked\n" {
		t.Errorf("stderr = %q, want only the static block notice", stderr)
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
	if stderr != "obot-sentry enforce: blocked\n" {
		t.Errorf("stderr = %q, want only the static block notice", stderr)
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
	input := writeTempFile(t, hookPayload(t, "Bash", home))

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

func TestEnforceServerURLIgnoresEnvironment(t *testing.T) {
	home := homeFixture(t)
	input := writeTempFile(t, hookPayload(t, "Bash", home))
	t.Setenv("OBOT_SENTRY_SERVER_URL", "https://attacker.invalid")
	t.Setenv("ENFORCE_SERVER_URL", "https://also-attacker.invalid")

	cmd, hook := newEnforceCommand()
	cmd.SetArgs([]string{"--agent", "claude-code", "--event", "PreToolUse", "--input", input, "--dry-run"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("enforce --dry-run: %v", err)
	}
	if hook.serverURL != "" {
		t.Fatalf("server URL was populated from the environment: %q", hook.serverURL)
	}

	hook.loadMDMConfig = func() (mdmconfig.Config, error) {
		return mdmconfig.Config{ServerURL: "https://mdm.example.com"}, nil
	}
	cfg, err := hook.resolveConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerURL != "https://mdm.example.com" {
		t.Fatalf("resolved server URL = %q, want the protected MDM value", cfg.ServerURL)
	}
}

func TestEnforceProtocolFlagsIgnoreEnvironment(t *testing.T) {
	home := homeFixture(t)
	input := writeTempFile(t, `{"tool_name":"Bash","cwd":"`+home+`"}`)
	root := enforceRoot(t, mdmconfig.Config{})

	// These names used to be implicit bindings created by the command builder.
	// A hook inherits the invoking agent's environment, so either debug flag
	// could turn a real decision into a successful, response-free dry run or
	// corrupt stdout with diagnostic JSON.
	t.Setenv("ENFORCE_DRY_RUN", "true")
	t.Setenv("ENFORCE_PRINT_NORMALIZED", "true")

	stdout, stderr, err := runCommand(t, root,
		"enforce", "--agent", "claude-code", "--event", "PreToolUse", "--input", input)
	if err != nil {
		t.Fatalf("the hook exited non-zero: %v", err)
	}
	var out struct {
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("stdout is not one protocol response: %v (%q)", err, stdout)
	}
	if out.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("permissionDecision = %q, want deny", out.HookSpecificOutput.PermissionDecision)
	}
	if stderr != "obot-sentry enforce: blocked\n" {
		t.Fatalf("stderr = %q, want a real blocking decision", stderr)
	}
}

func TestEnforceInputIgnoresEnvironment(t *testing.T) {
	home := homeFixture(t)
	root := enforceRoot(t, mdmconfig.Config{})
	fake := writeTempFile(t, `{"tool_name":"Read","cwd":"`+home+`"}`)
	t.Setenv("ENFORCE_INPUT", fake)

	stdinPath := writeTempFile(t, `{"tool_name":"Bash","cwd":"`+home+`"}`)
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	originalStdin := os.Stdin
	os.Stdin = stdin
	t.Cleanup(func() {
		os.Stdin = originalStdin
		_ = stdin.Close()
	})

	stdout, _, err := runCommand(t, root,
		"enforce", "--agent", "codex", "--event", "PreToolUse", "--dry-run", "--print-normalized")
	if err != nil {
		t.Fatalf("enforce --dry-run: %v", err)
	}
	var req types.EnforcementDecisionRequest
	if err := json.Unmarshal([]byte(stdout), &req); err != nil {
		t.Fatalf("stdout is not the normalized request: %v (%q)", err, stdout)
	}
	if req.Tool != "Bash" || req.Kind != "shell" {
		t.Fatalf("request = %+v, want the real stdin payload rather than ENFORCE_INPUT", req)
	}
}

func TestEnforceAgentAndEventIgnoreEnvironment(t *testing.T) {
	home := homeFixture(t)
	input := writeTempFile(t, `{"tool_name":"Bash","cwd":"`+home+`"}`)
	root := enforceRoot(t, mdmconfig.Config{})
	t.Setenv("ENFORCE_AGENT", "claude-code")
	t.Setenv("ENFORCE_EVENT", "PreToolUse")

	stdout, _, err := runCommand(t, root, "enforce", "--input", input)
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != 2 {
		t.Fatalf("err = %v, want blocking exit code 2 with no explicit agent or event", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want no protocol response for an unusable invocation", stdout)
	}
}

func TestEnforceManagedByIgnoresEnvironment(t *testing.T) {
	home := homeFixture(t)
	input := writeTempFile(t, `{"tool_name":"Bash","cwd":"`+home+`"}`)
	root := enforceRoot(t, mdmconfig.Config{})
	t.Setenv("ENFORCE_MANAGED_BY", "someone-else")

	stdout, _, err := runCommand(t, root,
		"enforce", "--agent", "claude-code", "--event", "PreToolUse", "--input", input)
	if err != nil {
		t.Fatalf("environment supplied --managed-by affected the invocation: %v", err)
	}
	var out struct {
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("stdout is not a hook response: %v (%q)", err, stdout)
	}
	if out.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("permissionDecision = %q, want deny", out.HookSpecificOutput.PermissionDecision)
	}
}

func TestEnforceExplicitServerURLOverridesMDM(t *testing.T) {
	cmd, hook := newEnforceCommand()
	if err := cmd.Flags().Parse([]string{"--server-url", "https://explicit.example.com"}); err != nil {
		t.Fatal(err)
	}
	hook.loadMDMConfig = func() (mdmconfig.Config, error) {
		return mdmconfig.Config{ServerURL: "https://mdm.example.com"}, nil
	}
	cfg, err := hook.resolveConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerURL != "https://explicit.example.com" {
		t.Fatalf("resolved server URL = %q, want explicit override", cfg.ServerURL)
	}
}

func TestEnforceResponseWriteFailureExitsBlocking(t *testing.T) {
	home := homeFixture(t)
	input := writeTempFile(t, hookPayload(t, "Bash", home))
	wantErr := errors.New("hook stdout is closed")
	root := enforceRoot(t, mdmconfig.Config{})
	root.SetOut(errorWriter{err: wantErr})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"enforce", "--agent", "claude-code", "--event", "PreToolUse", "--input", input})
	err := root.Execute()

	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != 2 {
		t.Fatalf("err = %v, want blocking exit code 2", err)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want it to wrap the output failure", err)
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

// TestEnforceKiroDenyExitsTwoQuietly: Kiro blocks only on exit 2 and shows
// stderr to the model, so a Kiro denial is the agent message on stderr, nothing
// on stdout, and a quiet exit 2 that main does not log over.
func TestEnforceKiroDenyExitsTwoQuietly(t *testing.T) {
	home := homeFixture(t)
	input := writeTempFile(t, `{"session_id":"sess_x","hook_event_name":"PreToolUse","cwd":"`+home+`","tool_name":"execute_bash","tool_input":{"command":"ls"}}`)

	stdout, stderr, err := runCommand(t, enforceRoot(t, mdmconfig.Config{}),
		"enforce", "--agent", "kiro", "--event", "PreToolUse", "--input", input)
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != 2 || !exitErr.Quiet {
		t.Fatalf("err = %#v, want a quiet exit 2", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if !strings.Contains(stderr, "Obot blocked this call") || strings.Contains(stderr, "obot-sentry enforce: blocked") {
		t.Errorf("stderr = %q, want only the agent-facing denial", stderr)
	}
}

// TestEnforceKiroDryRunNeverBlocks: a dry run promises no verdict, so even a
// payload that can't be read must not take Kiro's blocking exit.
func TestEnforceKiroDryRunNeverBlocks(t *testing.T) {
	input := writeTempFile(t, "{")
	if _, _, err := runCommand(t, enforceRoot(t, mdmconfig.Config{}),
		"enforce", "--agent", "kiro", "--event", "PreToolUse", "--input", input, "--dry-run"); err != nil {
		t.Fatalf("enforce --dry-run exited non-zero: %v", err)
	}
}
