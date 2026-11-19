package enforce

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
)

// Claude Code reads its configuration with exact-case keys and validates each
// server entry, skipping one it cannot use so the next scope answers for that
// name. encoding/json matches struct tags case-insensitively and lets a later
// variant overwrite an earlier key, so decoding Claude Code's files through
// tagged structs would let a decoy like "MCPServers" or "URL", which Claude Code
// never reads, answer for a name. Everything here looks keys up exactly, and keeps
// only the entries Claude Code would load.

// exactObject decodes a JSON object into its members, keyed exactly as written.
// A repeated key keeps its last value, as JSON.parse does.
func exactObject(data []byte) (map[string]json.RawMessage, bool) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return nil, false
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(data, &members) != nil {
		return nil, false
	}
	return members, true
}

// exactString reads member key as a string.
func exactString(members map[string]json.RawMessage, key string) (value string, present, ok bool) {
	raw, present := members[key]
	if !present {
		return "", false, true
	}
	if json.Unmarshal(raw, &value) != nil || bytes.TrimSpace(raw)[0] != '"' {
		return "", true, false
	}
	return value, true, true
}

// claudeServerEntry decodes one server entry the way Claude Code validates it,
// reporting false for an entry it skips.
//
// A stdio entry has no type, or type "stdio", and a string command; a url it also
// carries is ignored, because Claude Code runs the command. An "http", "sse", or
// "ws" entry has a string url, and a command it carries is ignored. Every other
// shape is skipped: a url with no type, an unknown type or one in the wrong case,
// the internal types only Claude Code itself registers, and fields of the wrong
// JSON type.
func claudeServerEntry(raw json.RawMessage) (mcpEntry, bool) {
	members, ok := exactObject(raw)
	if !ok {
		return mcpEntry{}, false
	}
	typ, typed, ok := exactString(members, "type")
	if !ok || typed && typ == "" {
		// A type that is present must name a transport; "" is not an absent type.
		return mcpEntry{}, false
	}
	switch typ {
	case "", "stdio":
		command, present, ok := exactString(members, "command")
		if !present || !ok {
			return mcpEntry{}, false
		}
		entry := mcpEntry{Command: command}
		if !optionalMember(members, "args", '[', &entry.Args) || !optionalMember(members, "env", '{', &entry.Environment) {
			return mcpEntry{}, false
		}
		return entry, true
	case "http", "sse", "ws":
		url, present, ok := exactString(members, "url")
		if !present || !ok {
			return mcpEntry{}, false
		}
		var headers map[string]string
		if !optionalMember(members, "headers", '{', &headers) {
			return mcpEntry{}, false
		}
		return mcpEntry{URL: url}, true
	default:
		return mcpEntry{}, false
	}
}

// optionalMember decodes member key into out when present. A present member must
// be the JSON kind open starts ('[' or '{'): Claude Code rejects null where it
// expects a list or a record, though Go would decode null into either.
func optionalMember(members map[string]json.RawMessage, key string, open byte, out any) bool {
	raw, present := members[key]
	if !present {
		return true
	}
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == open && json.Unmarshal(raw, out) == nil
}

// claudeServers decodes a server table, keeping only the entries Claude Code
// loads. Unlike decodeServers, a skipped entry is left out rather than kept as a
// match: Claude Code drops it, so a lower scope's definition of the name is the
// one the session runs.
func claudeServers(table map[string]json.RawMessage) serverSet {
	set := make(serverSet, len(table))
	for name, raw := range table {
		if entry, ok := claudeServerEntry(raw); ok {
			set[name] = entry
		}
	}
	return set
}

// objectMember reads member key of a JSON object as an object, such as a server
// table. ok is false when the member is present but not an object.
func objectMember(members map[string]json.RawMessage, key string) (table map[string]json.RawMessage, ok bool) {
	raw, present := members[key]
	if !present {
		return nil, true
	}
	return exactObject(raw)
}

// claudeJSONServers reads the mcpServers table of a Claude Code JSON
// configuration file, such as .mcp.json or managed-mcp.json.
//
// Claude Code skips such a file over claudeMCPConfigMaxBytes, so it is unusable
// here too: a project file then falls through, and managed-mcp.json still locks.
func claudeJSONServers(loader *configLoader, path string) func(context.Context) (serverSet, loadResult) {
	return func(ctx context.Context) (serverSet, loadResult) {
		var raw json.RawMessage
		if res := loader.loadJSONLimit(ctx, path, &raw, claudeMCPConfigMaxBytes); res != loadOK {
			return nil, res
		}
		members, ok := exactObject(raw)
		if !ok {
			return nil, loadUnusable
		}
		table, ok := objectMember(members, mcpServersKey)
		if !ok {
			return nil, loadUnusable
		}
		return claudeServers(table), loadOK
	}
}

// UnmarshalJSON decodes .claude.json by exact key.
func (c *claudeJSON) UnmarshalJSON(data []byte) error {
	members, ok := exactObject(data)
	if !ok {
		return errors.New(".claude.json is not a JSON object")
	}
	*c = claudeJSON{}
	c.MCPServers, _ = objectMember(members, mcpServersKey)
	if projects, ok := objectMember(members, "projects"); ok {
		c.Projects = make(map[string]claudeProjectConfig, len(projects))
		for key, raw := range projects {
			project, ok := exactObject(raw)
			if !ok {
				continue
			}
			servers, _ := objectMember(project, mcpServersKey)
			c.Projects[key] = claudeProjectConfig{MCPServers: servers}
		}
	}
	if raw, present := members["claudeAiMcpEverConnected"]; present {
		_ = json.Unmarshal(raw, &c.ClaudeAIMCPEverConnected)
	}
	return nil
}
