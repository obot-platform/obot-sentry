// Package hookinstall converges the native audit-hook configuration for the
// supported local coding agents (Claude Code, Codex, Visual Studio Code, and
// Cursor) onto the hidden `obot-sentry audit submit` command.
//
// The package is split into independently testable seams so the same primitives
// can back a future hook-status/hook-uninstall command:
//
//   - platform discovery (privilege_*.go, install.go),
//   - command generation (command.go, clients.go),
//   - ownership recognition (ownership.go), and
//   - the CLI orchestration (install.go).
//
// This file defines the vocabulary shared by those seams: the managed agents,
// their destinations, and the per-destination convergence result.
package hookinstall

// Agent identifies a supported local coding agent. The string values match the
// providers accepted by `obot-sentry audit submit --agent`; a test asserts they stay
// in lockstep with pkg/audit's Agent constants.
type Agent string

const (
	AgentClaudeCode Agent = "claude-code"
	AgentCodex      Agent = "codex"
	AgentVSCode     Agent = "vscode"
	AgentCursor     Agent = "cursor"
)

// Agents returns the fixed, ordered set of agents hook-install manages. Order is
// deterministic so preflight, plans, and summaries are stable across runs.
func Agents() []Agent {
	return []Agent{AgentClaudeCode, AgentCodex, AgentVSCode, AgentCursor}
}

// DisplayName is the human-readable agent name used in operator-facing output.
func (a Agent) DisplayName() string {
	switch a {
	case AgentClaudeCode:
		return "Claude Code"
	case AgentCodex:
		return "Codex"
	case AgentVSCode:
		return "Visual Studio Code"
	case AgentCursor:
		return "Cursor"
	default:
		return string(a)
	}
}

// Scope distinguishes machine-wide destinations (one file for all users) from
// active-user destinations (one file under a single console user's home).
type Scope string

const (
	// ScopeMachine is a fixed, absolute path owned by administrators.
	ScopeMachine Scope = "machine"
	// ScopeUser is resolved against the active console user's home directory.
	ScopeUser Scope = "user"
)

// Format is the on-disk encoding of a destination file. It selects the merge
// engine used when the config editors land: JSON/JSONC files are edited with a
// comment-preserving AST, TOML with a decode/re-encode cycle.
type Format string

const (
	FormatJSON  Format = "json"  // strict JSON (Claude, Cursor, Copilot hook files)
	FormatJSONC Format = "jsonc" // JSON with comments/trailing commas (VS Code settings)
	FormatTOML  Format = "toml"  // Codex requirements.toml
)

// Status is the per-destination convergence outcome reported to the operator.
type Status string

const (
	// StatusInstalled means the destination had no managed hook and now does.
	StatusInstalled Status = "installed"
	// StatusUpdated means a managed hook existed and was replaced or deduplicated.
	StatusUpdated Status = "updated"
	// StatusUnchanged means the destination already held the desired state.
	StatusUnchanged Status = "unchanged"
	// StatusFailed means the destination could not be converged; see Err.
	StatusFailed Status = "failed"
)

// Result is the outcome for one destination. It intentionally carries only
// paths, counts, and status — never config contents or credentials — so it is
// safe to print in the command summary.
type Result struct {
	Agent Agent
	// Label names the destination for output, e.g. "Claude Code" or
	// "VS Code settings". It disambiguates the two vscode-related files.
	Label string
	Scope Scope
	// Path is the resolved absolute destination path, populated once path
	// resolution lands. It may be empty for a preflight-only plan.
	Path   string
	Status Status
	// DuplicatesRemoved counts owned entries collapsed during convergence.
	DuplicatesRemoved int
	// Err is set when Status is StatusFailed.
	Err error
}
