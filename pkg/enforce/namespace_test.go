package enforce

import "testing"

func TestFormClaudeCode(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain name is untouched", "linear", "linear"},
		{"hyphen is legal and survives", "claude-in-chrome", "claude-in-chrome"},
		{"underscore is legal and survives", "my_server", "my_server"},
		{"case is preserved", "MixedCase-Server", "MixedCase-Server"},
		{"a space folds", "Claude Preview", "Claude_Preview"},
		{"a dot folds", "my.server", "my_server"},
		{"an at-sign and a slash fold", "@acme/server", "_acme_server"},
		// The distinguishing case: replacement is per character, not per run, so two
		// adjacent illegal characters become two underscores.
		{"runs are not collapsed", "a..b", "a__b"},
		{"leading and trailing are not trimmed", "-lead.trail-", "-lead_trail-"},
		{"an existing underscore run is left alone", "double__under", "double__under"},
		{"empty stays empty", "", ""},

		// The claude.ai branch: names beginning with the literal display prefix also
		// collapse underscore runs and lose leading and trailing underscores.
		{"connector folds the dot and space", "claude.ai Linear", "claude_ai_Linear"},
		{"connector collapses and trims", "claude.ai MyServer (2)", "claude_ai_MyServer_2"},
		{"connector keeps a name ending in a digit", "claude.ai Notion 2", "claude_ai_Notion_2"},
		{"connector preserves inner case", "claude.ai Google Calendar", "claude_ai_Google_Calendar"},
		// The branch keys on the raw name, so a name that merely resembles it after
		// folding does not get the extra pass.
		{"lookalike does not take the branch", "claude_ai Linear (2)", "claude_ai_Linear__2_"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formClaudeCode(tc.in); got != tc.want {
				t.Fatalf("formClaudeCode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestFormCodex transcribes sanitize_responses_api_tool_name from
// codex-rs/codex-mcp/src/mcp/mod.rs at revision 3725f02c. Verified against the
// function itself, extracted and run.
func TestFormCodex(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain name is untouched", "linear", "linear"},
		{"underscore is legal and survives", "under_name", "under_name"},
		// The one difference from Claude Code.
		{"hyphen folds", "dash-name", "dash_name"},
		{"case is preserved", "MixedCase-Server", "MixedCase_Server"},
		{"a dot folds", "dot.dot", "dot_dot"},
		{"runs are not collapsed", "dot..dot", "dot__dot"},
		{"an at-sign folds", "at@sign", "at_sign"},
		{"a space folds", "space name", "space_name"},
		{"leading and trailing are not trimmed", "-lead-trail-", "_lead_trail_"},
		{"an existing underscore run is left alone", "double__under", "double__under"},
		{"the captured probe server", "probe-npx-stdio", "probe_npx_stdio"},
		{"empty becomes one underscore", "", "_"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formCodex(tc.in); got != tc.want {
				t.Fatalf("formCodex(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFormsDifferOnlyOnHyphenWithinTheBMP(t *testing.T) {
	for _, in := range []string{
		"linear", "my_server", "MixedCase", "a..b", "space name",
		"my.server", "@acme/server", "double__under", "-lead-trail-",
		// Non-ASCII, still one UTF-16 code unit each, so the invariant holds.
		"café", "中文", "★-star",
	} {
		claude, codex := formClaudeCode(in), formCodex(in)
		// Replacing every hyphen in Claude Code's answer must give Codex's.
		want := ""
		for _, r := range claude {
			if r == '-' {
				want += "_"
			} else {
				want += string(r)
			}
		}
		if codex != want {
			t.Errorf("for %q: claude=%q codex=%q, but hyphen-substituting claude gives %q",
				in, claude, codex, want)
		}
	}
}

func TestFormsDifferAboveTheBMP(t *testing.T) {
	for _, tc := range []struct{ in, claude, codex string }{
		{"a🙂b", "a__b", "a_b"},
		// Two of them, and one adjacent to an ordinary illegal character.
		{"🙂🙂", "____", "__"},
		{"a.🙂", "a___", "a__"},
		// A BMP character stays one underscore under both rules, which is what
		// makes the code unit — not "non-ASCII" — the thing being tested.
		{"a★b", "a_b", "a_b"},
	} {
		if got := formClaudeCode(tc.in); got != tc.claude {
			t.Errorf("formClaudeCode(%q) = %q, want %q", tc.in, got, tc.claude)
		}
		if got := formCodex(tc.in); got != tc.codex {
			t.Errorf("formCodex(%q) = %q, want %q", tc.in, got, tc.codex)
		}
	}
}
