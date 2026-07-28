package hookinstall

import (
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

// destFor returns the darwin destination for an agent, used to drive mergeConfig
// through its format/agent dispatch exactly as Run does. The two vscode
// destinations are disambiguated by format.
func destFor(t *testing.T, agent localagent.Agent, format Format) Destination {
	t.Helper()
	for _, d := range Destinations("darwin") {
		if d.Agent == agent && d.Format == format {
			return d
		}
	}
	t.Fatalf("no darwin destination for agent %q format %q", agent, format)
	return Destination{}
}

// mergeOnce runs one merge for a destination and fails on error.
func mergeOnce(t *testing.T, d Destination, existing []byte) mergeOutcome {
	t.Helper()
	o, err := mergeConfig(d, existing, macExe, "darwin", false)
	if err != nil {
		t.Fatalf("mergeConfig(%s): %v", d.Label, err)
	}
	return o
}

// TestMergeNewFileMatchesGolden proves a first install of each JSON hook file
// (absent -> written fresh) produces the exact canonical golden document.
func TestMergeNewFileMatchesGolden(t *testing.T) {
	cases := []struct {
		name   string
		agent  localagent.Agent
		format Format
		golden string
	}{
		{"claude", localagent.ClaudeCode, FormatJSON, claudeDarwinGolden},
		{"cursor", localagent.Cursor, FormatJSON, cursorDarwinGolden},
		{"vscode hook", localagent.VSCode, FormatJSON, vscodeDarwinGolden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := mergeOnce(t, destFor(t, tc.agent, tc.format), nil)
			if o.status != StatusInstalled || !o.write {
				t.Fatalf("new file: status=%q write=%v, want installed/true", o.status, o.write)
			}
			if string(o.data) != tc.golden {
				t.Fatalf("new file mismatch\n--- got ---\n%s\n--- want ---\n%s", o.data, tc.golden)
			}
		})
	}
}

// TestMergeIdempotentAcrossAgents proves that for every agent, merging the
// freshly written document again reports unchanged and does not rewrite it.
func TestMergeIdempotentAcrossAgents(t *testing.T) {
	dests := []Destination{
		destFor(t, localagent.ClaudeCode, FormatJSON),
		destFor(t, localagent.Cursor, FormatJSON),
		destFor(t, localagent.VSCode, FormatJSON),
		destFor(t, localagent.VSCode, FormatJSONC),
		destFor(t, localagent.Codex, FormatTOML),
	}
	for _, d := range dests {
		t.Run(d.Label, func(t *testing.T) {
			first := mergeOnce(t, d, nil)
			if !first.write {
				t.Fatalf("first merge should write")
			}
			// Re-merging the written document reports unchanged and does not
			// rewrite it, so the on-disk file stays byte-identical (the merged
			// bytes are discarded when write is false). End-to-end file-level
			// byte-identity is asserted in TestRunConvergesAndIsIdempotent.
			second := mergeOnce(t, d, first.data)
			if second.status != StatusUnchanged || second.write {
				t.Fatalf("second merge: status=%q write=%v, want unchanged/false", second.status, second.write)
			}
		})
	}
}

// TestMergePreservesThirdPartyAndReplacesStale exercises the core convergence
// contract on Cursor's direct layout: a third-party hook is preserved, a stale
// owned entry (pointing at a previous obot-sentry path) is replaced by the current
// one, duplicate owned entries collapse, and the status is "updated".
func TestMergePreservesThirdPartyAndReplacesStale(t *testing.T) {
	stale := ownedCmd("/previous/obot-sentry")
	src := `{
  "version": 0,
  "hooks": {
    "postToolUse": [
      {"type": "command", "command": "/third/party watch"},
      {"type": "command", "command": "` + stale + `"},
      {"type": "command", "command": "` + stale + `"}
    ]
  }
}`
	o := mergeOnce(t, destFor(t, localagent.Cursor, FormatJSON), []byte(src))
	if o.status != StatusUpdated {
		t.Fatalf("status = %q, want updated", o.status)
	}
	if o.dupes != 1 {
		t.Fatalf("dupes = %d, want 1 (two stale owned entries collapsed to one)", o.dupes)
	}
	got := string(o.data)
	if !strings.Contains(got, "/third/party watch") {
		t.Fatalf("third-party hook lost:\n%s", got)
	}
	if strings.Contains(got, "/previous/obot-sentry") {
		t.Fatalf("stale owned entry not replaced:\n%s", got)
	}
	if !strings.Contains(got, "--agent cursor --phase post-tool "+managedMarker) {
		t.Fatalf("current desired entry missing:\n%s", got)
	}
	if !strings.Contains(got, `"version": 1`) {
		t.Fatalf("version not forced to 1:\n%s", got)
	}
}

// TestMergeExistingFileWithoutOwnedIsInstalled proves adding a hook to a file
// that has unrelated settings but no obot-sentry hook reports "installed", not
// "updated", while preserving the unrelated settings.
func TestMergeExistingFileWithoutOwnedIsInstalled(t *testing.T) {
	src := `{
  "editor.fontSize": 14,
  "hooks": {
    "PostToolUse": [
      {"type": "command", "command": "/third/party run"}
    ]
  }
}`
	o := mergeOnce(t, destFor(t, localagent.ClaudeCode, FormatJSON), []byte(src))
	if o.status != StatusInstalled {
		t.Fatalf("status = %q, want installed", o.status)
	}
	if !strings.Contains(string(o.data), "/third/party run") {
		t.Fatalf("third-party hook lost:\n%s", o.data)
	}
}

// TestMergeIncompatibleHooksTypeRejected proves an incompatible "hooks" type is
// reported as an error rather than silently overwritten.
func TestMergeIncompatibleHooksTypeRejected(t *testing.T) {
	src := `{"hooks": "not an object"}`
	if _, err := mergeConfig(destFor(t, localagent.ClaudeCode, FormatJSON), []byte(src), macExe, "darwin", false); err == nil {
		t.Fatal("expected an error for an incompatible hooks type")
	}
}

// TestMergeMalformedRejected proves a malformed document aborts rather than
// being treated as empty.
func TestMergeMalformedRejected(t *testing.T) {
	if _, err := mergeConfig(destFor(t, localagent.Cursor, FormatJSON), []byte(`{"hooks":`), macExe, "darwin", false); err == nil {
		t.Fatal("expected malformed JSON to be rejected")
	}
	if _, err := mergeConfig(destFor(t, localagent.Codex, FormatTOML), []byte(`x = = 1`), macExe, "darwin", false); err == nil {
		t.Fatal("expected malformed TOML to be rejected")
	}
}

// TestMergeVSCodeSettingsConverges proves the settings merge enables the Copilot
// location and disables the three Claude locations while preserving a custom
// location, and reports "updated" because a managed key already existed.
func TestMergeVSCodeSettingsConverges(t *testing.T) {
	src := `{
  "editor.fontSize": 14,
  "chat.hookFilesLocations": {
    "~/custom/hooks": true,
    "~/.copilot/hooks": false
  }
}`
	d := destFor(t, localagent.VSCode, FormatJSONC)
	o := mergeOnce(t, d, []byte(src))
	if o.status != StatusUpdated {
		t.Fatalf("status = %q, want updated", o.status)
	}
	got := string(o.data)
	for _, want := range []string{
		`"~/custom/hooks": true`,
		`"~/.copilot/hooks": true`,
		`".claude/settings.json": false`,
		`".claude/settings.local.json": false`,
		`"~/.claude/settings.json": false`,
		`"editor.fontSize": 14`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("settings merge missing %q:\n%s", want, got)
		}
	}

	// Re-merging the converged settings is unchanged.
	if second := mergeOnce(t, d, o.data); second.status != StatusUnchanged || second.write {
		t.Fatalf("second settings merge: status=%q write=%v, want unchanged/false", second.status, second.write)
	}
}

// TestMergeCodexConvergesMatchesGolden proves the Codex merge through the
// dispatch produces the same golden document the tomlconfig test pins, replacing
// a stale owned entry and preserving unrelated tables, and is unchanged on a
// second pass.
func TestMergeCodexConvergesMatchesGolden(t *testing.T) {
	d := destFor(t, localagent.Codex, FormatTOML)
	o := mergeOnce(t, d, []byte(codexFixture))
	if o.status != StatusUpdated {
		t.Fatalf("status = %q, want updated (a stale owned entry was present)", o.status)
	}
	if string(o.data) != codexMergedGolden {
		t.Fatalf("codex merge mismatch\n--- got ---\n%s\n--- want ---\n%s", o.data, codexMergedGolden)
	}
	if second := mergeOnce(t, d, o.data); second.status != StatusUnchanged || second.write {
		t.Fatalf("second codex merge: status=%q write=%v, want unchanged/false", second.status, second.write)
	}
}

// TestMergeWindowsCommandsSurvive proves the Windows call-operator/quoting forms
// survive template generation and serialization for a fresh install on windows.
func TestMergeWindowsCommandsSurvive(t *testing.T) {
	dests := Destinations("windows")
	find := func(agent localagent.Agent, format Format) Destination {
		for _, d := range dests {
			if d.Agent == agent && d.Format == format {
				return d
			}
		}
		t.Fatalf("no windows destination for %q/%q", agent, format)
		return Destination{}
	}
	// VS Code hook uses the PowerShell call operator.
	vs, err := mergeConfig(find(localagent.VSCode, FormatJSON), nil, winExe, "windows", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(vs.data), `& \"C:\\Program Files\\Obot\\obot-sentry\\obot-sentry.exe\"`) {
		t.Fatalf("vscode windows call operator not preserved:\n%s", vs.data)
	}
	// Cursor uses a directly quoted executable, no call operator.
	cur, err := mergeConfig(find(localagent.Cursor, FormatJSON), nil, winExe, "windows", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cur.data), "& \\\"") {
		t.Fatalf("cursor windows must not use the call operator:\n%s", cur.data)
	}
	if !strings.Contains(string(cur.data), `\"C:\\Program Files\\Obot\\obot-sentry\\obot-sentry.exe\"`) {
		t.Fatalf("cursor windows quoted executable not preserved:\n%s", cur.data)
	}
}
