package hookinstall

import (
	"bytes"
	"fmt"

	"github.com/BurntSushi/toml"
)

// This file is the Codex requirements.toml editor. Codex is the highest-risk
// merge because obot-sentry hooks and unrelated managed requirements share one file,
// so BurntSushi/toml is used as a decode/mutate/re-encode cycle rather than a
// span editor: decode the whole document into a generic map, mutate only the
// obot-sentry-owned parts, and re-encode.
//
// Accepted tradeoff: the re-encode preserves all unrelated *data* (keys, tables,
// array-of-tables entries, and values) but normalizes the whole file — it drops
// comments and rewrites key order (alphabetical), inline-table syntax, string
// quoting, indentation, and line endings. The encoder is deterministic and sorts
// keys, so a hand-edited file is reformatted once on the first run and every
// subsequent run is byte-identical and reports unchanged. A malformed file is
// never treated as empty; the decode error aborts preflight.

// ---

// codexTOMLDoc is a decoded requirements.toml as a generic map. Nested tables
// decode to map[string]any and arrays-of-tables to []map[string]any.
type codexTOMLDoc map[string]any

// parseCodexTOML decodes existing requirements.toml bytes. Empty (or
// whitespace-only) input becomes an empty document. Malformed input returns an
// error so the caller aborts preflight rather than overwriting the file.
func parseCodexTOML(data []byte) (codexTOMLDoc, error) {
	m := codexTOMLDoc{}
	if len(bytes.TrimSpace(data)) == 0 {
		return m, nil
	}
	if _, err := toml.Decode(string(data), &m); err != nil {
		return nil, fmt.Errorf("parsing Codex requirements.toml: %w", err)
	}
	return m, nil
}

// encodeCodexTOML re-encodes the document. BurntSushi's encoder sorts keys and is
// deterministic, so once a hand-formatted file is normalized on the first run the
// output is byte-stable.
func encodeCodexTOML(m codexTOMLDoc) ([]byte, error) {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(map[string]any(m)); err != nil {
		return nil, fmt.Errorf("encoding Codex requirements.toml: %w", err)
	}
	return b.Bytes(), nil
}

// setCodexFeaturePins forces each pinned [features] value, creating the
// [features] table if absent. One operation covers replacing a value the user
// set, inserting into an existing table, and creating the table. It errors when
// an existing "features" value is not a table.
//
// Only the pinned keys are touched: any other feature the user has configured is
// left exactly as it is, because obot-sentry has no business deciding the rest of
// their Codex configuration.
func setCodexFeaturePins(m codexTOMLDoc, pins []codexFeaturePin) error {
	feats, ok := m["features"].(map[string]any)
	if !ok {
		if m["features"] != nil {
			return fmt.Errorf("codex [features] is %T, want a table", m["features"])
		}
		feats = make(map[string]any, len(pins))
		m["features"] = feats
	}
	for _, pin := range pins {
		feats[pin.Key] = pin.Value
	}
	return nil
}

// tableSlice coerces a decoded array-of-tables value into
// []map[string]any. BurntSushi decodes `[[...]]` groups to that concrete
// type, but an inline array can decode to []any; both are accepted. A
// non-array or an array holding a non-table entry is an incompatible type the
// caller must not silently overwrite, so it returns an error.
func tableSlice(v any) ([]map[string]any, error) {
	switch s := v.(type) {
	case nil:
		return nil, nil
	case []map[string]any:
		return s, nil
	case []any:
		out := make([]map[string]any, 0, len(s))
		for _, e := range s {
			m, ok := e.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("has a non-table entry (%T)", e)
			}
			out = append(out, m)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("is %T, want an array of tables", v)
	}
}

// filterInnerHooks removes inner hooks whose command matches owned from a
// decoded slice, preserving all other entries.
func filterInnerHooks(inner []map[string]any, owned func(string) bool) (removed int, kept []map[string]any) {
	kept = inner[:0]
	for _, h := range inner {
		if cmd, ok := h["command"].(string); ok && owned(cmd) {
			removed++
			continue
		}
		kept = append(kept, h)
	}
	return removed, kept
}

// codexDesiredGroups converts typed Codex hook groups into the decoded map shape
// appended to one hooks.<event> array-of-tables. Integer values use int64 to
// match how BurntSushi decodes a re-read document, so the appended group is
// byte-stable across a decode/re-encode cycle. command_windows is only present
// when the desired hook set it (Windows).
func codexDesiredGroups(desired []codexHookGroup) []map[string]any {
	groups := make([]map[string]any, 0, len(desired))
	for _, g := range desired {
		inner := make([]map[string]any, 0, len(g.Hooks))
		for _, h := range g.Hooks {
			hm := map[string]any{
				"type":          h.Type,
				"command":       h.Command,
				"timeout":       int64(h.Timeout),
				"statusMessage": h.StatusMessage,
			}
			if h.CommandWindows != "" {
				hm["command_windows"] = h.CommandWindows
			}
			inner = append(inner, hm)
		}
		groups = append(groups, map[string]any{"matcher": g.Matcher, "hooks": inner})
	}
	return groups
}

// filterCodexOwned removes obot-sentry-managed inner hooks from each group of the
// hooks.<event> array-of-tables (Codex's nested layout mirrors Claude's:
// [[hooks.PostToolUse]] groups each holding [[hooks.PostToolUse.hooks]] command
// entries). A group is dropped only when our removal emptied its inner list; the
// event key is removed when no groups remain. Third-party groups and inner hooks
// survive. Returns the number of inner hooks removed. An incompatible type on the
// hooks table or the event array returns an error.
func filterCodexHooks(m codexTOMLDoc, event string, owned func(string) bool) (int, error) {
	hooksVal, ok := m["hooks"]
	if !ok {
		return 0, nil
	}
	hooks, ok := hooksVal.(map[string]any)
	if !ok {
		return 0, fmt.Errorf("codex [hooks] is %T, want a table", hooksVal)
	}
	groups, err := tableSlice(hooks[event])
	if err != nil {
		return 0, fmt.Errorf("codex [[hooks.%s]] %w", event, err)
	}
	removed := 0
	kept := groups[:0]
	for _, grp := range groups {
		if innerVal, has := grp["hooks"]; has {
			inner, err := tableSlice(innerVal)
			if err != nil {
				return 0, fmt.Errorf("codex [[hooks.%s.hooks]] %w", event, err)
			}
			r, keptInner := filterInnerHooks(inner, owned)
			removed += r
			if r > 0 && len(keptInner) == 0 {
				continue // group emptied by removing our hooks; drop it
			}
			grp["hooks"] = keptInner
		}
		kept = append(kept, grp)
	}
	if len(kept) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = kept
	}
	return removed, nil
}

func filterCodexOwned(m codexTOMLDoc, event string) (int, error) {
	return filterCodexHooks(m, event, isOwnedCommand)
}

// filterAllCodexHooks applies an ownership predicate to every event under the
// Codex hooks table, including event names from older or newer agent versions.
func filterAllCodexHooks(m codexTOMLDoc, owned func(string) bool) (int, error) {
	hooksVal, ok := m["hooks"]
	if !ok {
		return 0, nil
	}
	hooks, ok := hooksVal.(map[string]any)
	if !ok {
		return 0, fmt.Errorf("codex [hooks] is %T, want a table", hooksVal)
	}
	removed := 0
	for event := range hooks {
		r, err := filterCodexHooks(m, event, owned)
		if err != nil {
			return 0, err
		}
		removed += r
	}
	return removed, nil
}
