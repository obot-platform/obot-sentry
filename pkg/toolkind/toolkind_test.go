package toolkind

import "testing"

func TestKind(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		// The three MCP namespacing conventions.
		{"mcp__linear__search_issues", KindMCP},
		{"mcp__github__create_pull_request", KindMCP},
		{"MCP__Linear__Search", KindMCP},
		{"mcp__linear", KindMCP}, // no tool half: still an MCP call
		{"MCP:search_issues", KindMCP},
		{"mcp:search_issues", KindMCP},
		{"mcp_linear_search", KindMCP},
		{"mcp_linear", KindMCP},

		// Built-in tools, per agent.
		{"Bash", KindShell},
		{"Shell", KindShell},
		{"run_in_terminal", KindShell},
		{"runInTerminalShell", KindShell},
		{"Read", KindRead},
		{"read_file", KindRead},
		{"Write", KindWrite},
		{"Edit", KindWrite},
		{"MultiEdit", KindWrite},
		{"NotebookEdit", KindWrite},
		{"Task", KindTask},
		{"TodoWrite", KindWrite}, // "write" wins: it is checked before "task"

		// Everything unrecognized. Grep and Delete land here rather than on
		// read/write, which is why Cursor's preToolUse classifier uses an exact
		// map instead of these heuristics.
		{"Grep", KindGeneric},
		{"Delete", KindGeneric},
		{"Glob", KindGeneric},
		{"WebFetch", KindGeneric},
		{"", KindGeneric},
	}

	for _, tc := range cases {
		if got := Kind(tc.name); got != tc.want {
			t.Errorf("Kind(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestKindShellBeatsRead pins the switch order: a name containing both a shell
// and a read signal classifies as shell, because shell is checked first.
func TestKindShellBeatsRead(t *testing.T) {
	if got := Kind("read_from_shell"); got != KindShell {
		t.Fatalf("Kind = %q, want %q", got, KindShell)
	}
}
