package enforce

import (
	"fmt"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

// Event is an agent's own native pre-tool hook event name. Using the agent's
// spelling keeps an installed hook line self-documenting and lets one command
// serve Cursor's two events.
//
// Note that EventPreToolUse and EventCursorPreToolUse differ only in case, so
// event parsing is case-sensitive and agent-scoped.
type Event string

const (
	EventPreToolUse               Event = "PreToolUse"
	EventCursorBeforeMCPExecution Event = "beforeMCPExecution"
	EventCursorPreToolUse         Event = "preToolUse"
	// EventOpenCodePermission is OpenCode V2's own permission.evaluate event.
	// It is not a native hook event: no managed hook install writes it. The
	// external OpenCode plugin calls the enforce command with it directly.
	EventOpenCodePermission Event = "permission.evaluate"
)

const (
	wireAgentClaudeCode = "claude_code"
	wireAgentCodex      = "codex"
	wireAgentCursor     = "cursor"
	wireAgentWorkBuddy  = "workbuddy"
	wireAgentZCode      = "zcode"
	wireAgentOpenCode   = "opencode"
)

// ParseAgent maps a CLI --agent value to a supported agent.
func ParseAgent(value string) (localagent.Agent, error) {
	switch localagent.Agent(value) {
	case localagent.ClaudeCode:
		return localagent.ClaudeCode, nil
	case localagent.Codex:
		return localagent.Codex, nil
	case localagent.Cursor:
		return localagent.Cursor, nil
	case localagent.OpenCode:
		return localagent.OpenCode, nil
	case localagent.WorkBuddy:
		return localagent.WorkBuddy, nil
	case localagent.ZCode:
		return localagent.ZCode, nil
	default:
		return "", fmt.Errorf("unsupported enforcement agent %q", value)
	}
}

// ParseEvent maps a CLI --event value to one of agent's own pre-tool events.
// Events are not interchangeable across agents: Cursor's preToolUse is a
// different event from the PreToolUse the other supported agents fire.
func ParseEvent(agent localagent.Agent, value string) (Event, error) {
	switch agent {
	case localagent.ClaudeCode, localagent.Codex, localagent.WorkBuddy, localagent.ZCode:
		if Event(value) == EventPreToolUse {
			return EventPreToolUse, nil
		}
	case localagent.OpenCode:
		if Event(value) == EventOpenCodePermission {
			return EventOpenCodePermission, nil
		}
	case localagent.Cursor:
		switch Event(value) {
		case EventCursorBeforeMCPExecution:
			return EventCursorBeforeMCPExecution, nil
		case EventCursorPreToolUse:
			return EventCursorPreToolUse, nil
		}
	}
	return "", fmt.Errorf("unsupported %s enforcement event %q", agent, value)
}

// wireAgent returns the agent value the decision endpoint expects.
func wireAgent(agent localagent.Agent) string {
	switch agent {
	case localagent.ClaudeCode:
		return wireAgentClaudeCode
	case localagent.Codex:
		return wireAgentCodex
	case localagent.Cursor:
		return wireAgentCursor
	case localagent.OpenCode:
		return wireAgentOpenCode
	case localagent.WorkBuddy:
		return wireAgentWorkBuddy
	case localagent.ZCode:
		return wireAgentZCode
	default:
		return ""
	}
}

// Events returns the pre-tool events an agent fires, in the order a hook
// configuration should list them. OpenCode and ZCode are external-plugin
// agents: their events are returned for protocol validation, but neither is
// included in localagent.All(), so managed hook installation never consumes
// these entries.
func Events(agent localagent.Agent) []Event {
	switch agent {
	case localagent.ClaudeCode, localagent.Codex, localagent.WorkBuddy:
		return []Event{EventPreToolUse}
	case localagent.Cursor:
		return []Event{EventCursorBeforeMCPExecution, EventCursorPreToolUse}
	case localagent.OpenCode:
		return []Event{EventOpenCodePermission}
	case localagent.ZCode:
		return []Event{EventPreToolUse}
	default:
		return nil
	}
}
