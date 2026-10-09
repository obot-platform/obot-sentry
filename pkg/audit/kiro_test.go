package audit

import (
	"strings"
	"testing"
	"time"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

func processKiro(t *testing.T, phase Phase, payload string) Result {
	t.Helper()
	result, err := Process([]byte(payload), ProcessOptions{
		Agent:      localagent.Kiro,
		Phase:      phase,
		Now:        func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) },
		Enrichment: &Enrichment{Hostname: "host-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// TestProcessKiroPostTool uses the PostToolUse payload Kiro 1.2 builds:
// session_id, hook_event_name, cwd, tool_name (the tool id), tool_input, and
// tool_response.
func TestProcessKiroPostTool(t *testing.T) {
	result := processKiro(t, PhasePostTool, `{
		"session_id": "kiro-session",
		"hook_event_name": "PostToolUse",
		"cwd": "/work/repo",
		"tool_name": "execute_bash",
		"tool_input": {"command": "go test ./..."},
		"tool_response": {"exitCode": 0, "stdout": "ok"}
	}`)
	if len(result.Warnings) != 0 || len(result.Entries) != 1 {
		t.Fatalf("result = %+v", result)
	}
	entry := result.Entries[0]
	if entry.AgentProvider != "kiro" || entry.Status != StatusSuccess || entry.SessionID != "kiro-session" {
		t.Fatalf("entry = %+v", entry)
	}
	// execute_bash is a shell call, which the generic heuristics would miss.
	if entry.ToolName != "execute_bash" || entry.ToolKind != "shell" || entry.CWD != "/work/repo" {
		t.Fatalf("entry = %+v", entry)
	}
	if !strings.Contains(string(entry.ToolOutput), `"stdout": "ok"`) {
		t.Fatalf("tool output = %s", entry.ToolOutput)
	}
}

func TestProcessKiroMCPTools(t *testing.T) {
	// An MCP id splits at any underscore, so it carries no reliable server hint.
	entry := processKiro(t, PhasePostTool, `{"tool_name": "mcp_github_enterprise_create_issue", "tool_input": {}}`).Entries[0]
	if entry.ToolKind != "mcp" || entry.MCPServerHint != "" || entry.MCPToolName != "" {
		t.Fatalf("mcp id entry = %+v", entry)
	}

	// A Power's tool names its server and tool exactly.
	entry = processKiro(t, PhasePostTool, `{"tool_name": "kiro_powers", "tool_input": {
		"action": "use", "powerName": "stripe", "serverName": "stripe", "toolName": "create_payment_link", "arguments": {}
	}}`).Entries[0]
	if entry.ToolKind != "mcp" || entry.MCPServerHint != "power-stripe-stripe" || entry.MCPToolName != "create_payment_link" {
		t.Fatalf("kiro_powers entry = %+v", entry)
	}

	// Its other actions are the Power's own files, not an MCP call.
	entry = processKiro(t, PhasePostTool, `{"tool_name": "kiro_powers", "tool_input": {"action": "activate", "powerName": "stripe"}}`).Entries[0]
	if entry.ToolKind != "generic" || entry.MCPServerHint != "" {
		t.Fatalf("kiro_powers activate entry = %+v", entry)
	}
}

// TestProcessKiroFailurePhaseUnsupported: Kiro has no failure trigger, so a
// failure-phase invocation is a misconfiguration that submits nothing.
func TestProcessKiroFailurePhaseUnsupported(t *testing.T) {
	result := processKiro(t, PhaseFailure, `{"tool_name": "execute_bash", "tool_input": {}}`)
	if len(result.Entries) != 0 || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "kiro failure hooks are not supported") {
		t.Fatalf("result = %+v", result)
	}
}
