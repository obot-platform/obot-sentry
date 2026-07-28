package hookinstall

import (
	"bytes"
	"reflect"
	"testing"
)

// decodeTOML decodes TOML test input or fails.
func decodeTOML(t *testing.T, data []byte) codexTOMLDoc {
	t.Helper()
	m, err := parseCodexTOML(data)
	if err != nil {
		t.Fatalf("parseCodexTOML: %v", err)
	}
	return m
}

// TestParseCodexTOMLMalformedAborts proves a malformed file is reported as an
// error, never treated as empty.
func TestParseCodexTOMLMalformedAborts(t *testing.T) {
	for _, src := range []string{
		`title = `,
		`[features`,
		`x = = 1`,
	} {
		if _, err := parseCodexTOML([]byte(src)); err == nil {
			t.Fatalf("parseCodexTOML(%q) = nil error, want error", src)
		}
	}
}

// TestParseCodexTOMLEmpty proves empty input becomes an empty document.
func TestParseCodexTOMLEmpty(t *testing.T) {
	for _, src := range []string{"", "   \n\t"} {
		m := decodeTOML(t, []byte(src))
		if len(m) != 0 {
			t.Fatalf("empty input %q decoded to %#v", src, m)
		}
	}
}

// codexFixture exercises the hard round-trip cases: datetimes, local dates,
// multiline strings, special floats, heterogeneous and array-of-tables values,
// inline tables, dotted/quoted table names, an existing [features] with
// hooks = false, and multiple third-party inner hooks in one event.
const codexFixture = `# top comment
title = "requirements"
ratio = 3.14
positive_inf = inf
negative_inf = -inf
when = 2026-07-16T10:30:00Z
localdate = 2026-07-16
multiline = """
first
second"""
mixed = [1, 2, 3]
inline = { alpha = 1, beta = 2 }

[features]
hooks = false
other = "keep"

["quoted.name"]
value = 42

[server.limits]
max = 100

[[hooks.PostToolUse]]
matcher = ".*"

[[hooks.PostToolUse.hooks]]
type = "command"
command = "/third/party/audit run"

[[hooks.PostToolUse.hooks]]
type = "command"
command = "/another/third/party watch"

[[hooks.PostToolUse.hooks]]
type = "command"
command = "/old/obot-sentry audit submit --agent codex --phase post-tool --managed-by obot-sentry"
`

// codexFixtureNormalized is the exact output of decoding codexFixture and
// re-encoding it. The encoder is deterministic, so every normalization is
// predictable: comments dropped; keys sorted alphabetically; the inline table
// expanded to an [inline] block; the multiline string rewritten as an escaped
// basic string; special floats, datetimes, and local dates preserved; and
// two-space indentation per nesting level. Unrelated data survives; only its
// formatting changes.
const codexFixtureNormalized = `localdate = 2026-07-16
mixed = [1, 2, 3]
multiline = "first\nsecond"
negative_inf = -inf
positive_inf = inf
ratio = 3.14
title = "requirements"
when = 2026-07-16T10:30:00Z

[features]
  hooks = false
  other = "keep"

[hooks]

  [[hooks.PostToolUse]]
    matcher = ".*"

    [[hooks.PostToolUse.hooks]]
      command = "/third/party/audit run"
      type = "command"

    [[hooks.PostToolUse.hooks]]
      command = "/another/third/party watch"
      type = "command"

    [[hooks.PostToolUse.hooks]]
      command = "/old/obot-sentry audit submit --agent codex --phase post-tool --managed-by obot-sentry"
      type = "command"

[inline]
  alpha = 1
  beta = 2

["quoted.name"]
  value = 42

[server]
  [server.limits]
    max = 100
`

// TestCodexDecodeEncodePreservesData proves the decode->encode cycle preserves
// all unrelated data while normalizing formatting. Because the encoder is
// deterministic we assert the exact normalized output rather than probing for
// fragments, pinning down every documented normalization at once. A re-decode
// equal to the original decode independently confirms no data was lost in the
// reformat (and guards against a mistranscribed golden).
func TestCodexDecodeEncodePreservesData(t *testing.T) {
	m1 := decodeTOML(t, []byte(codexFixture))
	encoded, err := encodeCodexTOML(m1)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != codexFixtureNormalized {
		t.Fatalf("normalized output mismatch\n--- got ---\n%s\n--- want ---\n%s", encoded, codexFixtureNormalized)
	}
	if !reflect.DeepEqual(decodeTOML(t, encoded), m1) {
		t.Fatal("decode->encode->decode changed data semantically")
	}
}

// TestCodexEncodeDeterministicAndStable proves the encoder is deterministic and
// byte-stable once normalized: the first run may reformat, every run after is
// byte-identical (the basis for reporting unchanged).
func TestCodexEncodeDeterministicAndStable(t *testing.T) {
	m := decodeTOML(t, []byte(codexFixture))
	first, err := encodeCodexTOML(m)
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic: encoding the same map again is identical.
	again, _ := encodeCodexTOML(m)
	if !bytes.Equal(first, again) {
		t.Fatal("encoder is not deterministic for the same map")
	}
	// Stable: decoding the normalized output and re-encoding is byte-identical.
	second, err := encodeCodexTOML(decodeTOML(t, first))
	if !bytes.Equal(first, second) {
		t.Fatalf("re-encode not byte-stable:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// TestSetCodexFeaturePins covers creating the table, overwriting values the user
// set, preserving unpinned siblings, and rejecting an incompatible existing value.
func TestSetCodexFeaturePins(t *testing.T) {
	assertPins := func(t *testing.T, m codexTOMLDoc) {
		t.Helper()
		feats, ok := m["features"].(map[string]any)
		if !ok {
			t.Fatalf("features is %T, want a table", m["features"])
		}
		for _, pin := range codexFeaturePins() {
			if feats[pin.Key] != pin.Value {
				t.Errorf("features.%s = %v, want %v", pin.Key, feats[pin.Key], pin.Value)
			}
		}
	}

	t.Run("creates missing table", func(t *testing.T) {
		m := codexTOMLDoc{}
		if err := setCodexFeaturePins(m, codexFeaturePins()); err != nil {
			t.Fatal(err)
		}
		assertPins(t, m)
	})
	t.Run("overwrites the user's values and preserves siblings", func(t *testing.T) {
		m := decodeTOML(t, []byte(codexFixture))
		// The fixture has hooks = false. Set the other pin against us too, so both
		// directions of overwrite are covered.
		feats := m["features"].(map[string]any)
		feats[codexFeatureNonPrefixedMCPToolName] = true

		if err := setCodexFeaturePins(m, codexFeaturePins()); err != nil {
			t.Fatal(err)
		}
		assertPins(t, m)
		if feats["other"] != "keep" {
			t.Fatalf("unrelated features key lost: %#v", feats)
		}
	})
	t.Run("leaves an unpinned feature alone", func(t *testing.T) {
		m := codexTOMLDoc{"features": map[string]any{"some_other_feature": true}}
		if err := setCodexFeaturePins(m, codexFeaturePins()); err != nil {
			t.Fatal(err)
		}
		feats := m["features"].(map[string]any)
		if feats["some_other_feature"] != true {
			t.Errorf("an unpinned feature was changed: %#v", feats)
		}
	})
	t.Run("rejects non-table features", func(t *testing.T) {
		m := codexTOMLDoc{"features": "not a table"}
		if err := setCodexFeaturePins(m, codexFeaturePins()); err == nil {
			t.Fatal("expected error for non-table features")
		}
	})
}

// TestCodexFeaturePinKeys is the lockstep guard on the key strings.
//
// A [features] key Codex does not recognize is discarded with only a startup
// warning ("Ignoring unknown `features` requirement"), which a user will not see.
// So a typo, or an upstream rename, disables the pin silently and enforcement
// degrades with no signal anywhere. There is no cross-repository test that can
// catch that, so these strings are asserted literally: changing one has to be
// deliberate.
//
// Verified against Codex's feature registry (codex-rs/features/src/lib.rs) at
// revision 3725f02c, where "hooks" is Stage::Stable default_enabled=true and
// "non_prefixed_mcp_tool_names" is Stage::UnderDevelopment default_enabled=false.
func TestCodexFeaturePinKeys(t *testing.T) {
	want := map[string]bool{
		"hooks":                       true,
		"non_prefixed_mcp_tool_names": false,
	}

	got := make(map[string]bool, len(want))
	for _, pin := range codexFeaturePins() {
		if _, dup := got[pin.Key]; dup {
			t.Errorf("feature %q is pinned twice", pin.Key)
		}
		got[pin.Key] = pin.Value
	}

	if len(got) != len(want) {
		t.Fatalf("pinned features = %v, want %v", got, want)
	}
	for key, value := range want {
		actual, ok := got[key]
		if !ok {
			t.Errorf("feature %q is no longer pinned", key)
			continue
		}
		if actual != value {
			t.Errorf("feature %q pinned to %v, want %v", key, actual, value)
		}
	}
}

// TestFilterCodexOwned proves owned inner hooks are removed while third-party
// inner hooks (including multiple in one event) are preserved.
func TestFilterCodexOwned(t *testing.T) {
	m := decodeTOML(t, []byte(codexFixture))
	removed, err := filterCodexOwned(m, "PostToolUse")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	groups := m["hooks"].(map[string]any)["PostToolUse"].([]map[string]any)
	if len(groups) != 1 {
		t.Fatalf("expected the shared group preserved, got %d groups", len(groups))
	}
	inner := groups[0]["hooks"].([]map[string]any)
	if len(inner) != 2 {
		t.Fatalf("expected 2 third-party inner hooks preserved, got %d", len(inner))
	}
	for _, h := range inner {
		if isOwnedCommand(h["command"].(string)) {
			t.Fatalf("owned hook survived: %v", h)
		}
	}
}

// TestFilterCodexDropsEmptiedGroup proves a group holding only obot-sentry hooks is
// dropped, and the event key removed when no groups remain.
func TestFilterCodexDropsEmptiedGroup(t *testing.T) {
	src := `[[hooks.PostToolUse]]
matcher = ".*"

[[hooks.PostToolUse.hooks]]
type = "command"
command = "/old/obot-sentry audit submit --agent codex --phase post-tool --managed-by obot-sentry"
`
	m := decodeTOML(t, []byte(src))
	removed, err := filterCodexOwned(m, "PostToolUse")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	hooks := m["hooks"].(map[string]any)
	if _, ok := hooks["PostToolUse"]; ok {
		t.Fatalf("emptied event key should be removed, got %#v", hooks)
	}
}

// TestFilterCodexIncompatibleType proves an incompatible hooks-table type is
// reported rather than silently ignored.
func TestFilterCodexIncompatibleType(t *testing.T) {
	m := codexTOMLDoc{"hooks": "not a table"}
	if _, err := filterCodexOwned(m, "PostToolUse"); err == nil {
		t.Fatal("expected error for non-table [hooks]")
	}
}

// codexMergedGolden is the exact output of merging the desired obot-sentry hook into
// codexFixture. It shows, precisely: [features].hooks flipped false->true; the
// stale owned entry removed from the shared ".*" group (leaving its two
// third-party inner hooks); a new group appended holding exactly the desired
// obot-sentry hook (keys sorted, so command/statusMessage/timeout/type); and every
// unrelated table ([inline], ["quoted.name"], [server.limits]) preserved.
const codexMergedGolden = `localdate = 2026-07-16
mixed = [1, 2, 3]
multiline = "first\nsecond"
negative_inf = -inf
positive_inf = inf
ratio = 3.14
title = "requirements"
when = 2026-07-16T10:30:00Z

[features]
  hooks = true
  non_prefixed_mcp_tool_names = false
  other = "keep"

[hooks]

  [[hooks.PostToolUse]]
    matcher = ".*"

    [[hooks.PostToolUse.hooks]]
      command = "/third/party/audit run"
      type = "command"

    [[hooks.PostToolUse.hooks]]
      command = "/another/third/party watch"
      type = "command"

  [[hooks.PostToolUse]]
    matcher = ".*"

    [[hooks.PostToolUse.hooks]]
      command = "/usr/local/bin/obot-sentry audit submit --agent codex --phase post-tool --managed-by obot-sentry"
      statusMessage = "Submitting Obot audit log"
      timeout = 30
      type = "command"

[inline]
  alpha = 1
  beta = 2

["quoted.name"]
  value = 42

[server]
  [server.limits]
    max = 100
`

// TestCodexMergeIdempotent proves the full Codex merge (feature flag + filter +
// append + encode) produces the exact expected document and is byte-stable on the
// second run, converging a stale owned entry in place rather than appending a
// duplicate.
func TestCodexMergeIdempotent(t *testing.T) {
	desired := desiredCodex(macExe, "darwin", false)

	merge := func(data []byte) []byte {
		m, err := parseCodexTOML(data)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if err := setCodexFeaturePins(m, desired.Features); err != nil {
			t.Fatalf("features: %v", err)
		}
		if _, err := filterCodexOwned(m, "PostToolUse"); err != nil {
			t.Fatalf("filter: %v", err)
		}
		hooks := m["hooks"]
		if hooks == nil {
			hooks = map[string]any{}
			m["hooks"] = hooks
		}
		hm := hooks.(map[string]any)
		existing, _ := tableSlice(hm["PostToolUse"])
		hm["PostToolUse"] = append(existing, codexDesiredGroups(desired.PostToolUse)...)
		out, err := encodeCodexTOML(m)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return out
	}

	out1 := merge([]byte(codexFixture))
	if string(out1) != codexMergedGolden {
		t.Fatalf("merged output mismatch\n--- got ---\n%s\n--- want ---\n%s", out1, codexMergedGolden)
	}
	out2 := merge(out1)
	if !bytes.Equal(out1, out2) {
		t.Fatalf("Codex merge not byte-idempotent:\n--- run1 ---\n%s\n--- run2 ---\n%s", out1, out2)
	}
}
