package audit

import (
	"encoding/json"
	"math"
)

type sharedToolHookCommon struct {
	SessionID      string `json:"session_id,omitempty"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	CWD            string `json:"cwd,omitempty"`
	HookEventName  string `json:"hook_event_name,omitempty"`
	Model          string `json:"model,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	GenerationID   string `json:"generation_id,omitempty"`
	Version        string `json:"version,omitempty"`
}

// sharedSingleToolHook is a superset of the similar-but-not-identical
// single-tool hook envelopes used by VS Code, Codex, Claude Code, WorkBuddy,
// OpenCode, and ZCode.
// The common core is tool_name/tool_input plus a tool-use or call ID and
// tool_response on post-success; product-specific fields such as VS Code
// timestamp, Codex turn_id/model, and WorkBuddy generation_id/version/error
// details remain optional.
type sharedSingleToolHook struct {
	sharedToolHookCommon

	TurnID        string          `json:"turn_id,omitempty"`
	ToolName      string          `json:"tool_name"`
	ToolUseID     string          `json:"tool_use_id,omitempty"`
	CallID        string          `json:"call_id,omitempty"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolResponse  json.RawMessage `json:"tool_response,omitempty"`
	DurationMs    int64           `json:"duration_ms,omitempty"`
	Error         string          `json:"error,omitempty"`
	ToolErrorCode string          `json:"tool_error_code,omitempty"`
	ToolErrorName string          `json:"tool_error_name,omitempty"`
	IsInterrupt   bool            `json:"is_interrupt,omitempty"`
	Timestamp     string          `json:"timestamp,omitempty"`
	AgentVersion  string          `json:"agent_version,omitempty"`
}

// cursorToolHook covers Cursor's generic terminal tool hooks: postToolUse and
// postToolUseFailure. Cursor uses conversation/generation IDs instead of
// session/turn IDs and uses tool_output/result_json for terminal output.
type cursorToolHook struct {
	ConversationID string `json:"conversation_id,omitempty"`
	GenerationID   string `json:"generation_id,omitempty"`
	Model          string `json:"model,omitempty"`
	ModelID        string `json:"model_id,omitempty"`
	CursorVersion  string `json:"cursor_version,omitempty"`
	UserEmail      string `json:"user_email,omitempty"`
	TranscriptPath string `json:"transcript_path,omitempty"`

	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolOutput     json.RawMessage `json:"tool_output,omitempty"`
	ResultJSON     json.RawMessage `json:"result_json,omitempty"`
	ToolResponse   json.RawMessage `json:"tool_response,omitempty"`
	ToolUseID      string          `json:"tool_use_id,omitempty"`
	CWD            string          `json:"cwd,omitempty"`
	WorkspaceRoots []string        `json:"workspace_roots,omitempty"`
	Duration       float64         `json:"duration,omitempty"`
	ErrorMessage   string          `json:"error_message,omitempty"`
	FailureType    string          `json:"failure_type,omitempty"`
	IsInterrupt    bool            `json:"is_interrupt,omitempty"`
	AgentMessage   string          `json:"agent_message,omitempty"`
}

// nativeEvent is the small adapter surface the normalizer needs after
// unmarshalling an agent-specific native hook payload.
type nativeEvent interface {
	toolName() string
	toolInput() json.RawMessage
	toolOutput(phase Phase) (json.RawMessage, bool)
	toolUseID() string
	sessionID() string
	turnID() string
	model() string
	modelID() string
	permissionMode() string
	reportedUserEmail() string
	cwd() string
	transcriptPath() string
	agentVersion() string
	durationMs() int64
	errorText() string
	failureType() string
	timestamp() string
}

func (e sharedSingleToolHook) toolName() string           { return e.ToolName }
func (e sharedSingleToolHook) toolInput() json.RawMessage { return e.ToolInput }
func (e sharedSingleToolHook) toolOutput(phase Phase) (json.RawMessage, bool) {
	// A terminal event with no reported output (a failure, or a success that
	// simply produced nothing) is still a completed tool call: submit an
	// explicit null rather than dropping the audit entry.
	if phase == PhaseFailure || len(e.ToolResponse) == 0 {
		return json.RawMessage("null"), true
	}
	return cloneRaw(e.ToolResponse), true
}
func (e sharedSingleToolHook) toolUseID() string         { return firstNonEmpty(e.ToolUseID, e.CallID) }
func (e sharedSingleToolHook) sessionID() string         { return e.SessionID }
func (e sharedSingleToolHook) turnID() string            { return firstNonEmpty(e.TurnID, e.GenerationID) }
func (e sharedSingleToolHook) model() string             { return e.Model }
func (e sharedSingleToolHook) modelID() string           { return "" }
func (e sharedSingleToolHook) permissionMode() string    { return e.PermissionMode }
func (e sharedSingleToolHook) reportedUserEmail() string { return "" }
func (e sharedSingleToolHook) cwd() string               { return e.CWD }
func (e sharedSingleToolHook) transcriptPath() string    { return e.TranscriptPath }
func (e sharedSingleToolHook) agentVersion() string      { return firstNonEmpty(e.AgentVersion, e.Version) }
func (e sharedSingleToolHook) durationMs() int64         { return e.DurationMs }
func (e sharedSingleToolHook) errorText() string         { return firstNonEmpty(e.Error, e.ToolErrorName) }
func (e sharedSingleToolHook) failureType() string {
	if code := firstNonEmpty(e.ToolErrorCode); code != "" {
		return code
	}
	if e.IsInterrupt {
		return "interrupt"
	}
	return ""
}
func (e sharedSingleToolHook) timestamp() string { return e.Timestamp }

func (e cursorToolHook) toolName() string           { return e.ToolName }
func (e cursorToolHook) toolInput() json.RawMessage { return e.ToolInput }
func (e cursorToolHook) toolOutput(phase Phase) (json.RawMessage, bool) {
	if phase == PhaseFailure {
		return json.RawMessage("null"), true
	}
	for _, raw := range []json.RawMessage{e.ToolOutput, e.ResultJSON, e.ToolResponse} {
		if len(raw) == 0 {
			continue
		}
		return parseJSONString(raw), true
	}
	// A completed tool call that reported no output still gets an audit entry
	// with an explicit null output rather than being dropped.
	return json.RawMessage("null"), true
}
func (e cursorToolHook) toolUseID() string         { return e.ToolUseID }
func (e cursorToolHook) sessionID() string         { return e.ConversationID }
func (e cursorToolHook) turnID() string            { return e.GenerationID }
func (e cursorToolHook) model() string             { return e.Model }
func (e cursorToolHook) modelID() string           { return e.ModelID }
func (e cursorToolHook) permissionMode() string    { return "" }
func (e cursorToolHook) reportedUserEmail() string { return e.UserEmail }
func (e cursorToolHook) cwd() string {
	values := make([]string, 0, len(e.WorkspaceRoots)+1)
	values = append(values, e.CWD)
	values = append(values, e.WorkspaceRoots...)
	return firstNonEmpty(values...)
}
func (e cursorToolHook) transcriptPath() string { return e.TranscriptPath }
func (e cursorToolHook) agentVersion() string   { return e.CursorVersion }
func (e cursorToolHook) durationMs() int64      { return int64(math.Round(e.Duration)) }
func (e cursorToolHook) errorText() string      { return e.ErrorMessage }
func (e cursorToolHook) failureType() string    { return e.FailureType }
func (e cursorToolHook) timestamp() string      { return "" }
