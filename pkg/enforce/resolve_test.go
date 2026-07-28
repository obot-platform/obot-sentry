package enforce

import (
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
	"github.com/obot-platform/obot/apiclient/types"
)

// TestResolutionString covers the human-readable rendering of each identity form,
// which the resolve diagnostic prints and the deny copy names.
func TestResolutionString(t *testing.T) {
	cases := []struct {
		name string
		res  Resolution
		want string
	}{
		{"url", Resolution{Identity: types.EnforcementDecisionServer{URL: "https://mcp.linear.app/sse"}}, "https://mcp.linear.app/sse"},
		{
			name: "package with a version",
			res:  Resolution{Identity: types.EnforcementDecisionServer{Package: &types.AllowlistServerPackage{Source: "npm", Name: "linear-mcp", Version: "1.2.3"}, Command: "npx"}},
			want: "npm / linear-mcp / 1.2.3",
		},
		{
			name: "package with no version pin",
			res:  Resolution{Identity: types.EnforcementDecisionServer{Package: &types.AllowlistServerPackage{Source: "pypi", Name: "mcp-server-git"}, Command: "uvx"}},
			want: "pypi / mcp-server-git / any version",
		},
		{"connector", Resolution{Identity: types.EnforcementDecisionServer{Connector: "claude.ai Linear"}}, "connector claude.ai Linear"},
		{"unresolved runner", Resolution{Identity: types.EnforcementDecisionServer{Command: "node"}}, "node"},
		{"built-in server", Resolution{ServerName: "Claude_Preview"}, ""},
	}

	for _, tc := range cases {
		if got := tc.res.String(); got != tc.want {
			t.Errorf("%s: String() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestBuiltinNamesMatchTheNamespaceForm(t *testing.T) {
	for _, name := range []string{
		"Claude_Preview", // "Claude Preview", space folded
		"Claude_Browser", // "Claude Browser", space folded
		"claude-in-chrome",
		"computer-use",
		"workspace",
	} {
		if !isBuiltinAgentMCP(localagent.ClaudeCode, name) {
			t.Errorf("isBuiltinAgentMCP(claude-code, %q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"", "linear", "claude", "preview", "workspaces",
		"claude_preview",   // lowercased: Claude Code preserves case
		"ClaudePreview",    // separator deleted: Claude Code folds, never deletes
		"claudeinchrome",   // same
		"claude_in_chrome", // hyphens are legal, so they survive rather than folding
	} {
		if isBuiltinAgentMCP(localagent.ClaudeCode, name) {
			t.Errorf("isBuiltinAgentMCP(claude-code, %q) = true, want false", name)
		}
	}
}

func TestBuiltinNamesUseTheAgentsOwnForm(t *testing.T) {
	for _, name := range []string{"Claude_Preview", "workspace", "computer-use"} {
		if isBuiltinAgentMCP(localagent.Codex, name) {
			t.Errorf("isBuiltinAgentMCP(codex, %q) = true, want false", name)
		}
	}
}
