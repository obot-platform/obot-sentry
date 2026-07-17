package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/obot-platform/obot-sentry/pkg/audit"
	"github.com/obot-platform/obot-sentry/pkg/mdmconfig"
	"github.com/obot-platform/obot/apiclient/types"
	"github.com/spf13/cobra"
)

func TestAuditEntriesToInputsBuildsExplicitNormalizedTarget(t *testing.T) {
	events := auditEntriesToInputs([]audit.Entry{{
		AgentProvider:  "codex",
		CLIVersion:     "1.2.3",
		Status:         audit.StatusSuccess,
		OccurredAt:     time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
		IdempotencyKey: "event-1",
		ToolName:       "mcp__github__search",
		ToolKind:       "mcp",
		MCPServerHint:  "github",
		MCPToolName:    "search",
	}})
	if len(events) != 1 {
		t.Fatalf("expected one event, got %d", len(events))
	}
	event := events[0]
	if event.Action.Name != "mcp__github__search" || event.Target.TargetType != types.AuditLogTargetTypeMCPTool || event.Target.Name != "search" {
		t.Fatalf("unexpected action/target: %#v %#v", event.Action, event.Target)
	}
	if event.Target.Parent == nil || event.Target.Parent.TargetType != types.AuditLogTargetTypeMCPServer || event.Target.Parent.Name != "github" {
		t.Fatalf("unexpected target parent: %#v", event.Target.Parent)
	}
	if event.Outcome.Status != types.AuditLogOutcomeStatusSuccess || event.Details.Trace.IdempotencyKey != "event-1" {
		t.Fatalf("unexpected outcome/trace: %#v %#v", event.Outcome, event.Details.Trace)
	}
}

func TestAuditSubmitPrintNormalized(t *testing.T) {
	input := writeTempAuditPayload(t, `{
		"session_id": "session-1",
		"turn_id": "turn-1",
		"tool_use_id": "tool-1",
		"tool_name": "Bash",
		"tool_input": {"command": "echo hi"},
		"tool_response": "hi"
	}`)

	root := New()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"audit", "submit", "--agent", "codex", "--phase", "post-tool", "--input", input, "--print-normalized"})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
	var entries []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("expected normalized JSON on stdout, got %q: %v", stdout.String(), err)
	}
	if len(entries) != 1 {
		t.Fatalf("unexpected normalized entries: %#v", entries)
	}
	action, _ := entries[0]["action"].(map[string]any)
	details, _ := entries[0]["details"].(map[string]any)
	agent, _ := details["agent"].(map[string]any)
	if action["name"] != "Bash" || agent["provider"] != "codex" {
		t.Fatalf("unexpected normalized entries: %#v", entries)
	}
}

func TestAuditSubmitDryRunWritesAuditLogToUserCache(t *testing.T) {
	input := writeTempAuditPayload(t, `{
		"session_id": "session-1",
		"turn_id": "turn-1",
		"tool_use_id": "tool-1",
		"tool_name": "Bash",
		"tool_input": {"command": "echo hi"},
		"tool_response": "hi"
	}`)
	cacheDir := auditCacheDir(t)

	root := New()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"audit", "submit", "--agent", "codex", "--phase", "post-tool", "--input", input, "--dry-run"})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected dry-run to keep stdout empty, got %q", stdout.String())
	}

	logDir := filepath.Join(cacheDir, "obot", "obot-sentry", "audit-logs")
	files, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("read dry-run audit log directory: %v", err)
	}
	if len(files) != 1 || files[0].IsDir() || filepath.Ext(files[0].Name()) != ".json" {
		t.Fatalf("expected one JSON audit log file, got %#v", files)
	}
	path := filepath.Join(logDir, files[0].Name())
	if !strings.Contains(stderr.String(), path) {
		t.Fatalf("expected stderr to report written audit log %q, got %q", path, stderr.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var log map[string]any
	if err := json.Unmarshal(data, &log); err != nil {
		t.Fatalf("decode dry-run audit log: %v", err)
	}
	action, _ := log["action"].(map[string]any)
	details, _ := log["details"].(map[string]any)
	agent, _ := details["agent"].(map[string]any)
	trace, _ := details["trace"].(map[string]any)
	if action["name"] != "Bash" || agent["provider"] != "codex" || trace["idempotencyKey"] == "" {
		t.Fatalf("unexpected dry-run audit log: %#v", log)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("dry-run audit log permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestAuditSubmitDryRunDoesNotOverwriteExistingLog(t *testing.T) {
	input := writeTempAuditPayload(t, `{
		"session_id": "session-1",
		"tool_use_id": "tool-1",
		"tool_name": "Bash",
		"tool_input": {"command": "echo hi"},
		"tool_response": "hi"
	}`)
	cacheDir := auditCacheDir(t)

	for range 2 {
		root := New()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"audit", "submit", "--agent", "codex", "--phase", "post-tool", "--input", input, "--dry-run"})
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
	}

	logDir := filepath.Join(cacheDir, "obot", "obot-sentry", "audit-logs")
	files, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected repeated dry-runs to create two files, got %d", len(files))
	}
}

func TestAuditSubmitNormalExecutionDoesNotWriteStdout(t *testing.T) {
	root := withoutAuditDeploymentConfig(t)

	input := writeTempAuditPayload(t, `{
		"session_id": "session-1",
		"tool_use_id": "tool-1",
		"tool_name": "Bash",
		"tool_input": {"command": "echo hi"},
		"tool_response": "hi"
	}`)

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"audit", "submit", "--agent", "vscode", "--phase", "post-tool", "--input", input})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected normal execution to keep stdout empty, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no ServerURL configured") {
		t.Fatalf("expected missing ServerURL warning, got %q", stderr.String())
	}
}

func TestAuditSubmitRejectsUnknownManagedByBeforeReadingPayload(t *testing.T) {
	root := New()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"audit", "submit", "--agent", "codex", "--phase", "post-tool", "--input", "/does/not/exist", "--managed-by", "someone-else"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected --managed-by validation error")
	}
	if !strings.Contains(err.Error(), "--managed-by") {
		t.Fatalf("expected managed-by error, got %v", err)
	}
}

func TestAuditSubmitHiddenFromRootHelp(t *testing.T) {
	root := New()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--help"})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	help := stdout.String()
	if strings.Contains(help, "audit") || strings.Contains(help, "submit") {
		t.Fatalf("audit submit should be hidden from root help, got:\n%s", help)
	}
}

func TestAuditSubmitMissingServerURLDoesNotSpool(t *testing.T) {
	root := withoutAuditDeploymentConfig(t)

	input := writeTempAuditPayload(t, `{
		"session_id": "session-1",
		"tool_use_id": "tool-1",
		"tool_name": "Bash",
		"tool_input": {"command": "echo hi"},
		"tool_response": "hi"
	}`)

	cacheDir := auditCacheDir(t)

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"audit", "submit", "--agent", "codex", "--phase", "post-tool", "--input", input})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "no ServerURL configured") {
		t.Fatalf("expected missing ServerURL warning, got %q", stderr.String())
	}
	// Without an authenticated device identity there is nothing to submit, so
	// the completed event must fail open rather than being spooled.
	spoolDir := filepath.Join(cacheDir, "obot", "obot-sentry", "audit-spool")
	if _, err := os.Stat(spoolDir); !os.IsNotExist(err) {
		t.Fatalf("missing ServerURL must not spool; audit-spool exists (err=%v)", err)
	}
}

func TestAuditSubmitRejectsPreToolPhase(t *testing.T) {
	input := writeTempAuditPayload(t, `{
		"session_id": "session-1",
		"tool_use_id": "tool-1",
		"tool_name": "mcp__github__search",
		"tool_input": {"query": "obot"}
	}`)

	cacheDir := auditCacheDir(t)

	root := New()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"audit", "submit", "--agent", "codex", "--phase", "pre-tool", "--input", input})

	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "unsupported audit phase") {
		t.Fatalf("expected pre-tool phase to be rejected, got %v", err)
	}
	spoolDir := filepath.Join(cacheDir, "obot", "obot-sentry", "audit-spool")
	if _, err := os.Stat(spoolDir); !os.IsNotExist(err) {
		t.Fatalf("rejected pre-tool invocation must not create the encrypted submit spool (err=%v)", err)
	}
}

// auditCacheDir redirects os.UserCacheDir at a temp location on every platform
// and returns the effective cache directory the audit command's spool will
// use. os.UserCacheDir resolves differently per OS
// (HOME/Library/Caches on macOS, XDG_CACHE_HOME/HOME/.cache on Linux,
// %LocalAppData% on Windows), so redirecting HOME plus clearing XDG_CACHE_HOME
// and setting LocalAppData covers all three rather than assuming XDG on macOS.
func auditCacheDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("LocalAppData", filepath.Join(home, "AppData", "Local"))
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("resolve user cache dir: %v", err)
	}
	return cache
}

// withoutAuditDeploymentConfig builds a root command whose MDM loader is
// stubbed to an empty config, keeping missing-configuration tests independent
// of environment variables and real MDM state on the host running the suite.
func withoutAuditDeploymentConfig(t *testing.T) *cobra.Command {
	t.Helper()
	for _, key := range []string{
		"OBOT_SENTRY_SERVER_URL",
		"OBOT_SENTRY_ENROLLMENT_KEY",
	} {
		t.Setenv(key, "")
	}

	return newRoot(func() (mdmconfig.Config, error) {
		return mdmconfig.Config{}, nil
	})
}

func writeTempAuditPayload(t *testing.T, payload string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "payload-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(payload); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}
