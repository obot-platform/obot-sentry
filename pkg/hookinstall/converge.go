package hookinstall

import (
	"bytes"
	"fmt"

	"github.com/obot-platform/obot-sentry/pkg/enforce"
	"github.com/obot-platform/obot-sentry/pkg/localagent"
	"github.com/tailscale/hujson"
)

// This file is the per-agent convergence layer: it wires the format-neutral
// editing primitives (jsonconfig.go, tomlconfig.go) into one merge function per
// agent, then decides the per-destination Status by comparing the merged result
// to the original.
//
// Every merge follows the same shape:
//
//   - a missing or empty file is written fresh from the typed desired document,
//     using the canonical two-space serializer so a new file is human-readable;
//   - an existing file is edited in place — obot-sentry-owned entries are filtered
//     out and exactly one current desired entry is added back per event, so a
//     stale entry (a previous obot-sentry path) is replaced and duplicate owned
//     entries collapse to one, while third-party entries and formatting survive;
//   - the result is compared to the original to report installed, updated, or
//     unchanged, and an already-current file is not rewritten (so a second run
//     is byte-identical).
//
// Ownership of an existing entry is the only signal for updated-vs-installed:
// a file that already carried an obot-sentry hook is "updated", one that did not is
// "installed".

// ---

// mergeOutcome is the in-memory result of merging one destination: the bytes to
// write, the status to report, how many duplicate owned entries were collapsed,
// and whether the file actually needs writing (false when already current).
type mergeOutcome struct {
	data   []byte
	status Status
	dupes  int
	write  bool
}

// mergeConfig produces the desired bytes for one destination from its existing
// content (nil when the file is absent). It dispatches to the per-agent writer
// by format and agent. A parse error (a malformed existing document or an
// incompatible field type) is returned so the caller aborts before writing.
func mergeConfig(d Destination, existing []byte, exe, goos string, enforcing bool) (mergeOutcome, error) {
	switch {
	case d.Format == FormatTOML:
		return mergeCodex(existing, exe, goos, enforcing)
	case d.Agent == localagent.ClaudeCode:
		return mergeClaude(existing, exe, goos, enforcing)
	case d.Agent == localagent.Cursor:
		return mergeCursor(existing, exe, goos, enforcing)
	case d.Agent == localagent.VSCode && d.Format == FormatJSONC:
		return mergeVSCodeSettings(existing)
	case d.Agent == localagent.VSCode:
		return mergeVSCodeHook(existing, exe, goos)
	default:
		return mergeOutcome{}, fmt.Errorf("no merge writer for destination %q", d.Label)
	}
}

// isEmptyConfig reports whether data holds no JSON/TOML content — absent,
// whitespace-only, or just a BOM — so it is written fresh rather than edited.
func isEmptyConfig(data []byte) bool {
	return len(bytes.TrimSpace(bytes.TrimPrefix(data, []byte(utf8BOM)))) == 0
}

// mergeJSONHook is the shared JSON hook-file merge. For an empty file it emits
// newDoc through the canonical serializer; otherwise it parses the existing
// document, applies mutate (which filters owned entries and adds the desired
// ones, reporting the duplicates collapsed and whether an owned entry existed),
// and reports unchanged when the edited result is semantically identical to the
// original so the file is left untouched.
func mergeJSONHook(existing []byte, newDoc any, mutate func(*hujson.Object) (dupes int, hadOwned bool, err error)) (mergeOutcome, error) {
	if isEmptyConfig(existing) {
		b, err := marshalHookJSON(newDoc)
		if err != nil {
			return mergeOutcome{}, err
		}
		return mergeOutcome{data: b, status: StatusInstalled, write: true}, nil
	}

	cfg, err := parseJSONConfig(existing)
	if err != nil {
		return mergeOutcome{}, err
	}
	obj, err := cfg.object()
	if err != nil {
		return mergeOutcome{}, err
	}
	dupes, hadOwned, err := mutate(obj)
	if err != nil {
		return mergeOutcome{}, err
	}
	packed := cfg.pack()

	same, err := jsonSemanticEqual(existing, packed)
	if err != nil {
		return mergeOutcome{}, err
	}
	if same {
		return mergeOutcome{data: packed, status: StatusUnchanged}, nil
	}
	status := StatusInstalled
	if hadOwned {
		status = StatusUpdated
	}
	return mergeOutcome{data: packed, status: status, dupes: dupes, write: true}, nil
}

// mergeEventArray filters the obot-sentry-owned entries out of one event's array and
// appends the single desired entry, returning the duplicates collapsed (owned
// entries removed beyond the one we re-add) and whether any owned entry existed.
// filter is the layout-specific remover: filterDirectOwned for Cursor/VS Code,
// filterNestedOwned for Claude's matcher groups.
func mergeEventArray(hooks *hujson.Object, event string, desired any, filter func(*hujson.Array) int) (dupes int, hadOwned bool, err error) {
	arr, err := getOrCreateArrayMember(hooks, event)
	if err != nil {
		return 0, false, err
	}
	removed := filter(arr)
	entry, err := jsonValueFromGo(desired)
	if err != nil {
		return 0, false, err
	}
	arrayAppend(arr, entry)
	return max(0, removed-1), removed > 0, nil
}

// mergeClaude converges Claude Code's nested settings.json: one matcher-group
// entry per event, each carrying the obot-sentry command as an inner hook.
func mergeClaude(existing []byte, exe, goos string, enforcing bool) (mergeOutcome, error) {
	desired := desiredClaude(exe, goos, enforcing)
	return mergeJSONHook(existing, desired, func(obj *hujson.Object) (int, bool, error) {
		hooks, err := getOrCreateObjectMember(obj, "hooks")
		if err != nil {
			return 0, false, err
		}
		events := []struct {
			key   string
			group claudeMatcherGroup
		}{
			{"PostToolUse", desired.Hooks.PostToolUse[0]},
			{"PostToolUseFailure", desired.Hooks.PostToolUseFailure[0]},
		}
		for i, event := range preToolEvents(localagent.ClaudeCode, enforcing) {
			events = append(events, struct {
				key   string
				group claudeMatcherGroup
			}{event, desired.Hooks.PreToolUse[i]})
		}
		dupes, hadOwned := 0, false
		for _, ev := range events {
			d, owned, err := mergeEventArray(hooks, ev.key, ev.group, filterNestedOwned)
			if err != nil {
				return 0, false, err
			}
			dupes += d
			hadOwned = hadOwned || owned
		}
		return dupes, hadOwned, nil
	})
}

// mergeCursor converges Cursor's hooks.json: direct command entries in each
// event array, plus the supported schema version forced to 1.
func mergeCursor(existing []byte, exe, goos string, enforcing bool) (mergeOutcome, error) {
	desired := desiredCursor(exe, goos, enforcing)
	return mergeJSONHook(existing, desired, func(obj *hujson.Object) (int, bool, error) {
		objectSet(obj, "version", hujson.Value{Value: hujson.Int(cursorVersion)})
		hooks, err := getOrCreateObjectMember(obj, "hooks")
		if err != nil {
			return 0, false, err
		}
		events := []struct {
			key   string
			entry cursorHook
		}{
			{"postToolUse", desired.Hooks.PostToolUse[0]},
			{"postToolUseFailure", desired.Hooks.PostToolUseFailure[0]},
		}
		for _, entry := range desired.Hooks.BeforeMCPExecution {
			events = append(events, struct {
				key   string
				entry cursorHook
			}{string(enforce.EventCursorBeforeMCPExecution), entry})
		}
		for _, entry := range desired.Hooks.PreToolUse {
			events = append(events, struct {
				key   string
				entry cursorHook
			}{string(enforce.EventCursorPreToolUse), entry})
		}
		dupes, hadOwned := 0, false
		for _, ev := range events {
			d, owned, err := mergeEventArray(hooks, ev.key, ev.entry, filterDirectOwned)
			if err != nil {
				return 0, false, err
			}
			dupes += d
			hadOwned = hadOwned || owned
		}
		return dupes, hadOwned, nil
	})
}

// mergeVSCodeHook converges the dedicated Copilot obot-sentry.json: a single direct
// PostToolUse command entry.
func mergeVSCodeHook(existing []byte, exe, goos string) (mergeOutcome, error) {
	desired := desiredVSCode(exe, goos)
	return mergeJSONHook(existing, desired, func(obj *hujson.Object) (int, bool, error) {
		hooks, err := getOrCreateObjectMember(obj, "hooks")
		if err != nil {
			return 0, false, err
		}
		return mergeEventArray(hooks, "PostToolUse", desired.Hooks.PostToolUse[0], filterDirectOwned)
	})
}

// mergeVSCodeSettings converges the JSONC VS Code user settings: it merges the
// obot-sentry-owned values under chat.hookFilesLocations (enable the Copilot hook
// directory, disable the three default Claude locations) without disturbing any
// custom location the operator configured. Unlike the hook files, these values
// carry no ownership marker, so an existing managed key is the updated-vs-
// installed signal.
func mergeVSCodeSettings(existing []byte) (mergeOutcome, error) {
	if isEmptyConfig(existing) {
		b, err := marshalHookJSON(newVSCodeSettings())
		if err != nil {
			return mergeOutcome{}, err
		}
		return mergeOutcome{data: b, status: StatusInstalled, write: true}, nil
	}

	cfg, err := parseJSONConfig(existing)
	if err != nil {
		return mergeOutcome{}, err
	}
	obj, err := cfg.object()
	if err != nil {
		return mergeOutcome{}, err
	}
	loc, err := getOrCreateObjectMember(obj, "chat.hookFilesLocations")
	if err != nil {
		return mergeOutcome{}, err
	}
	hadKey := false
	for _, sv := range desiredVSCodeHookLocations() {
		if objectMember(loc, sv.Key) != nil {
			hadKey = true
		}
		objectSet(loc, sv.Key, hujson.Value{Value: hujson.Bool(sv.Value)})
	}
	packed := cfg.pack()

	same, err := jsonSemanticEqual(existing, packed)
	if err != nil {
		return mergeOutcome{}, err
	}
	if same {
		return mergeOutcome{data: packed, status: StatusUnchanged}, nil
	}
	status := StatusInstalled
	if hadKey {
		status = StatusUpdated
	}
	return mergeOutcome{data: packed, status: status, write: true}, nil
}

// mergeCodex converges Codex's requirements.toml through the decode/re-encode
// cycle: force [features].hooks = true, filter the obot-sentry-owned inner hooks out
// of the PostToolUse array-of-tables, append the one desired group, and
// re-encode. The comparison is against the re-encoded original — the encoder
// normalizes formatting on first touch, so an already-normalized file with the
// desired hook re-encodes identically and reports unchanged.
func mergeCodex(existing []byte, exe, goos string, enforcing bool) (mergeOutcome, error) {
	m, err := parseCodexTOML(existing)
	if err != nil {
		return mergeOutcome{}, err
	}
	// Re-encode before mutating to get the normalized original: comparing the
	// merged output against this (not the raw bytes) means a hand-formatted file
	// that already holds the desired hook still reports unchanged.
	normalized, err := encodeCodexTOML(m)
	if err != nil {
		return mergeOutcome{}, err
	}

	if err := setCodexFeaturePins(m, codexFeaturePins()); err != nil {
		return mergeOutcome{}, err
	}

	// The desired groups, keyed by the event array they belong in. An event with
	// no desired group is absent from this map and is therefore never filtered or
	// rewritten, which is what leaves a pre-tool array alone on a run that is not
	// installing enforcement.
	desired := desiredCodex(exe, goos, enforcing)
	events := []struct {
		key    string
		groups []codexHookGroup
	}{{"PostToolUse", desired.PostToolUse}}
	for i, event := range preToolEvents(localagent.Codex, enforcing) {
		events = append(events, struct {
			key    string
			groups []codexHookGroup
		}{event, desired.PreToolUse[i : i+1]})
	}

	removed := 0
	for _, ev := range events {
		r, err := filterCodexOwned(m, ev.key)
		if err != nil {
			return mergeOutcome{}, err
		}
		removed += r
	}

	hooks, ok := m["hooks"].(map[string]any)
	if !ok {
		if m["hooks"] != nil {
			return mergeOutcome{}, fmt.Errorf("codex [hooks] is %T, want a table", m["hooks"])
		}
		hooks = map[string]any{}
		m["hooks"] = hooks
	}
	for _, ev := range events {
		groups, err := tableSlice(hooks[ev.key])
		if err != nil {
			return mergeOutcome{}, fmt.Errorf("codex [[hooks.%s]] %w", ev.key, err)
		}
		hooks[ev.key] = append(groups, codexDesiredGroups(ev.groups)...)
	}

	out, err := encodeCodexTOML(m)
	if err != nil {
		return mergeOutcome{}, err
	}
	if bytes.Equal(out, normalized) {
		return mergeOutcome{data: out, status: StatusUnchanged}, nil
	}
	status := StatusInstalled
	if removed > 0 {
		status = StatusUpdated
	}
	return mergeOutcome{data: out, status: status, dupes: max(0, removed-1), write: true}, nil
}
