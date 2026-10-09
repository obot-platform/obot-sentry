package hookinstall

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

func decodeKiro(t *testing.T, data []byte) kiroDocument {
	t.Helper()
	// Strict, as Kiro reads it: JSON.parse rejects a file with a comment.
	var doc kiroDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decoding Kiro hook file: %v\n%s", err, data)
	}
	return doc
}

// TestKiroFreshFile pins the whole written document against Kiro's v1 schema:
// version, a non-empty hooks array, and per hook a name, trigger, a matcher that
// compiles as a RegExp, a command action, and a timeout.
func TestKiroFreshFile(t *testing.T) {
	for _, tc := range []struct {
		goos, exe string
		audit     string
		enforce   string
	}{
		{"darwin", macExe,
			"/usr/local/bin/obot-sentry audit submit --agent kiro --phase post-tool --managed-by obot-sentry",
			"/usr/local/bin/obot-sentry enforce --agent kiro --event PreToolUse --managed-by obot-sentry"},
		// cmd.exe runs the command, so no PowerShell call operator.
		{"windows", winExe,
			`"C:\Program Files\Obot\obot-sentry\obot-sentry.exe" audit submit --agent kiro --phase post-tool --managed-by obot-sentry`,
			`"C:\Program Files\Obot\obot-sentry\obot-sentry.exe" enforce --agent kiro --event PreToolUse --managed-by obot-sentry`},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			out, err := mergeConfig(destFor(t, localagent.Kiro, FormatJSON), nil, tc.exe, tc.goos, true)
			if err != nil {
				t.Fatal(err)
			}
			if out.status != StatusInstalled || !out.write {
				t.Fatalf("status = %s, write = %v", out.status, out.write)
			}
			doc := decodeKiro(t, out.data)
			want := kiroDocument{Version: "v1", Hooks: []kiroHook{
				{Name: "Obot audit", Trigger: "PostToolUse", Matcher: ".*", Action: kiroAction{Type: "command", Command: tc.audit}, Timeout: hookTimeout},
				{Name: "Obot tool policy", Trigger: "PreToolUse", Matcher: ".*", Action: kiroAction{Type: "command", Command: tc.enforce}, Timeout: hookTimeout},
			}}
			if got, _ := json.Marshal(doc); string(got) != string(mustMarshal(t, want)) {
				t.Fatalf("document =\n%s\nwant\n%s", got, mustMarshal(t, want))
			}
		})
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestKiroConverges covers the lifecycle on one file: a second run is a no-op,
// someone else's hook in the same file survives every step, dropping --enforce
// removes only the enforcement hook, and uninstall removes only ours.
func TestKiroConverges(t *testing.T) {
	d := destFor(t, localagent.Kiro, FormatJSON)
	theirs := `{"name": "Lint on save", "trigger": "PostFileSave", "matcher": "\\.ts$", "action": {"type": "command", "command": "npm run lint"}}`
	existing := `{
  "version": "v1",
  "hooks": [
    ` + theirs + `
  ]
}
`
	enforced, err := mergeConfig(d, []byte(existing), macExe, "darwin", true)
	if err != nil {
		t.Fatal(err)
	}
	if enforced.status != StatusInstalled {
		t.Fatalf("first run status = %s", enforced.status)
	}
	if !strings.Contains(string(enforced.data), theirs) {
		t.Fatalf("third-party content lost:\n%s", enforced.data)
	}
	if doc := decodeKiro(t, enforced.data); len(doc.Hooks) != 3 {
		t.Fatalf("hooks = %+v, want theirs plus audit and enforce", doc.Hooks)
	}

	again, err := mergeConfig(d, enforced.data, macExe, "darwin", true)
	if err != nil {
		t.Fatal(err)
	}
	if again.status != StatusUnchanged || again.write {
		t.Fatalf("second run status = %s, write = %v", again.status, again.write)
	}

	auditOnly, err := mergeConfig(d, enforced.data, macExe, "darwin", false)
	if err != nil {
		t.Fatal(err)
	}
	if auditOnly.status != StatusUpdated || auditOnly.removed != 1 || auditOnly.dupes != 0 {
		t.Fatalf("audit-only run = %+v", auditOnly)
	}
	doc := decodeKiro(t, auditOnly.data)
	if len(doc.Hooks) != 2 || doc.Hooks[1].Trigger != "PostToolUse" {
		t.Fatalf("audit-only hooks = %+v", doc.Hooks)
	}

	removed, err := removeConfig(d, auditOnly.data)
	if err != nil {
		t.Fatal(err)
	}
	if removed.status != StatusRemoved || removed.removed != 1 {
		t.Fatalf("uninstall = %+v", removed)
	}
	doc = decodeKiro(t, removed.data)
	if len(doc.Hooks) != 1 || doc.Hooks[0].Name != "Lint on save" {
		t.Fatalf("uninstall left %+v, want only theirs", doc.Hooks)
	}
}

// TestKiroReplacesStaleAndDuplicateEntries: an old binary path and a duplicate
// both collapse to the one current entry.
func TestKiroReplacesStaleAndDuplicateEntries(t *testing.T) {
	stale := `{"name": "Obot audit", "trigger": "PostToolUse", "matcher": ".*", "action": {"type": "command", "command": "/opt/old/obot-sentry audit submit --agent kiro --phase post-tool --managed-by obot-sentry"}, "timeout": 30}`
	existing := `{"version": "v0", "hooks": [` + stale + `, ` + stale + `]}`
	out, err := mergeConfig(destFor(t, localagent.Kiro, FormatJSON), []byte(existing), macExe, "darwin", false)
	if err != nil {
		t.Fatal(err)
	}
	if out.status != StatusUpdated || out.dupes != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	doc := decodeKiro(t, out.data)
	if doc.Version != "v1" {
		t.Errorf("version = %q, want forced to v1", doc.Version)
	}
	if len(doc.Hooks) != 1 || !strings.HasPrefix(doc.Hooks[0].Action.Command, macExe+" ") {
		t.Fatalf("hooks = %+v", doc.Hooks)
	}
}

// TestKiroRejectsIncompatibleHooks: a hooks member that is not an array is not
// overwritten.
func TestKiroRejectsIncompatibleHooks(t *testing.T) {
	if _, err := mergeConfig(destFor(t, localagent.Kiro, FormatJSON), []byte(`{"hooks": {}}`), macExe, "darwin", false); err == nil {
		t.Fatal("expected an error for a non-array hooks member")
	}
}

// TestKiroReenablesDisabledHook: turning a hook off in Kiro's Agent Hooks
// panel writes "enabled": false into our entry. The next convergence replaces
// the entry, which turns the hook back on.
func TestKiroReenablesDisabledHook(t *testing.T) {
	d := destFor(t, localagent.Kiro, FormatJSON)
	fresh, err := mergeConfig(d, nil, macExe, "darwin", false)
	if err != nil {
		t.Fatal(err)
	}
	disabled := strings.Replace(string(fresh.data), `"name": "Obot audit",`, `"name": "Obot audit", "enabled": false,`, 1)
	if disabled == string(fresh.data) {
		t.Fatal("test setup: could not disable the hook")
	}
	out, err := mergeConfig(d, []byte(disabled), macExe, "darwin", false)
	if err != nil {
		t.Fatal(err)
	}
	if out.status != StatusUpdated || !out.write || strings.Contains(string(out.data), `"enabled"`) {
		t.Fatalf("outcome = %s, write = %v, data:\n%s", out.status, out.write, out.data)
	}
}
