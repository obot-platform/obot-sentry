package audit

import (
	"encoding/json"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

// The payloads below keep the field sets the agents were observed sending,
// with the values shortened.

func TestProcessVSCodeCopilotHarnessReadsToolResult(t *testing.T) {
	// VS Code's Copilot harness (shared with Copilot CLI) reports output as
	// tool_result and sends no tool_response.
	payload := []byte(`{
		"hook_event_name": "PostToolUse",
		"session_id": "session-vs",
		"timestamp": "2026-11-12T15:43:26.359Z",
		"cwd": "/work/repo",
		"tool_use_id": "tool-vs",
		"tool_name": "Bash",
		"tool_input": {"command": "echo hi"},
		"tool_result": {"result_type": "success", "text_result_for_llm": "hi"}
	}`)
	result, err := Process(payload, ProcessOptions{
		Agent:      localagent.VSCode,
		Phase:      PhasePostTool,
		Now:        fixedNow,
		Enrichment: &Enrichment{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected one entry, got %d; warnings=%v", len(result.Entries), result.Warnings)
	}
	entry := result.Entries[0]

	var output struct {
		ResultType string `json:"result_type"`
		Text       string `json:"text_result_for_llm"`
	}
	if err := json.Unmarshal(entry.ToolOutput, &output); err != nil {
		t.Fatalf("expected tool_result as the output, got %s: %v", entry.ToolOutput, err)
	}
	if output.ResultType != "success" || output.Text != "hi" {
		t.Fatalf("unexpected output: %s", entry.ToolOutput)
	}

	raw := decodeRawPayload(t, entry.RawHookPayload)
	assertOmitted(t, raw, "tool_result", rawOmittedToolOutput)
}

func TestProcessPrefersToolResponseOverToolResult(t *testing.T) {
	payload := []byte(`{
		"session_id": "session-1",
		"tool_use_id": "tool-1",
		"tool_name": "Bash",
		"tool_input": {"command": "echo hi"},
		"tool_response": "from tool_response",
		"tool_result": {"result_type": "success", "text_result_for_llm": "from tool_result"}
	}`)
	result, err := Process(payload, ProcessOptions{
		Agent:      localagent.VSCode,
		Phase:      PhasePostTool,
		Now:        fixedNow,
		Enrichment: &Enrichment{},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := result.Entries[0]
	if got := string(entry.ToolOutput); got != `"from tool_response"` {
		t.Fatalf("expected tool_response to win, got %s", got)
	}

	// Only the key the output was read from is replaced; the other one stays.
	raw := decodeRawPayload(t, entry.RawHookPayload)
	assertOmitted(t, raw, "tool_response", rawOmittedToolOutput)
	assertJSONEqual(t, raw["tool_result"], `{"result_type": "success", "text_result_for_llm": "from tool_result"}`)
}

func TestProcessRawPayloadOmitsExtractedInputAndOutput(t *testing.T) {
	tests := []struct {
		name      string
		agent     localagent.Agent
		phase     Phase
		payload   string
		outputKey string
		wantKept  map[string]string
	}{
		{
			name:  "claude code post tool",
			agent: localagent.ClaudeCode,
			phase: PhasePostTool,
			payload: `{
				"session_id": "session-1",
				"transcript_path": "/tmp/transcript.jsonl",
				"cwd": "/work/repo",
				"prompt_id": "prompt-1",
				"permission_mode": "acceptEdits",
				"effort": {"level": "medium"},
				"hook_event_name": "PostToolUse",
				"tool_name": "mcp__everything__echo",
				"tool_input": {"message": "ping"},
				"tool_response": [{"type": "text", "text": "Echo: ping"}],
				"tool_use_id": "toolu_1",
				"duration_ms": 11,
				"mcp_server": {"name": "everything", "source": "dynamic"}
			}`,
			outputKey: "tool_response",
			wantKept: map[string]string{
				"hook_event_name": `"PostToolUse"`,
				"prompt_id":       `"prompt-1"`,
				"effort":          `{"level": "medium"}`,
				"duration_ms":     `11`,
				"mcp_server":      `{"name": "everything", "source": "dynamic"}`,
				"tool_name":       `"mcp__everything__echo"`,
			},
		},
		{
			name:  "claude code failure has no output to omit",
			agent: localagent.ClaudeCode,
			phase: PhaseFailure,
			payload: `{
				"session_id": "session-1",
				"hook_event_name": "PostToolUseFailure",
				"tool_name": "Bash",
				"tool_input": {"command": "cat /nonexistent/file"},
				"tool_use_id": "toolu_2",
				"error": "Exit code 1",
				"is_interrupt": false,
				"duration_ms": 7
			}`,
			wantKept: map[string]string{
				"hook_event_name": `"PostToolUseFailure"`,
				"error":           `"Exit code 1"`,
				"is_interrupt":    `false`,
			},
		},
		{
			name:  "codex post tool",
			agent: localagent.Codex,
			phase: PhasePostTool,
			payload: `{
				"session_id": "session-1",
				"turn_id": "turn-1",
				"transcript_path": "/tmp/rollout.jsonl",
				"cwd": "/work/repo",
				"hook_event_name": "PostToolUse",
				"model": "gpt-5",
				"permission_mode": "default",
				"tool_name": "Bash",
				"tool_input": {"command": "echo hi"},
				"tool_response": "hi\n",
				"tool_use_id": "exec-1"
			}`,
			outputKey: "tool_response",
			wantKept: map[string]string{
				"hook_event_name": `"PostToolUse"`,
				"turn_id":         `"turn-1"`,
				"model":           `"gpt-5"`,
			},
		},
		{
			name:  "cursor post tool",
			agent: localagent.Cursor,
			phase: PhasePostTool,
			payload: `{
				"conversation_id": "conversation-1",
				"generation_id": "generation-1",
				"model": "default",
				"tool_name": "Shell",
				"tool_input": {"command": "echo hi", "cwd": "", "timeout": 30000},
				"tool_output": "{\"output\":\"hi\\n\",\"exitCode\":0}",
				"duration": 554.376,
				"tool_use_id": "tool-1",
				"cwd": "",
				"session_id": "conversation-1",
				"hook_event_name": "postToolUse",
				"cursor_version": "2026.11.11",
				"workspace_roots": ["/work/repo"],
				"user_email": "user@example.com",
				"transcript_path": "/tmp/transcript.jsonl"
			}`,
			outputKey: "tool_output",
			wantKept: map[string]string{
				"hook_event_name": `"postToolUse"`,
				"session_id":      `"conversation-1"`,
				"duration":        `554.376`,
				"workspace_roots": `["/work/repo"]`,
			},
		},
		{
			name:  "output in a field the parser does not read stays in the raw payload",
			agent: localagent.VSCode,
			phase: PhasePostTool,
			payload: `{
				"session_id": "session-1",
				"tool_use_id": "tool-1",
				"tool_name": "Bash",
				"tool_input": {"command": "echo hi"},
				"some_future_output": {"text": "hi"}
			}`,
			wantKept: map[string]string{
				"some_future_output": `{"text": "hi"}`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Process([]byte(tt.payload), ProcessOptions{
				Agent:      tt.agent,
				Phase:      tt.phase,
				Now:        fixedNow,
				Enrichment: &Enrichment{},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Entries) != 1 {
				t.Fatalf("expected one entry, got %d; warnings=%v", len(result.Entries), result.Warnings)
			}
			entry := result.Entries[0]

			original := decodeRawPayload(t, []byte(tt.payload))
			raw := decodeRawPayload(t, entry.RawHookPayload)
			if len(raw) != len(original) {
				t.Fatalf("raw payload should keep every top-level key: got %d keys, want %d", len(raw), len(original))
			}

			assertOmitted(t, raw, "tool_input", rawOmittedToolInput)
			if tt.outputKey != "" {
				assertOmitted(t, raw, tt.outputKey, rawOmittedToolOutput)
			}
			for key, want := range tt.wantKept {
				assertJSONEqual(t, raw[key], want)
			}

			// What was omitted is what the entry carries.
			assertJSONEqual(t, entry.ToolInput, string(original["tool_input"]))
		})
	}
}

func TestProcessIdempotencyKeyCoversOmittedOutput(t *testing.T) {
	// The key is derived from the payload as the agent sent it, so two calls
	// that differ only in output still get different keys after the output is
	// omitted from the raw payload.
	keyFor := func(output string) string {
		t.Helper()
		payload := []byte(`{
			"session_id": "session-1",
			"tool_use_id": "tool-1",
			"tool_name": "Bash",
			"tool_input": {"command": "date"},
			"tool_response": "` + output + `"
		}`)
		result, err := Process(payload, ProcessOptions{
			Agent:      localagent.ClaudeCode,
			Phase:      PhasePostTool,
			Now:        fixedNow,
			Enrichment: &Enrichment{},
		})
		if err != nil {
			t.Fatal(err)
		}
		return result.Entries[0].IdempotencyKey
	}
	if keyFor("one") == keyFor("two") {
		t.Fatal("expected different idempotency keys for different outputs")
	}
}

func TestSlimRawPayloadKeepsNonObjectPayload(t *testing.T) {
	for _, payload := range []string{`[1, 2]`, `"text"`, `null`, `not json`} {
		if got := string(slimRawPayload([]byte(payload), "tool_response")); got != payload {
			t.Fatalf("expected %q unchanged, got %q", payload, got)
		}
	}
}

func decodeRawPayload(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("raw payload is not a JSON object: %v: %s", err, raw)
	}
	return fields
}

func assertOmitted(t *testing.T, raw map[string]json.RawMessage, key, movedTo string) {
	t.Helper()
	assertJSONEqual(t, raw[key], `{"`+rawOmittedKey+`": "`+movedTo+`"}`)
}

func assertJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("got is not valid JSON: %v: %q", err, got)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("want is not valid JSON: %v: %q", err, want)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("JSON mismatch:\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}
