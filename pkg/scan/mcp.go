package scan

import (
	"encoding/json"
	"strings"

	"github.com/obot-platform/obot/apiclient/types"
)

// mcpServerSpec is the JSON shape that all JSON-format MCP server
// entries share. Optional fields default to zero — empty strings, nil
// maps/slices. Per-client config structs embed maps of these (e.g.
// claudeCodeConfig.MCPServers is map[string]mcpServerSpec).
//
// Env and Headers are kept as map[string]any because we only extract
// their keys; values are opaque to the scanner.
type mcpServerSpec struct {
	Type      string         `json:"type"`
	Transport string         `json:"transport"`
	Command   string         `json:"command"`
	Args      []string       `json:"args"`
	URL       string         `json:"url"`
	ServerURL string         `json:"serverUrl"`
	Env       map[string]any `json:"env"`
	Headers   map[string]any `json:"headers"`
	Enabled   *bool          `json:"enabled"` // pointer: absence ≠ false
}

// disabled reports whether the entry is explicitly switched off.
func (e mcpServerSpec) disabled() bool {
	return e.Enabled != nil && !*e.Enabled
}

// toServer converts a parsed entry into a wire DeviceScanMCPServer with
// the standard JSON-config transport rules: explicit type/transport
// (canonicalized), then sse if url/serverUrl is set, then stdio.
func (e mcpServerSpec) toServer(name, client, filePath, projectPath string) types.DeviceScanMCPServer {
	transport := normalizeTransport(e.Type, e.Transport, e.URL, e.ServerURL)
	url := firstNonEmpty(e.URL, e.ServerURL)
	return types.DeviceScanMCPServer{
		Client:      client,
		ProjectPath: projectPath,
		File:        filePath,
		Name:        name,
		Transport:   transport,
		Command:     e.Command,
		Args:        e.Args,
		URL:         url,
		EnvKeys:     sortedKeys(e.Env),
		HeaderKeys:  sortedKeys(e.Headers),
		ConfigHash:  mcpConfigHash(name, transport, e.Command, e.Args, url),
	}
}

// normalizeTransport returns the wire transport string. Explicit
// type/transport wins; otherwise sse if a URL is present; otherwise
// stdio.
func normalizeTransport(typeField, transportField, urlField, serverURLField string) string {
	if explicit := firstNonEmpty(typeField, transportField); explicit != "" {
		return canonicalTransport(explicit)
	}
	if firstNonEmpty(urlField, serverURLField) != "" {
		return "sse"
	}
	return "stdio"
}

// canonicalTransport lowercases and hyphenates an explicit transport
// value (`_`→`-`, `streamablehttp`→`streamable-http`).
func canonicalTransport(explicit string) string {
	n := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(explicit)), "_", "-")
	if n == "streamablehttp" {
		n = "streamable-http"
	}
	return n
}

// firstNonEmpty returns the first non-empty string in ss, or "".
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// emitJSONServers opens a JSON file at configRel and emits one MCP
// server per enabled entry under the given top-level dict key.
// projectPath is the project root for project-scope configs, "" for
// global. Used by JSON clients with no per-client quirks (Cursor,
// VS Code, Windsurf).
//
// Only the servers key is decoded into the typed shape, so unrelated
// top-level keys of any type (VS Code's `inputs` array, `$schema`,
// editor settings) never poison the parse. The returned slice is empty
// if the file is missing, malformed, or the servers key is absent.
func emitJSONServers(s *state, configRel, serversKey, client, projectPath string) []types.DeviceScanMCPServer {
	cfg, ok := readJSON[map[string]json.RawMessage](s.fsys, configRel)
	if !ok {
		return nil
	}
	configPath := s.addFileOrAbs(configRel)

	var servers map[string]mcpServerSpec
	if raw, found := cfg[serversKey]; found {
		_ = json.Unmarshal(raw, &servers)
	}
	out := make([]types.DeviceScanMCPServer, 0, len(servers))
	for _, name := range sortedKeys(servers) {
		if e := servers[name]; !e.disabled() {
			out = append(out, e.toServer(name, client, configPath, projectPath))
		}
	}
	return out
}
