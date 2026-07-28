package audit

import "testing"

func TestClassifyTool(t *testing.T) {
	cases := []struct {
		agent   Agent
		name    string
		kind    string
		server  string
		mcpTool string
	}{
		// mcp__<server>__<tool>: only Claude Code and Codex yield a server hint.
		{AgentClaudeCode, "mcp__linear__search_issues", "mcp", "linear", "search_issues"},
		{AgentCodex, "mcp__linear__search_issues", "mcp", "linear", "search_issues"},
		{AgentVSCode, "mcp__linear__search_issues", "mcp", "", "search_issues"},
		{AgentCursor, "mcp__linear__search_issues", "mcp", "", "search_issues"},

		// Claude Code's plugin and account-connector namespaces are ordinary
		// server hints as far as audit is concerned.
		{AgentClaudeCode, "mcp__plugin_myplugin_linear__search", "mcp", "plugin_myplugin_linear", "search"},
		{AgentClaudeCode, "mcp__claude_ai_Linear__search", "mcp", "claude_ai_Linear", "search"},

		// An MCP-prefixed name with no tool half keeps the kind and drops both hints.
		{AgentClaudeCode, "mcp__linear", "mcp", "", ""},
		{AgentClaudeCode, "mcp_linear", "mcp", "", ""},

		// The other two namespacing conventions never carry a server hint.
		{AgentCursor, "MCP:search_issues", "mcp", "", "search_issues"},
		{AgentClaudeCode, "mcp_linear_search", "mcp", "", "search"},

		// Built-in tools.
		{AgentClaudeCode, "Bash", "shell", "", ""},
		{AgentVSCode, "run_in_terminal", "shell", "", ""},
		{AgentClaudeCode, "Read", "read", "", ""},
		{AgentClaudeCode, "Edit", "write", "", ""},
		{AgentClaudeCode, "Write", "write", "", ""},
		{AgentClaudeCode, "Task", "task", "", ""},
		{AgentClaudeCode, "Grep", "generic", "", ""},
		{AgentCursor, "Delete", "generic", "", ""},
	}

	for _, tc := range cases {
		kind, server, tool := classifyTool(tc.agent, tc.name)
		if kind != tc.kind || server != tc.server || tool != tc.mcpTool {
			t.Errorf("classifyTool(%s, %q) = (%q, %q, %q), want (%q, %q, %q)",
				tc.agent, tc.name, kind, server, tool, tc.kind, tc.server, tc.mcpTool)
		}
	}
}
