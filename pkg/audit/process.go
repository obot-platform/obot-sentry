package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/obot-platform/obocop/pkg/version"
)

var (
	ErrUnsupportedAgent = errors.New("unsupported audit agent")
	ErrUnsupportedPhase = errors.New("unsupported audit phase")
)

func Process(payload []byte, opts ProcessOptions) (Result, error) {
	// Cursor on Windows can prefix hook stdin with a UTF-8 byte order mark.
	// encoding/json rejects a BOM, and retaining it in RawHookPayload would also
	// make the normalized manifest invalid JSON, so remove exactly one leading
	// marker before decoding or hashing the payload.
	payload = bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf})

	agent, err := normalizeAgent(opts.Agent)
	if err != nil {
		return Result{}, err
	}
	phase, err := normalizePhase(opts.Phase)
	if err != nil {
		return Result{}, err
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	if phase == PhaseFailure && (agent == AgentVSCode || agent == AgentCodex) {
		return Result{Warnings: []string{
			fmt.Sprintf("obocop audit: %s failure hooks are not supported; no audit entry submitted", agent),
		}}, nil
	}

	event, err := parseTerminalEvent(agent, payload)
	if err != nil {
		return Result{Warnings: []string{fmt.Sprintf("obocop audit: %v; no audit entry submitted", err)}}, nil
	}

	enrichment := Enrichment{}
	if opts.Enrichment != nil {
		enrichment = *opts.Enrichment
	} else if !opts.SkipLocal {
		enrichment = CollectEnrichment(event.cwd())
	}

	entry, err := normalizeEvent(agent, phase, event, payload, now, enrichment)
	if err != nil {
		return Result{Warnings: []string{fmt.Sprintf("obocop audit: %v", err)}}, nil
	}
	return Result{Entries: []Entry{entry}}, nil
}

func parseTerminalEvent(agent Agent, payload []byte) (nativeEvent, error) {
	switch agent {
	case AgentCursor:
		var event cursorToolHook
		if err := decodeJSON(payload, &event); err != nil {
			return nil, fmt.Errorf("invalid terminal hook payload: %w", err)
		}
		return event, nil
	default:
		var event sharedSingleToolHook
		if err := decodeJSON(payload, &event); err != nil {
			return nil, fmt.Errorf("invalid terminal hook payload: %w", err)
		}
		return event, nil
	}
}

func decodeJSON(payload []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	return dec.Decode(out)
}

func normalizeAgent(agent Agent) (Agent, error) {
	switch Agent(strings.TrimSpace(string(agent))) {
	case AgentClaudeCode:
		return AgentClaudeCode, nil
	case AgentCodex:
		return AgentCodex, nil
	case AgentVSCode:
		return AgentVSCode, nil
	case AgentCursor:
		return AgentCursor, nil
	default:
		return "", ErrUnsupportedAgent
	}
}

func normalizePhase(phase Phase) (Phase, error) {
	switch Phase(strings.TrimSpace(string(phase))) {
	case PhasePostTool:
		return PhasePostTool, nil
	case PhaseFailure:
		return PhaseFailure, nil
	default:
		return "", ErrUnsupportedPhase
	}
}

func normalizeEvent(agent Agent, phase Phase, event nativeEvent, payload []byte, now func() time.Time, enrichment Enrichment) (Entry, error) {
	toolName := strings.TrimSpace(event.toolName())
	if toolName == "" {
		return Entry{}, errors.New("missing tool_name")
	}
	input := event.toolInput()
	if len(input) == 0 {
		return Entry{}, fmt.Errorf("missing tool_input for %s", toolName)
	}

	status, failureType := statusFor(phase, event)
	output, ok := event.toolOutput(phase)
	if !ok {
		return Entry{}, fmt.Errorf("missing terminal tool output for %s", toolName)
	}

	occurredAt := occurredAt(event, now)
	cwd := firstNonEmpty(event.cwd(), enrichment.CWD)
	entry := Entry{
		AgentProvider: providerValue(agent),
		AgentVersion:  event.agentVersion(),
		CLIName:       "obocop",
		CLIVersion:    version.Get().String(),
		Status:        status,
		FailureType:   failureType,
		OccurredAt:    occurredAt,
		DurationMs:    event.durationMs(),
		Error:         event.errorText(),

		ToolUseID:  event.toolUseID(),
		SessionID:  event.sessionID(),
		TurnID:     event.turnID(),
		ToolName:   toolName,
		ToolInput:  cloneRaw(input),
		ToolOutput: cloneRaw(output),

		Model:             event.model(),
		ModelID:           event.modelID(),
		PermissionMode:    event.permissionMode(),
		ReportedUserEmail: event.reportedUserEmail(),
		CWD:               cwd,
		Hostname:          enrichment.Hostname,
		OS:                enrichment.OS,
		Arch:              enrichment.Arch,
		LocalUsername:     enrichment.LocalUsername,
		GitRepoRoot:       enrichment.GitRepoRoot,
		GitRemoteURLs:     enrichment.GitRemoteURLs,
		GitBranch:         enrichment.GitBranch,
		GitCommitSHA:      enrichment.GitCommitSHA,
		TranscriptPath:    event.transcriptPath(),
		RawHookPayload:    cloneRaw(payload),
	}

	entry.ToolKind, entry.MCPServerHint, entry.MCPToolName = classifyTool(agent, toolName)
	entry.IdempotencyKey = idempotencyKey(entry, payload)
	return entry, nil
}

func providerValue(agent Agent) string {
	if agent == AgentClaudeCode {
		return "claude_code"
	}
	return string(agent)
}

func statusFor(phase Phase, event nativeEvent) (Status, string) {
	if phase == PhasePostTool {
		return StatusSuccess, ""
	}

	failureType := event.failureType()
	switch failureType {
	case "permission_denied", "permission-denied", "denied":
		return StatusDenied, failureType
	case "timeout":
		return StatusTimeout, failureType
	default:
		return StatusFailure, failureType
	}
}

func parseJSONString(raw json.RawMessage) json.RawMessage {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return cloneRaw(raw)
	}
	var parsed json.RawMessage
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return cloneRaw(raw)
	}
	return parsed
}

func occurredAt(event nativeEvent, now func() time.Time) time.Time {
	if ts := event.timestamp(); ts != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			return parsed
		}
	}
	return now().UTC()
}

func classifyTool(agent Agent, name string) (kind, server, tool string) {
	lower := strings.ToLower(name)

	// MCP tool names are agent-specific. Claude Code and Codex document the
	// mcp__<server>__<tool> convention, so only those providers yield a server
	// hint. Cursor and VS Code do not expose a reliable server name in their
	// generic tool-hook names.
	if strings.HasPrefix(lower, "mcp__") {
		parts := strings.SplitN(name[len("mcp__"):], "__", 2)
		if len(parts) == 2 {
			if agent == AgentClaudeCode || agent == AgentCodex {
				return "mcp", parts[0], parts[1]
			}
			return "mcp", "", parts[1]
		}
		return "mcp", "", ""
	}
	if strings.HasPrefix(lower, "mcp:") {
		return "mcp", "", name[len("MCP:"):]
	}
	if strings.HasPrefix(lower, "mcp_") {
		parts := strings.SplitN(name[len("mcp_"):], "_", 2)
		if len(parts) == 2 {
			return "mcp", "", parts[1]
		}
		return "mcp", "", ""
	}
	switch {
	case lower == "bash" || lower == "shell" || strings.Contains(lower, "shell") || lower == "run_in_terminal":
		return "shell", "", ""
	case strings.Contains(lower, "read"):
		return "read", "", ""
	case strings.Contains(lower, "write") || strings.Contains(lower, "edit"):
		return "write", "", ""
	case strings.Contains(lower, "task"):
		return "task", "", ""
	default:
		return "generic", "", ""
	}
}

func idempotencyKey(entry Entry, payload []byte) string {
	h := sha256.New()
	writeHash := func(s string) {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	writeHash(entry.AgentProvider)
	writeHash(string(entry.Status))
	writeHash(entry.SessionID)
	writeHash(entry.TurnID)
	writeHash(entry.ToolUseID)
	writeHash(entry.ToolName)
	writeHash(entry.OccurredAt.Format(time.RFC3339Nano))
	_, _ = h.Write(payload)
	return "laal_" + hex.EncodeToString(h.Sum(nil))[:32]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cloneRaw(payload []byte) json.RawMessage {
	clone := make([]byte, len(payload))
	copy(clone, payload)
	return clone
}
