// Package toolkind classifies a runtime tool name into one of the six kinds the
// Obot enforcement evaluator recognizes: mcp, shell, read, write, task, or
// generic.
package toolkind

import "strings"

// Kinds recognized by the evaluator. A call whose kind is anything other than
// KindMCP is gated behind the "allow all built-in agent tools" toggle.
const (
	KindMCP     = "mcp"
	KindShell   = "shell"
	KindRead    = "read"
	KindWrite   = "write"
	KindTask    = "task"
	KindGeneric = "generic"
)

// Kind classifies name. The MCP prefixes are the three namespacing conventions
// local agents use (mcp__server__tool, MCP:tool, mcp_server_tool); everything
// else falls through to substring heuristics over the built-in tool names.
func Kind(name string) string {
	lower := strings.ToLower(name)

	if strings.HasPrefix(lower, "mcp__") || strings.HasPrefix(lower, "mcp:") || strings.HasPrefix(lower, "mcp_") {
		return KindMCP
	}
	switch {
	case lower == "bash" || lower == "shell" || strings.Contains(lower, "shell") || lower == "run_in_terminal":
		return KindShell
	case strings.Contains(lower, "read"):
		return KindRead
	case strings.Contains(lower, "write") || strings.Contains(lower, "edit"):
		return KindWrite
	case strings.Contains(lower, "task"):
		return KindTask
	default:
		return KindGeneric
	}
}
