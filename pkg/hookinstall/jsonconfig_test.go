package hookinstall

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

// ownedCmd builds a command string carrying the obot-sentry ownership marker for an
// arbitrary executable path, so tests can seed owned entries that point at a
// different binary than the current desired one.
func ownedCmd(exe string) string {
	return exe + " audit submit --agent cursor --phase post-tool " + managedMarker
}

// mustParse parses JSON/JSONC test input or fails.
func mustParse(t *testing.T, data string) *jsonConfig {
	t.Helper()
	cfg, err := parseJSONConfig([]byte(data))
	if err != nil {
		t.Fatalf("parseJSONConfig(%q): %v", data, err)
	}
	return cfg
}

// TestParseJSONConfigMalformedAborts proves a malformed document is reported as
// an error rather than silently becoming {}, which would destroy user config.
func TestParseJSONConfigMalformedAborts(t *testing.T) {
	for _, src := range []string{
		`{"hooks": }`,
		`{"a": 1`,
		`not json at all`,
		`{"a": 1} trailing garbage {`,
	} {
		if _, err := parseJSONConfig([]byte(src)); err == nil {
			t.Fatalf("parseJSONConfig(%q) = nil error, want error (must never fall back to {})", src)
		}
	}
}

// TestParseJSONConfigEmpty proves empty/whitespace input becomes an empty object
// a first install can populate.
func TestParseJSONConfigEmpty(t *testing.T) {
	for _, src := range []string{"", "   ", "\n\t\n"} {
		cfg := mustParse(t, src)
		if _, err := cfg.object(); err != nil {
			t.Fatalf("empty input %q did not yield an object: %v", src, err)
		}
		if got := string(cfg.pack()); got != "{}" {
			t.Fatalf("empty input %q packed to %q, want {}", src, got)
		}
	}
}

// TestJSONConfigRootNotObject proves a document whose top level is not an object
// is rejected rather than overwritten.
func TestJSONConfigRootNotObject(t *testing.T) {
	cfg := mustParse(t, `[1, 2, 3]`)
	if _, err := cfg.object(); err == nil {
		t.Fatal("expected error for non-object root, got nil")
	}
}

// TestJSONCPreservesUnrelatedContent is the JSONC AST-editing proof: comments,
// trailing commas, unknown settings, and line endings survive a targeted merge
// into chat.hookFilesLocations. It mirrors the VS Code settings merge.
func TestJSONCPreservesUnrelatedContent(t *testing.T) {
	// A realistic settings.json: comments, a trailing comma, an unrelated
	// object, an unrelated array, a URL with `//`, and CRLF line endings.
	src := "{\r\n" +
		"  // editor preferences\r\n" +
		"  \"editor.fontSize\": 14,\r\n" +
		"  \"editor.rulers\": [80, 100,],\r\n" +
		"  \"update.url\": \"https://example.com//releases\",\r\n" +
		"  /* keep me */\r\n" +
		"  \"chat.hookFilesLocations\": {\r\n" +
		"    \"~/custom/hooks\": true,\r\n" +
		"    \"~/.copilot/hooks\": false\r\n" +
		"  },\r\n" +
		"}\r\n"

	merge := func(data []byte) []byte {
		cfg, err := parseJSONConfig(data)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		obj, err := cfg.object()
		if err != nil {
			t.Fatalf("object: %v", err)
		}
		loc, err := getOrCreateObjectMember(obj, "chat.hookFilesLocations")
		if err != nil {
			t.Fatalf("locations: %v", err)
		}
		for _, sv := range desiredVSCodeHookLocations() {
			objectSet(loc, sv.Key, hujson.Value{Value: hujson.Bool(sv.Value)})
		}
		return cfg.pack()
	}

	// Because Pack reproduces every untouched byte, the merge is fully
	// deterministic and we can assert the exact result rather than probing for
	// fragments. This pins down member ordering, insertion position, and the
	// in-place value flip — not just presence:
	//   - every unrelated line (comments, fontSize, the trailing-comma array, the
	//     `//`-bearing URL) is byte-identical and in its original position;
	//   - "~/.copilot/hooks" flips false->true in place, keeping its position;
	//   - the three ".claude/*" exclusions are appended after it, in order, with
	//     the sibling's 4-space indentation; and
	//   - CRLF line endings are preserved throughout.
	want := "{\r\n" +
		"  // editor preferences\r\n" +
		"  \"editor.fontSize\": 14,\r\n" +
		"  \"editor.rulers\": [80, 100,],\r\n" +
		"  \"update.url\": \"https://example.com//releases\",\r\n" +
		"  /* keep me */\r\n" +
		"  \"chat.hookFilesLocations\": {\r\n" +
		"    \"~/custom/hooks\": true,\r\n" +
		"    \"~/.copilot/hooks\": true,\r\n" +
		"    \".claude/settings.json\": false,\r\n" +
		"    \".claude/settings.local.json\": false,\r\n" +
		"    \"~/.claude/settings.json\": false\r\n" +
		"  },\r\n" +
		"}\r\n"

	out := merge([]byte(src))
	if string(out) != want {
		t.Fatalf("merged output mismatch\n--- got ---\n%q\n--- want ---\n%q", out, want)
	}

	// Byte-idempotent: a second identical merge reproduces the file exactly.
	out2 := merge(out)
	if !bytes.Equal(out, out2) {
		t.Fatalf("merge not byte-idempotent:\n--- first ---\n%s\n--- second ---\n%s", out, out2)
	}
}

// TestJSONConfigBOMPreserved proves a leading UTF-8 BOM is stripped for parsing
// and re-prepended on pack, surviving a mutation.
func TestJSONConfigBOMPreserved(t *testing.T) {
	src := append([]byte(utf8BOM), []byte("{\n  \"a\": 1\n}\n")...)
	cfg, err := parseJSONConfig(src)
	if err != nil {
		t.Fatalf("parse with BOM: %v", err)
	}
	if !cfg.bom {
		t.Fatal("BOM not detected")
	}
	obj, _ := cfg.object()
	objectSet(obj, "b", hujson.Value{Value: hujson.Int(2)})
	out := cfg.pack()
	if !bytes.HasPrefix(out, []byte(utf8BOM)) {
		t.Fatalf("packed output lost its leading BOM:\n%q", out)
	}
	// Exactly one BOM, at the very start.
	if bytes.Count(out, []byte(utf8BOM)) != 1 {
		t.Fatalf("expected exactly one BOM, got %d:\n%q", bytes.Count(out, []byte(utf8BOM)), out)
	}
}

// TestFilterDirectOwned covers the Cursor/VS Code direct-entry layout: owned
// entries are removed (including duplicates), third-party entries and unrelated
// element shapes are preserved.
func TestFilterDirectOwned(t *testing.T) {
	src := `{
  "postToolUse": [
    {"type": "command", "command": "/third/party/tool run"},
    {"type": "command", "command": "` + ownedCmd("/old/obot-sentry") + `"},
    {"type": "command", "command": "` + ownedCmd("/new/obot-sentry") + `"},
    {"note": "not a command entry"}
  ]
}`
	cfg := mustParse(t, src)
	obj, _ := cfg.object()
	arr, ok := asArray(objectMember(obj, "postToolUse"))
	if !ok {
		t.Fatal("postToolUse is not an array")
	}
	removed := filterDirectOwned(arr)
	if removed != 2 {
		t.Fatalf("removed = %d, want 2 (both owned entries incl. duplicate)", removed)
	}
	if len(arr.Elements) != 2 {
		t.Fatalf("kept %d elements, want 2 (third-party command + non-command entry)", len(arr.Elements))
	}
	out := string(cfg.pack())
	if !strings.Contains(out, "/third/party/tool run") {
		t.Fatalf("third-party command not preserved:\n%s", out)
	}
	if !strings.Contains(out, "not a command entry") {
		t.Fatalf("non-command entry not preserved:\n%s", out)
	}
	if strings.Contains(out, managedMarker) {
		t.Fatalf("owned entries not fully removed:\n%s", out)
	}
}

// TestFilterNestedOwned covers Claude Code's matcher-group layout: only owned
// inner hooks are removed, a group is dropped only when our removal empties it,
// and groups retaining third-party inner hooks survive.
func TestFilterNestedOwned(t *testing.T) {
	src := `{
  "PostToolUse": [
    {
      "matcher": "*",
      "hooks": [
        {"type": "command", "command": "/third/party keep"},
        {"type": "command", "command": "` + ownedCmd("/old/obot-sentry") + `"}
      ]
    },
    {
      "matcher": "Bash",
      "hooks": [
        {"type": "command", "command": "` + ownedCmd("/old/obot-sentry") + `"}
      ]
    },
    {
      "matcher": "Edit",
      "hooks": [
        {"type": "command", "command": "/unrelated only"}
      ]
    }
  ]
}`
	cfg := mustParse(t, src)
	obj, _ := cfg.object()
	arr, ok := asArray(objectMember(obj, "PostToolUse"))
	if !ok {
		t.Fatal("PostToolUse is not an array")
	}
	removed := filterNestedOwned(arr)
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	// The "*" group keeps its third-party hook; the "Bash" group is dropped
	// (emptied by our removal); the "Edit" group is untouched. => 2 groups.
	if len(arr.Elements) != 2 {
		t.Fatalf("kept %d groups, want 2:\n%s", len(arr.Elements), cfg.pack())
	}
	out := string(cfg.pack())
	if !strings.Contains(out, "/third/party keep") || !strings.Contains(out, "/unrelated only") {
		t.Fatalf("third-party inner hooks not preserved:\n%s", out)
	}
	if strings.Contains(out, "\"Bash\"") {
		t.Fatalf("emptied Bash group should have been dropped:\n%s", out)
	}
	if strings.Contains(out, managedMarker) {
		t.Fatalf("owned inner hooks not fully removed:\n%s", out)
	}
}

// TestFilterNestedPreservesPreexistingEmptyGroup proves a group we did not touch
// (already empty, no owned hooks removed from it) is left alone rather than
// cleaned up — we only remove what we own.
func TestFilterNestedPreservesPreexistingEmptyGroup(t *testing.T) {
	src := `{
  "PostToolUse": [
    {"matcher": "*", "hooks": []}
  ]
}`
	cfg := mustParse(t, src)
	obj, _ := cfg.object()
	arr, _ := asArray(objectMember(obj, "PostToolUse"))
	if removed := filterNestedOwned(arr); removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
	if len(arr.Elements) != 1 {
		t.Fatal("pre-existing empty group should be preserved")
	}
}

// TestJSONMergeAppendIdempotent proves the filter-then-append merge is a fixed
// point: appending the desired Cursor entry, then filtering it back out and
// re-appending, reproduces byte-identical output. This is what lets a second
// install report unchanged.
func TestJSONMergeAppendIdempotent(t *testing.T) {
	desired := desiredCursor(macExe, "darwin", false).Hooks.PostToolUse[0]

	// Start from a file with a third-party hook and a stale owned entry pointing
	// at a previous obot-sentry path.
	src := `{
  "version": 0,
  "hooks": {
    "postToolUse": [
      {"type": "command", "command": "/third/party watch"},
      {"type": "command", "command": "` + ownedCmd("/previous/obot-sentry") + `"}
    ]
  }
}`

	merge := func(data []byte) []byte {
		cfg := mustParse(t, string(data))
		obj, _ := cfg.object()
		objectSet(obj, "version", hujson.Value{Value: hujson.Int(cursorVersion)})
		hooks, err := getOrCreateObjectMember(obj, "hooks")
		if err != nil {
			t.Fatalf("hooks: %v", err)
		}
		arr, err := getOrCreateArrayMember(hooks, "postToolUse")
		if err != nil {
			t.Fatalf("postToolUse: %v", err)
		}
		filterDirectOwned(arr)
		entry, err := jsonValueFromGo(desired)
		if err != nil {
			t.Fatalf("entry: %v", err)
		}
		arrayAppend(arr, entry)
		return cfg.pack()
	}

	out1 := merge([]byte(src))
	out2 := merge(out1)
	if !bytes.Equal(out1, out2) {
		t.Fatalf("append merge not byte-idempotent:\n--- run1 ---\n%s\n--- run2 ---\n%s", out1, out2)
	}
	if sem, err := jsonSemanticEqual(out1, out2); err != nil || !sem {
		t.Fatalf("jsonSemanticEqual = %v, err=%v; want true", sem, err)
	}
	got := string(out1)
	if !strings.Contains(got, "/third/party watch") {
		t.Fatalf("third-party hook lost:\n%s", got)
	}
	if strings.Contains(got, "/previous/obot-sentry") {
		t.Fatalf("stale owned entry not replaced:\n%s", got)
	}
	if !strings.Contains(got, "--agent cursor --phase post-tool "+managedMarker) {
		t.Fatalf("desired entry not present:\n%s", got)
	}
	if !strings.Contains(got, "\"version\": 1") {
		t.Fatalf("version not forced to 1:\n%s", got)
	}
}

// TestArrayAppendDropsSiblingComment proves an appended entry takes only the
// sibling's line indentation, never a comment the sibling carries — so a comment
// preceding the last element is not duplicated in front of the entry we add.
func TestArrayAppendDropsSiblingComment(t *testing.T) {
	src := `{
  "postToolUse": [
    {"type": "command", "command": "/third/party run"},
    // keep this note on the sibling
    {"type": "command", "command": "/second/party run"}
  ]
}`
	cfg := mustParse(t, src)
	obj, _ := cfg.object()
	arr, ok := asArray(objectMember(obj, "postToolUse"))
	if !ok {
		t.Fatal("postToolUse is not an array")
	}
	entry, err := jsonValueFromGo(map[string]string{"type": "command", "command": "/new/obot-sentry run"})
	if err != nil {
		t.Fatal(err)
	}
	arrayAppend(arr, entry)

	out := string(cfg.pack())
	if got := strings.Count(out, "keep this note on the sibling"); got != 1 {
		t.Fatalf("sibling comment count = %d, want 1 (must not be copied onto the appended entry):\n%s", got, out)
	}
	if !strings.Contains(out, "/new/obot-sentry run") {
		t.Fatalf("appended entry missing:\n%s", out)
	}
	// The appended entry lines up with its siblings at four-space indentation
	// (jsonValueFromGo emits compact JSON with map keys sorted).
	if !strings.Contains(out, "\n    {\"command\":\"/new/obot-sentry run\",\"type\":\"command\"}") {
		t.Fatalf("appended entry not indented like its siblings:\n%s", out)
	}
}

// TestJSONSemanticEqual checks the convergence comparison ignores formatting,
// comments, and a BOM but respects value differences.
func TestJSONSemanticEqual(t *testing.T) {
	a := []byte("{\n  \"a\": 1,\n  \"b\": [1, 2]\n}\n")
	b := append([]byte(utf8BOM), []byte(`{"a":1,/*c*/"b":[1,2,]}`)...)
	if eq, err := jsonSemanticEqual(a, b); err != nil || !eq {
		t.Fatalf("expected semantic equality, got %v err=%v", eq, err)
	}
	c := []byte(`{"a":2,"b":[1,2]}`)
	if eq, _ := jsonSemanticEqual(a, c); eq {
		t.Fatal("expected inequality for differing values")
	}
}
