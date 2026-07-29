package enforce

import (
	"strings"
	"unicode/utf16"
)

// This file transcribes how each agent derives a tool namespace from an MCP
// server name, so that matching can run FORWARD: transform every configuration
// key and compare the result to the name the hook reported.

type namespaceForm func(name string) string

const claudeAIDisplayPrefix = "claude.ai "

// formClaudeCode is Claude Code's transform.
//
// Hyphen and underscore are legal and survive; every other character becomes one
// underscore, except that a character above U+FFFF becomes TWO — Claude Code
// folds per UTF-16 code unit, and such a character is a surrogate pair, so it is
// replaced twice. Case is preserved and runs are NOT collapsed, so "a..b" is
// "a__b" — and so is "a🙂b", for that reason rather than this one.
//
// Names beginning with the claude.ai display prefix additionally collapse
// underscore runs and lose leading and trailing underscores — which is where a
// connector's "_2" disambiguation comes from. There is no suffix to strip.
func formClaudeCode(name string) string {
	folded := foldToNamespace(name, true)
	if strings.HasPrefix(name, claudeAIDisplayPrefix) {
		folded = collapseUnderscores(folded)
		folded = strings.Trim(folded, "_")
	}
	return folded
}

// formCodex is Codex's transform, transcribed from
// sanitize_responses_api_tool_name in codex-rs/codex-mcp/src/mcp/mod.rs at
// revision 3725f02c:
//
//	for c in name.chars() {
//	    if c.is_ascii_alphanumeric() || c == '_' { sanitized.push(c); }
//	    else { sanitized.push('_'); }
//	}
//	if sanitized.is_empty() { "_".to_string() } else { sanitized }
//
// This differs from formClaudeCode in two ways, only the first of which is about
// character classes:
//
//   - A hyphen is NOT legal here and folds to an underscore, which is why a
//     hyphenated Codex server key never matches its own configuration exactly.
//   - The Rust iterates Unicode scalar values (name.chars()), so a character above
//     U+FFFF yields ONE underscore. Claude Code iterates UTF-16 code units and
//     yields two. See formClaudeCode.
//
// Codex applies this to the tool half as well.
//
// Codex is not covered here in two cases, both of which leave the reported
// namespace unmatched by any configuration key, and so unresolved and decided by
// Obot rather than mistaken for another server:
//
//   - Two keys that fold alike are each suffixed with 12 hex digits of a SHA-1
//     over an internal identity string (tools.rs:152-172), so neither is reported
//     as the bare folded name.
//   - A total tool name over 64 characters is truncated with a hash suffix, and
//     past roughly 44 characters of key the namespace itself is truncated
//     (tools.rs:226, 269-287).
func formCodex(name string) string {
	if name == "" {
		// The Rust maps an empty name to "_"; the Go zero value would be "".
		return "_"
	}
	return foldToNamespace(name, false)
}

// foldToNamespace keeps ASCII alphanumerics and underscores, optionally keeps
// hyphens, and replaces every other character with a single underscore.
func foldToNamespace(name string, hyphenLegal bool) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		case r == '-' && hyphenLegal:
			b.WriteRune(r)
		case hyphenLegal && utf16.RuneLen(r) == 2:
			b.WriteString("__")
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func collapseUnderscores(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevUnderscore := false
	for _, r := range s {
		if r == '_' {
			if prevUnderscore {
				continue
			}
			prevUnderscore = true
		} else {
			prevUnderscore = false
		}
		b.WriteRune(r)
	}
	return b.String()
}
