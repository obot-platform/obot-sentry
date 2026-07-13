package scan

import (
	"encoding/json"
	"io/fs"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// readJSON reads and decodes a JSON file at rel into T. Returns the
// zero T and false on any error. Use a typed struct for T to get
// compile-time schema validation; use map[string]any when the schema is
// genuinely open-ended.
//
// Editor configs are commonly JSONC — VS Code, Zed, and OpenCode all
// write comments and trailing commas — so a strict-parse failure is
// retried with the JSONC syntax stripped.
func readJSON[T any](fsys fs.FS, rel string) (T, bool) {
	var out T
	data, err := fs.ReadFile(fsys, rel)
	if err != nil {
		return out, false
	}
	if err := json.Unmarshal(data, &out); err == nil {
		return out, true
	}
	var retry T
	if err := json.Unmarshal(stripJSONC(data), &retry); err != nil {
		var zero T
		return zero, false
	}
	return retry, true
}

// stripJSONC removes // and /* */ comments and trailing commas so
// JSONC documents parse with the strict JSON decoder. String contents
// are preserved verbatim.
func stripJSONC(data []byte) []byte {
	var (
		out               = make([]byte, 0, len(data))
		inString, escaped bool
		pendingComma      bool
	)
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i+1 < len(data) && data[i+1] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && (data[i] != '*' || data[i+1] != '/') {
				i++
			}
			i++
		case c == ',':
			// Hold the comma until the next significant character: a
			// closing brace/bracket makes it a trailing comma to drop.
			pendingComma = true
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			out = append(out, c)
		default:
			if pendingComma {
				if c != '}' && c != ']' {
					out = append(out, ',')
				}
				pendingComma = false
			}
			out = append(out, c)
			if c == '"' {
				inString = true
			}
		}
	}
	return out
}

// readYAML reads and decodes a YAML file at rel into T. See readJSON.
func readYAML[T any](fsys fs.FS, rel string) (T, bool) {
	var out T
	data, err := fs.ReadFile(fsys, rel)
	if err != nil {
		return out, false
	}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return out, false
	}
	return out, true
}

// readTOML reads and decodes a TOML file at rel into T. See readJSON.
func readTOML[T any](fsys fs.FS, rel string) (T, bool) {
	var out T
	data, err := fs.ReadFile(fsys, rel)
	if err != nil {
		return out, false
	}
	if _, err := toml.Decode(string(data), &out); err != nil {
		return out, false
	}
	return out, true
}
