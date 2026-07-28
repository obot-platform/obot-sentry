package hookinstall

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/tailscale/hujson"
)

// This file is the JSON/JSONC config editor. Claude, Cursor, and Copilot hook
// files are strict JSON; VS Code user settings are JSONC (comments, trailing
// commas). All are edited through the same comment- and whitespace-preserving
// HuJSON syntax tree: parse -> walk and mutate only the obot-sentry-owned nodes ->
// Pack. Pack reproduces every untouched byte of the input, which is what makes
// third-party settings, formatting, and a second byte-identical run all hold.
//
// A malformed document is never treated as {}, which would silently destroy
// user config; hujson.Parse returns an error and the caller aborts.

// ---

// marshalHookJSON serializes a desired JSON document to the canonical form used
// when writing a brand-new managed file: two-space indentation and a trailing
// newline. HTML escaping is disabled so the PowerShell call operator (`&`) and
// any `<`/`>` survive as literal characters rather than `&`-style escapes,
// matching the exact command strings the agents expect.
//
// This is only the new-file path. When an existing file is present, the merge
// path edits it through a comment- and whitespace-preserving JSON AST so
// unrelated settings, formatting, and third-party hooks are retained, rather
// than replacing it with this whole-document marshal.
func marshalHookJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// utf8BOM is a leading UTF-8 byte-order mark. hujson.Parse rejects it, so the
// editor strips it before parsing and re-prepends it on Pack, preserving files
// (occasionally VS Code settings) that carry one.
const utf8BOM = "\uFEFF"

// jsonConfig is a parsed, mutable JSON/JSONC document. It retains whether the
// source began with a BOM so pack() can reproduce it.
type jsonConfig struct {
	root hujson.Value
	bom  bool
}

// parseJSONConfig parses existing JSON/JSONC bytes into a mutable syntax tree.
// Empty (or whitespace-only) input becomes an empty object so a first install
// can populate it. Malformed input returns an error so the caller aborts
// preflight rather than overwriting the file.
func parseJSONConfig(data []byte) (*jsonConfig, error) {
	bom := bytes.HasPrefix(data, []byte(utf8BOM))
	body := data
	if bom {
		body = data[len(utf8BOM):]
	}
	if len(bytes.TrimSpace(body)) == 0 {
		v, err := hujson.Parse([]byte("{}"))
		if err != nil {
			return nil, err
		}
		return &jsonConfig{root: v, bom: bom}, nil
	}
	v, err := hujson.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("parsing JSON config: %w", err)
	}
	return &jsonConfig{root: v, bom: bom}, nil
}

// pack serializes the (possibly mutated) document, reproducing untouched input
// byte-for-byte and re-prepending a stripped BOM.
func (c *jsonConfig) pack() []byte {
	out := c.root.Pack()
	if c.bom {
		out = append([]byte(utf8BOM), out...)
	}
	return out
}

// object returns the document's root object, erroring if the top-level value is
// not a JSON object (an incompatible document the caller must not overwrite).
func (c *jsonConfig) object() (*hujson.Object, error) {
	obj, ok := c.root.Value.(*hujson.Object)
	if !ok {
		return nil, fmt.Errorf("config root is %s, want a JSON object", kindName(c.root.Value))
	}
	return obj, nil
}

// jsonSemanticEqual reports whether two JSON/JSONC documents are equal ignoring
// comments, whitespace, formatting, and a leading BOM (member order still
// matters). The convergence step uses it to detect an already-current file and
// skip the rewrite, which is what makes a second install byte-idempotent.
func jsonSemanticEqual(a, b []byte) (bool, error) {
	ma, err := hujson.Minimize(bytes.TrimPrefix(a, []byte(utf8BOM)))
	if err != nil {
		return false, err
	}
	mb, err := hujson.Minimize(bytes.TrimPrefix(b, []byte(utf8BOM)))
	if err != nil {
		return false, err
	}
	return bytes.Equal(ma, mb), nil
}

// --- syntax-tree accessors -------------------------------------------------

func kindName(v hujson.ValueTrimmed) string {
	switch v.(type) {
	case *hujson.Object:
		return "an object"
	case *hujson.Array:
		return "an array"
	case hujson.Literal:
		return "a literal"
	default:
		return "an unknown value"
	}
}

// memberName returns the unescaped name of an object member, or "" if the name
// is not a JSON string (which valid JSON never produces).
func memberName(m hujson.ObjectMember) string {
	if lit, ok := m.Name.Value.(hujson.Literal); ok {
		return lit.String()
	}
	return ""
}

// objectMember returns a mutable pointer to the value of obj's member named key,
// or nil if absent. Edits through the returned pointer are reflected on pack.
func objectMember(obj *hujson.Object, key string) *hujson.Value {
	for i := range obj.Members {
		if memberName(obj.Members[i]) == key {
			return &obj.Members[i].Value
		}
	}
	return nil
}

func asObject(v *hujson.Value) (*hujson.Object, bool) {
	o, ok := v.Value.(*hujson.Object)
	return o, ok
}

func asArray(v *hujson.Value) (*hujson.Array, bool) {
	a, ok := v.Value.(*hujson.Array)
	return a, ok
}

func cloneExtra(e hujson.Extra) hujson.Extra {
	if e == nil {
		return nil
	}
	c := make(hujson.Extra, len(e))
	copy(c, e)
	return c
}

// trailingIndent returns just the indentation of e's final line: its trailing
// run of spaces/tabs plus an immediately preceding newline, if any. Any comments
// e carries are dropped. It is used to position an appended array element or
// object member from a sibling's leading extra without copying a comment that
// sibling happens to carry (which cloning the whole extra would duplicate). The
// result is always whitespace-only, so a line comment like `// third-party hook`
// preceding the sibling never reappears in front of the entry we add.
func trailingIndent(e hujson.Extra) hujson.Extra {
	i := len(e)
	for i > 0 && (e[i-1] == ' ' || e[i-1] == '\t') {
		i--
	}
	if i > 0 && e[i-1] == '\n' {
		i--
	}
	return cloneExtra(e[i:])
}

// objectSet sets obj's member named key to value: it replaces an existing
// member's value in place, preserving that member's position and the whitespace
// after its colon, or appends a new member. A newly appended member inherits the
// leading whitespace of the first member so it lines up; in an empty object it is
// written inline.
func objectSet(obj *hujson.Object, key string, value hujson.Value) {
	for i := range obj.Members {
		if memberName(obj.Members[i]) == key {
			value.BeforeExtra = cloneExtra(obj.Members[i].Value.BeforeExtra)
			value.AfterExtra = nil
			obj.Members[i].Value = value
			return
		}
	}
	name := hujson.Value{Value: hujson.String(key)}
	if len(obj.Members) > 0 {
		name.BeforeExtra = cloneExtra(obj.Members[0].Name.BeforeExtra)
	}
	value.BeforeExtra = hujson.Extra(" ")
	value.AfterExtra = nil
	obj.Members = append(obj.Members, hujson.ObjectMember{Name: name, Value: value})
}

// getOrCreateObjectMember returns the object at obj[key], creating an empty
// object member when key is absent. It errors when key exists but is not an
// object, an incompatible type the caller must not silently overwrite.
func getOrCreateObjectMember(obj *hujson.Object, key string) (*hujson.Object, error) {
	if v := objectMember(obj, key); v != nil {
		o, ok := asObject(v)
		if !ok {
			return nil, fmt.Errorf("config member %q is %s, want a JSON object", key, kindName(v.Value))
		}
		return o, nil
	}
	created := &hujson.Object{}
	objectSet(obj, key, hujson.Value{Value: created})
	return created, nil
}

// getOrCreateArrayMember returns the array at obj[key], creating an empty array
// member when key is absent. It errors when key exists but is not an array.
func getOrCreateArrayMember(obj *hujson.Object, key string) (*hujson.Array, error) {
	if v := objectMember(obj, key); v != nil {
		a, ok := asArray(v)
		if !ok {
			return nil, fmt.Errorf("config member %q is %s, want a JSON array", key, kindName(v.Value))
		}
		return a, nil
	}
	created := &hujson.Array{}
	objectSet(obj, key, hujson.Value{Value: created})
	return created, nil
}

// arrayAppend appends elem as the last element of arr, inheriting a sibling's
// line indentation (never its comments) so the packed array stays readable and
// giving it no trailing comma. Callers building a fresh multi-line document use
// the new-file serializer instead; this is the merge-into-existing path.
func arrayAppend(arr *hujson.Array, elem hujson.Value) {
	elem.AfterExtra = nil
	if n := len(arr.Elements); n > 0 {
		elem.BeforeExtra = trailingIndent(arr.Elements[n-1].BeforeExtra)
	} else {
		elem.BeforeExtra = nil
	}
	arr.Elements = append(arr.Elements, elem)
}

// jsonValueFromGo converts a Go value into a syntax-tree node by marshaling it to
// compact JSON — HTML escaping disabled so `&`, `<`, and `>` survive literally —
// and parsing the result. Used to turn a typed desired hook entry into a node
// for insertion.
func jsonValueFromGo(v any) (hujson.Value, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return hujson.Value{}, err
	}
	return hujson.Parse(bytes.TrimRight(buf.Bytes(), "\n"))
}

// --- owned-entry filters ---------------------------------------------------

// entryCommand returns the "command" string of a hook-entry object and whether
// it was present as a JSON string.
func entryCommand(entry *hujson.Value) (string, bool) {
	obj, ok := asObject(entry)
	if !ok {
		return "", false
	}
	cv := objectMember(obj, "command")
	if cv == nil {
		return "", false
	}
	lit, ok := cv.Value.(hujson.Literal)
	if !ok || lit.Kind() != '"' {
		return "", false
	}
	return lit.String(), true
}

// filterDirectOwned removes array elements whose "command" is an obot-sentry-managed
// hook command, returning the count removed. This is the "direct" layout used by
// Cursor and VS Code, whose event arrays hold command entries directly.
// Third-party entries — and any element without an owned command — are preserved
// untouched, so unrelated hooks and their formatting survive.
func filterDirectOwned(arr *hujson.Array) int {
	removed := 0
	kept := arr.Elements[:0]
	for i := range arr.Elements {
		if cmd, ok := entryCommand(&arr.Elements[i]); ok && isOwnedCommand(cmd) {
			removed++
			continue
		}
		kept = append(kept, arr.Elements[i])
	}
	arr.Elements = kept
	return removed
}

// filterNestedOwned removes obot-sentry-managed inner hooks from each matcher group in
// arr — Claude Code's layout, where each element is {matcher, hooks:[...]}. It
// filters each group's inner "hooks" list with filterDirectOwned and drops a
// group only when our removal emptied its inner list, preserving groups that
// still hold third-party hooks (and any pre-existing empty group we did not
// touch). Returns the number of inner hooks removed.
func filterNestedOwned(arr *hujson.Array) int {
	removed := 0
	kept := arr.Elements[:0]
	for i := range arr.Elements {
		grp, ok := asObject(&arr.Elements[i])
		if !ok {
			kept = append(kept, arr.Elements[i])
			continue
		}
		if innerVal := objectMember(grp, "hooks"); innerVal != nil {
			if innerArr, ok := asArray(innerVal); ok {
				r := filterDirectOwned(innerArr)
				removed += r
				if r > 0 && len(innerArr.Elements) == 0 {
					continue // group emptied by removing our hooks; drop it
				}
			}
		}
		kept = append(kept, arr.Elements[i])
	}
	arr.Elements = kept
	return removed
}
