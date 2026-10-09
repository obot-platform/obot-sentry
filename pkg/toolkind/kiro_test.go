package toolkind

import "testing"

func TestKiroKind(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		// Misread by Kind: no "shell" in the name.
		{"execute_bash", KindShell},
		{"execute_pwsh", KindShell},
		// Misread by Kind: no "read" or "write" in the name.
		{"grep_search", KindRead},
		{"str_replace", KindWrite},
		{"delete_file", KindWrite},
		{"invoke_sub_agent", KindTask},
		// Agreeing with Kind, but pinned.
		{"read_file", KindRead},
		{"fs_write", KindWrite},
		// MCP tools and unknown ids fall through to Kind.
		{"mcp_github_create_issue", KindMCP},
		{"web_fetch", KindGeneric},
		{"kiro_powers", KindGeneric},
		{"some_future_reader", KindRead},
	}
	for _, tc := range cases {
		if got := KiroKind(tc.name); got != tc.want {
			t.Errorf("KiroKind(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}
