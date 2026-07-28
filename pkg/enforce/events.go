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
)

const (
	wireAgentClaudeCode = "claude_code"
	wireAgentCodex      = "codex"
	wireAgentCursor     = "cursor"
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
	default:
		return "", fmt.Errorf("unsupported enforcement agent %q", value)
	}
}

// ParseEvent maps a CLI --event value to one of agent's own pre-tool events.
// Events are not interchangeable across agents: Cursor's preToolUse is a
// different event from the PreToolUse the other two fire.
func ParseEvent(agent localagent.Agent, value string) (Event, error) {
	switch agent {
	case localagent.ClaudeCode, localagent.Codex:
		if Event(value) == EventPreToolUse {
			return EventPreToolUse, nil
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
	default:
		return ""
	}
}

// Events returns the pre-tool events agent fires, in the order a hook
// configuration should list them. An agent that enforcement does not support
// fires none, so that installing enforcement hooks cannot write an entry for one.
func Events(agent localagent.Agent) []Event {
	switch agent {
	case localagent.ClaudeCode, localagent.Codex:
		return []Event{EventPreToolUse}
	case localagent.Cursor:
		return []Event{EventCursorBeforeMCPExecution, EventCursorPreToolUse}
	default:
		return nil
	}
}
