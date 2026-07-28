// Package hookinstall converges the native audit-hook configuration for the
// supported local coding agents (Claude Code, Codex, Visual Studio Code, and
// Cursor) onto the hidden `obot-sentry audit submit` command.
//
// The package reads as a pipeline, one stage per file, so the same primitives can
// back a future hook-status or hook-uninstall command:
//
//   - who and where we are: platform.go and its per-GOOS files resolve the
//     console user and the privilege to write for them,
//   - what should be on disk: destinations.go models every managed file,
//     command.go builds the hook command and recognizes our own marker in one,
//     and desired.go assembles each agent's desired document,
//   - how it gets there: converge.go merges desired state into existing state
//     through the editors in jsonconfig.go and tomlconfig.go, and configio.go
//     performs the symlink-safe read and atomic commit, and
//   - install.go drives all of it behind injectable seams.
//
// This file defines the vocabulary those stages share: the managed agents, the
// scope and format of a destination, and the per-destination convergence result.
package hookinstall

import "github.com/obot-platform/obot-sentry/pkg/localagent"

type Agent = localagent.Agent

const (
	AgentClaudeCode = localagent.ClaudeCode
	AgentCodex      = localagent.Codex
	AgentVSCode     = localagent.VSCode
	AgentCursor     = localagent.Cursor
)

// Agents returns the fixed, ordered set of agents hook-install manages.
func Agents() []Agent {
	return localagent.All()
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
