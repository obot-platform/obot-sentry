package localagent

// Agent identifies a supported local coding agent. The values are the ones the
// CLI accepts for --agent, and they are what appear in an installed hook line.
//
// The wire values the Obot API expects are not these: see pkg/enforce, which maps
// claude-code to claude_code.
type Agent string

const (
	ClaudeCode Agent = "claude-code"
	Codex      Agent = "codex"
	VSCode     Agent = "vscode"
	Cursor     Agent = "cursor"
	WorkBuddy  Agent = "workbuddy"
	ZCode      Agent = "zcode"
	// OpenCode is submitted by its external plugin rather than by a managed
	// hook-install configuration, so it is intentionally not included in All.
	OpenCode Agent = "opencode"
)

// All returns the fixed, ordered set of agents whose hooks obot-sentry installs
// directly. OpenCode and ZCode are excluded because their enforcement is delivered
// by external plugins rather than native client configuration files.
func All() []Agent {
	return []Agent{ClaudeCode, Codex, VSCode, Cursor, WorkBuddy}
}

// DisplayName is the human-readable agent name used in operator-facing output.
func (a Agent) DisplayName() string {
	switch a {
	case ClaudeCode:
		return "Claude Code"
	case Codex:
		return "Codex"
	case VSCode:
		return "Visual Studio Code"
	case Cursor:
		return "Cursor"
	case WorkBuddy:
		return "WorkBuddy"
	case ZCode:
		return "ZCode"
	case OpenCode:
		return "OpenCode"
	default:
		return string(a)
	}
}
