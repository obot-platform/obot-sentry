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

	// OpenCode is submitted by an external plugin rather than by a managed
	// hook-install configuration, so it is intentionally not in All(). Its
	// enforcement event is registered by the plugin through the enforce command.
	OpenCode Agent = "opencode"
)

// All returns the fixed, ordered set of supported agents. Order is deterministic
// so preflight, plans, and summaries are stable across runs.
//
// OpenCode is excluded: there is no native client configuration file to write,
// so including it would make hook installation report a destination that does
// not exist.
func All() []Agent {
	return []Agent{ClaudeCode, Codex, VSCode, Cursor}
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
	case OpenCode:
		return "OpenCode"
	default:
		return string(a)
	}
}
