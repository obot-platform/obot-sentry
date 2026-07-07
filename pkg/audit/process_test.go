package audit

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProcessCodexPostToolNormalizesCompletedEntry(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	payload := []byte(`{
		"session_id": "session-1",
		"turn_id": "turn-1",
		"tool_use_id": "tool-1",
		"tool_name": "mcp__github__search",
		"tool_input": {"query": "obot"},
		"tool_response": {"ok": true},
		"model": "gpt-5",
		"permission_mode": "default",
		"cwd": "/work/repo",
		"transcript_path": "/tmp/transcript.jsonl"
	}`)

	result, err := Process(payload, ProcessOptions{
		Agent: AgentCodex,
		Phase: PhasePostTool,
		Now:   func() time.Time { return now },
		Enrichment: &Enrichment{
			CWD:           "/fallback",
			Hostname:      "host-1",
			OS:            "darwin",
			Arch:          "arm64",
			LocalUsername: "grant",
			GitRepoRoot:   "/work/repo",
			GitRemoteURLs: []string{"git@example.com:obot/obot.git"},
			GitBranch:     "main",
			GitCommitSHA:  "abc123",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", result.Warnings)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(result.Entries))
	}

	entry := result.Entries[0]
	if entry.AgentProvider != "codex" || entry.Status != StatusSuccess {
		t.Fatalf("unexpected provider/status: %#v", entry)
	}
	if entry.SessionID != "session-1" || entry.TurnID != "turn-1" || entry.ToolUseID != "tool-1" {
		t.Fatalf("native IDs were not normalized: %#v", entry)
	}
	if entry.ToolKind != "mcp" || entry.MCPServerHint != "github" || entry.MCPToolName != "search" {
		t.Fatalf("MCP tool classification failed: %#v", entry)
	}
	if entry.CWD != "/work/repo" || entry.GitRepoRoot != "/work/repo" || entry.Hostname != "host-1" {
		t.Fatalf("local enrichment missing: %#v", entry)
	}
	if entry.OccurredAt != now {
		t.Fatalf("expected occurredAt %s, got %s", now, entry.OccurredAt)
	}
	if entry.IdempotencyKey == "" {
		t.Fatal("expected idempotency key")
	}

	result2, err := Process(payload, ProcessOptions{
		Agent:      AgentCodex,
		Phase:      PhasePostTool,
		Now:        func() time.Time { return now },
		Enrichment: &Enrichment{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result2.Entries[0].IdempotencyKey != entry.IdempotencyKey {
		t.Fatalf("idempotency key should be deterministic for the same payload: %q != %q", result2.Entries[0].IdempotencyKey, entry.IdempotencyKey)
	}
}

func TestProcessPostToolWithoutOutputSubmitsNullOutput(t *testing.T) {
	// A successful terminal event that reports no tool_response is still a
	// completed tool call and must produce one entry with an explicit null
	// output rather than being dropped.
	for _, agent := range []Agent{AgentCodex, AgentClaudeCode, AgentVSCode} {
		t.Run(string(agent), func(t *testing.T) {
			payload := []byte(`{
				"session_id": "session-x",
				"tool_use_id": "tool-x",
				"tool_name": "Bash",
				"tool_input": {"command": "true"}
			}`)
			result, err := Process(payload, ProcessOptions{
				Agent:      agent,
				Phase:      PhasePostTool,
				Now:        func() time.Time { return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC) },
				Enrichment: &Enrichment{},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Entries) != 1 {
				t.Fatalf("expected one entry (event must not be dropped), got %d; warnings=%v", len(result.Entries), result.Warnings)
			}
			if got := strings.TrimSpace(string(result.Entries[0].ToolOutput)); got != "null" {
				t.Fatalf("expected explicit null output, got %q", got)
			}
			if result.Entries[0].Status != StatusSuccess {
				t.Fatalf("expected success status, got %s", result.Entries[0].Status)
			}
		})
	}
}

func TestProcessCursorPostToolWithoutOutputSubmitsNullOutput(t *testing.T) {
	payload := []byte(`{
		"conversation_id": "conv-x",
		"tool_use_id": "tool-x",
		"tool_name": "Bash",
		"tool_input": {"command": "true"}
	}`)
	result, err := Process(payload, ProcessOptions{
		Agent:      AgentCursor,
		Phase:      PhasePostTool,
		Now:        func() time.Time { return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC) },
		Enrichment: &Enrichment{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected one entry (event must not be dropped), got %d; warnings=%v", len(result.Entries), result.Warnings)
	}
	if got := strings.TrimSpace(string(result.Entries[0].ToolOutput)); got != "null" {
		t.Fatalf("expected explicit null output, got %q", got)
	}
}

func TestProcessCursorAcceptsUTF8BOMFractionalDurationAndWindowsWorkspaceRoot(t *testing.T) {
	payload := append([]byte{0xef, 0xbb, 0xbf}, []byte(`{
		"conversation_id": "6f98c956-cdf7-4bca-a2ec-a1386d384664",
		"generation_id": "841cde29-3bbc-4611-9b2d-2d68eb2889d8",
		"model": "composer-2.5",
		"tool_name": "Shell",
		"tool_input": {
			"command": "echo hello world",
			"cwd": "",
			"timeout": 30000
		},
		"tool_output": "{\"output\":\"\",\"exitCode\":0}",
		"duration": 172.791,
		"tool_use_id": "37f98a58-20f1-422d-872f-f96613580747",
		"cwd": "",
		"session_id": "6f98c956-cdf7-4bca-a2ec-a1386d384664",
		"hook_event_name": "postToolUse",
		"cursor_version": "3.11.13",
		"workspace_roots": [
			"/C:/Users/grant/devel/workspace/obocop"
		],
		"user_email": "user@example.com",
		"transcript_path": null
	}`)...)
	result, err := Process(payload, ProcessOptions{
		Agent:      AgentCursor,
		Phase:      PhasePostTool,
		Now:        fixedNow,
		Enrichment: &Enrichment{CWD: "/fallback"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 0 || len(result.Entries) != 1 {
		t.Fatalf("expected one entry without warnings, got entries=%d warnings=%v", len(result.Entries), result.Warnings)
	}
	entry := result.Entries[0]
	if entry.DurationMs != 173 {
		t.Fatalf("expected fractional duration to round to 173ms, got %d", entry.DurationMs)
	}
	if entry.CWD != "/C:/Users/grant/devel/workspace/obocop" {
		t.Fatalf("expected workspace root as cwd, got %q", entry.CWD)
	}
	if entry.SessionID != "6f98c956-cdf7-4bca-a2ec-a1386d384664" {
		t.Fatalf("expected conversation ID as session ID, got %q", entry.SessionID)
	}
	if bytes.HasPrefix(entry.RawHookPayload, []byte{0xef, 0xbb, 0xbf}) || !json.Valid(entry.RawHookPayload) {
		t.Fatalf("expected raw hook payload to be valid JSON without a BOM, got %q", entry.RawHookPayload)
	}
	var output map[string]any
	if err := json.Unmarshal(entry.ToolOutput, &output); err != nil {
		t.Fatalf("expected decoded tool output, got %s: %v", entry.ToolOutput, err)
	}
	if output["output"] != "" || output["exitCode"] != float64(0) {
		t.Fatalf("unexpected tool output: %#v", output)
	}
}

func TestProcessInvalidPayloadWarningIncludesDecoderError(t *testing.T) {
	result, err := Process([]byte(`{
		"tool_name": "Shell",
		"tool_input": {"command": "true"},
		"duration": "not-a-number"
	}`), ProcessOptions{
		Agent:      AgentCursor,
		Phase:      PhasePostTool,
		Enrichment: &Enrichment{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("expected one warning and no entries, got %#v", result)
	}
	if warning := result.Warnings[0]; !strings.Contains(warning, "invalid terminal hook payload: json: cannot unmarshal string") {
		t.Fatalf("expected decoder detail in warning, got %q", warning)
	}
}

func TestProcessVSCodeUsesPayloadTimestamp(t *testing.T) {
	payload := []byte(`{
		"timestamp": "2026-07-07T13:14:15Z",
		"session_id": "session-vs",
		"tool_use_id": "tool-vs",
		"tool_name": "editFiles",
		"tool_input": {"files": ["main.go"]},
		"tool_response": "ok"
	}`)
	result, err := Process(payload, ProcessOptions{
		Agent:      AgentVSCode,
		Phase:      PhasePostTool,
		Enrichment: &Enrichment{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(result.Entries))
	}
	if got := result.Entries[0].OccurredAt.Format(time.RFC3339); got != "2026-07-07T13:14:15Z" {
		t.Fatalf("expected payload timestamp, got %s", got)
	}
	if result.Entries[0].ToolKind != "write" {
		t.Fatalf("expected write tool kind, got %q", result.Entries[0].ToolKind)
	}
}

func TestProcessClaudePostToolUseNormalizesSingleToolEvent(t *testing.T) {
	payload := []byte(`{
		"session_id": "session-c",
		"tool_use_id": "good",
		"tool_name": "Read",
		"tool_input": {"file": "a.txt"},
		"tool_response": {"content": "hello"}
	}`)
	result, err := Process(payload, ProcessOptions{
		Agent:      AgentClaudeCode,
		Phase:      PhasePostTool,
		Now:        fixedNow,
		Enrichment: &Enrichment{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected one normalized entry, got %d", len(result.Entries))
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", result.Warnings)
	}
	if result.Entries[0].SessionID != "session-c" || result.Entries[0].ToolUseID != "good" {
		t.Fatalf("common/native fields were not normalized: %#v", result.Entries[0])
	}
}

func TestProcessFailureHooks(t *testing.T) {
	claudePayload := []byte(`{
		"session_id": "session-claude",
		"transcript_path": "/tmp/claude.jsonl",
		"cwd": "/project",
		"permission_mode": "default",
		"hook_event_name": "PostToolUseFailure",
		"tool_name": "Bash",
		"tool_input": {
			"command": "npm test",
			"description": "Run test suite"
		},
		"tool_use_id": "toolu_01ABC123",
		"error": "Command exited with non-zero status code 1",
		"is_interrupt": false,
		"duration_ms": 4187
	}`)
	result, err := Process(claudePayload, ProcessOptions{
		Agent:      AgentClaudeCode,
		Phase:      PhaseFailure,
		Now:        fixedNow,
		Enrichment: &Enrichment{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected one claude failure entry, got %d", len(result.Entries))
	}
	if got := result.Entries[0]; got.Status != StatusFailure || got.Error == "" || got.DurationMs != 4187 || got.PermissionMode != "default" {
		t.Fatalf("claude failure not normalized correctly: %#v", got)
	}

	cursorPayload := []byte(`{
		"conversation_id": "conversation-1",
		"generation_id": "generation-1",
		"tool_use_id": "tool-2",
		"tool_name": "Bash",
		"tool_input": {"command": "rm -rf /tmp/nope"},
		"error_message": "permission denied",
		"failure_type": "permission_denied",
		"duration": 42,
		"model": "cursor-model",
		"model_id": "cursor-model-id",
		"user_email": "user@example.com",
		"cursor_version": "1.2.3"
	}`)
	result, err = Process(cursorPayload, ProcessOptions{
		Agent:      AgentCursor,
		Phase:      PhaseFailure,
		Now:        fixedNow,
		Enrichment: &Enrichment{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected one cursor failure entry, got %d", len(result.Entries))
	}
	entry := result.Entries[0]
	if entry.Status != StatusDenied || entry.FailureType != "permission_denied" || entry.ToolOutput == nil {
		t.Fatalf("cursor failure not normalized correctly: %#v", entry)
	}
	if string(entry.ToolOutput) != "null" || entry.DurationMs != 42 {
		t.Fatalf("expected null output and duration, got output=%s duration=%d", entry.ToolOutput, entry.DurationMs)
	}

	codexPayload := []byte(`{"tool_name":"Bash","tool_input":{"command":"false"}}`)
	result, err = Process(codexPayload, ProcessOptions{
		Agent:      AgentCodex,
		Phase:      PhaseFailure,
		Enrichment: &Enrichment{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("expected unsupported codex failure to fail open with one warning, got %#v", result)
	}
}

func TestClassifyToolRecognizesAgentMCPPrefixes(t *testing.T) {
	tests := []struct {
		name       string
		agent      Agent
		toolName   string
		wantServer string
		wantTool   string
	}{
		{
			name:       "claude code extracts documented server segment",
			agent:      AgentClaudeCode,
			toolName:   "mcp__github__search",
			wantServer: "github",
			wantTool:   "search",
		},
		{
			name:       "codex extracts documented server segment",
			agent:      AgentCodex,
			toolName:   "mcp__github__search",
			wantServer: "github",
			wantTool:   "search",
		},
		{
			name:     "cursor generic tool hook",
			agent:    AgentCursor,
			toolName: "MCP:generate_bar_chart",
			wantTool: "generate_bar_chart",
		},
		{
			name:     "cursor never infers a server hint",
			agent:    AgentCursor,
			toolName: "mcp__server__tool",
			wantTool: "tool",
		},
		{
			name:     "vscode omits unreliable server hint",
			agent:    AgentVSCode,
			toolName: "mcp_mcp-server-ch_generate_bar_chart",
			wantTool: "generate_bar_chart",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, server, tool := classifyTool(tt.agent, tt.toolName)
			if kind != "mcp" || server != tt.wantServer || tool != tt.wantTool {
				t.Fatalf("unexpected classification for %q: kind=%q server=%q tool=%q", tt.toolName, kind, server, tool)
			}
		})
	}
}

func TestProcessRejectsUnsupportedAgentAndPhase(t *testing.T) {
	if _, err := Process([]byte(`{}`), ProcessOptions{Agent: Agent("bad"), Phase: PhasePostTool}); err != ErrUnsupportedAgent {
		t.Fatalf("expected ErrUnsupportedAgent, got %v", err)
	}
	if _, err := Process([]byte(`{}`), ProcessOptions{Agent: AgentCodex, Phase: Phase("permission")}); err != ErrUnsupportedPhase {
		t.Fatalf("expected ErrUnsupportedPhase, got %v", err)
	}
	if _, err := Process([]byte(`{}`), ProcessOptions{Agent: AgentCodex, Phase: Phase("pre-tool")}); err != ErrUnsupportedPhase {
		t.Fatalf("expected pre-tool to be unsupported, got %v", err)
	}
}

func TestCursorResultJSONParsesJSONStringPayload(t *testing.T) {
	payload := []byte(`{
		"conversation_id": "conversation-1",
		"tool_use_id": "tool-1",
		"tool_name": "MCP:tool",
		"tool_input": {"x": 1},
		"result_json": "{\"ok\":true}"
	}`)
	result, err := Process(payload, ProcessOptions{
		Agent:      AgentCursor,
		Phase:      PhasePostTool,
		Now:        fixedNow,
		Enrichment: &Enrichment{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]bool
	if err := json.Unmarshal(result.Entries[0].ToolOutput, &output); err != nil {
		t.Fatalf("expected parsed JSON object output, got %s: %v", result.Entries[0].ToolOutput, err)
	}
	if entry := result.Entries[0]; entry.MCPServerHint != "" || entry.MCPToolName != "tool" {
		t.Fatalf("unexpected Cursor MCP classification: %#v", entry)
	}
	if !output["ok"] {
		t.Fatalf("unexpected output: %#v", output)
	}
}

func TestCursorToolOutputParsesJSONStringPayload(t *testing.T) {
	payload := []byte(`{
		"conversation_id": "conversation-1",
		"tool_use_id": "tool-1",
		"tool_name": "Shell",
		"tool_input": {"command": "npm test"},
		"tool_output": "{\"exitCode\":0,\"stdout\":\"All tests passed\"}"
	}`)
	result, err := Process(payload, ProcessOptions{
		Agent:      AgentCursor,
		Phase:      PhasePostTool,
		Now:        fixedNow,
		Enrichment: &Enrichment{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(result.Entries[0].ToolOutput, &output); err != nil {
		t.Fatalf("expected parsed JSON object output, got %s: %v", result.Entries[0].ToolOutput, err)
	}
	if output["stdout"] != "All tests passed" {
		t.Fatalf("unexpected output: %#v", output)
	}
}

func fixedNow() time.Time {
	return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
}
