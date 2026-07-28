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
)

// All returns the fixed, ordered set of supported agents. Order is deterministic
// so preflight, plans, and summaries are stable across runs.
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
	default:
		return string(a)
	}
}
