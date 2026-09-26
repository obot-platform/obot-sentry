package audit

import (
	"encoding/json"
	"testing"
)

func TestProcessZCodeSuccess(t *testing.T) {
	payload := []byte(`{
		"hook_event_name":"PostToolUse",
		"session_id":"zcode-session",
		"tool_use_id":"zcode-tool",
		"tool_name":"mcp__docs__search",
		"tool_input":{"query":"obot"},
		"tool_response":{"ok":true},
		"mcp_server":"docs",
		"mcp_tool":"search",
		"cwd":"/work/project",
		"agent_version":"3.14.3"
	}`)
	result, err := Process(payload, ProcessOptions{
		Agent:     AgentZCode,
		Phase:     PhasePostTool,
		SkipLocal: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entries = %d, want 1: %+v", len(result.Entries), result.Warnings)
	}
	entry := result.Entries[0]
	if entry.AgentProvider != "zcode" || entry.Status != StatusSuccess || entry.ToolName != "mcp__docs__search" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.MCPServerHint != "docs" || entry.MCPToolName != "search" || entry.ToolUseID != "zcode-tool" {
		t.Fatalf("structured ZCode fields were not preserved: %+v", entry)
	}
	if entry.AgentVersion != "3.14.3" {
		t.Fatalf("agent version = %q", entry.AgentVersion)
	}
}

func TestProcessZCodeFailure(t *testing.T) {
	payload := []byte(`{
		"hook_event_name":"PostToolUseFailure",
		"session_id":"zcode-session",
		"tool_use_id":"zcode-tool",
		"tool_name":"Bash",
		"tool_input":{"command":"false"},
		"error":"command failed",
		"is_interrupt":false,
		"cwd":"/work/project"
	}`)
	result, err := Process(payload, ProcessOptions{
		Agent:     AgentZCode,
		Phase:     PhaseFailure,
		SkipLocal: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entries = %d, want 1: %+v", len(result.Entries), result.Warnings)
	}
	entry := result.Entries[0]
	if entry.Status != StatusFailure || entry.Error != "command failed" || entry.ToolKind != "shell" {
		t.Fatalf("unexpected failure entry: %+v", entry)
	}
	if string(entry.ToolOutput) != "null" {
		t.Fatalf("failure output = %s, want null", entry.ToolOutput)
	}
	if !json.Valid(entry.RawHookPayload) {
		t.Fatal("raw hook payload is not valid JSON")
	}
}
