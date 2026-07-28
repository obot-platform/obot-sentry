package enforce

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
	"github.com/obot-platform/obot-sentry/pkg/toolkind"
	"github.com/obot-platform/obot/apiclient/types"
)

// preToolPayload is the pre-tool envelope Claude Code and Codex share.
type preToolPayload struct {
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	CWD       string `json:"cwd,omitempty"`
}

type cursorPreToolPayload struct {
	ToolName       string `json:"tool_name"`
	ConversationID string `json:"conversation_id,omitempty"`
	GenerationID   string `json:"generation_id,omitempty"`
}

// cursorMCPPayload is Cursor's beforeMCPExecution envelope, the only pre-tool
// payload that names the target server.
//
// Cursor sends neither url — not even for a Streamable HTTP server — nor a usable
// cwd, so neither is modeled: an HTTP server resolves through the display-name
// lookup to the {url: …} entry in mcp.json, and WorkspaceRoots is the project
// context.
//
// Cursor's docs claim that this hook sends the server URL or command, but it does not.
type cursorMCPPayload struct {
	ToolName       string   `json:"tool_name"`
	MCPServerName  string   `json:"mcp_server_name"`
	WorkspaceRoots []string `json:"workspace_roots,omitempty"`
	ConversationID string   `json:"conversation_id,omitempty"`
	GenerationID   string   `json:"generation_id,omitempty"`
}

func decodePayload(raw []byte, out any) error {
	if err := json.Unmarshal(bytes.TrimPrefix(raw, utf8BOM), out); err != nil {
		return fmt.Errorf("invalid pre-tool hook payload: %w", err)
	}
	return nil
}

// classification is what a tool name and its payload say about a call, before any
// configuration file is read.
type classification struct {
	Kind       string
	Tool       string
	ServerName string
	Skip       bool
	// Splits holds every way an mcp__ tool name could divide into a server and tool
	// half, when there is more than one. Empty when the name divides one way, and for
	// Cursor, whose payload names the server outright. Tool and ServerName always
	// hold the first reading, which is the one both agents' own parsers take.
	Splits []mcpSplit
}

// mcpSplit is one reading of the server and tool halves of an mcp__ tool name.
type mcpSplit struct {
	Server string
	Tool   string
}

// mcpSplits returns every way rest divides into a non-empty server and a non-empty
// tool at a "__" boundary, shortest server first.
//
// Usually there is exactly one. There is more than one when the server half itself
// contains "__", which happens because "__" is both the delimiter and a legal
// sequence inside a namespace: a Codex key of "a..b" is namespaced "a__b", so
// "mcp__a__b__echo" reads as either (a, b__echo) or (a__b, echo).
func mcpSplits(rest string) []mcpSplit {
	var out []mcpSplit
	for i := 1; i+2 < len(rest); i++ {
		if rest[i] == '_' && rest[i+1] == '_' {
			out = append(out, mcpSplit{Server: rest[:i], Tool: rest[i+2:]})
		}
	}
	return out
}

// mcpToolPrefix namespaces MCP tools for Claude Code and Codex.
const mcpToolPrefix = "mcp__"

// cursorMCPToolPrefix namespaces MCP tools on Cursor's preToolUse event.
const cursorMCPToolPrefix = "MCP:"

var cursorToolKinds = map[string]string{
	"Shell":  toolkind.KindShell,
	"Read":   toolkind.KindRead,
	"Grep":   toolkind.KindRead,
	"Write":  toolkind.KindWrite,
	"Delete": toolkind.KindWrite,
	"Task":   toolkind.KindTask,
}

// classifyPreTool classifies a Claude Code or Codex pre-tool call.
func classifyPreTool(toolName string) classification {
	kind := toolkind.Kind(toolName)
	if kind != toolkind.KindMCP {
		return classification{Kind: kind, Tool: toolName}
	}

	rest, ok := cutPrefixFold(toolName, mcpToolPrefix)
	if !ok {
		// MCP-shaped by some other convention (mcp: or a single underscore) and so
		// carrying no server we can name. Still an MCP call: the resolver reports it
		// as unidentified rather than letting it through as a built-in tool.
		return classification{Kind: kind, Tool: toolName}
	}
	splits := mcpSplits(rest)
	if len(splits) == 0 {
		// No tool half. The whole remainder is the server, and the tool reported is
		// the name as it arrived — a tool-scoped allowlist entry cannot match it,
		// which fails closed.
		return classification{Kind: kind, Tool: toolName, ServerName: rest}
	}
	// The first reading is the shortest server half, which is what both agents' own
	// parsers take. When there is more than one, the resolver decides between them
	// against the configuration rather than assuming this one — see resolveSplits.
	class := classification{Kind: kind, Tool: splits[0].Tool, ServerName: splits[0].Server}
	if len(splits) > 1 {
		class.Splits = splits
	}
	return class
}

// classifyCursorPreTool classifies a Cursor preToolUse call.
//
// An MCP-shaped name is skipped: beforeMCPExecution already decided that call, so
// deciding again would double-log, and this event's tool name carries no server
// hint to decide on.
func classifyCursorPreTool(toolName string) classification {
	if tool, ok := cutPrefixFold(toolName, cursorMCPToolPrefix); ok {
		return classification{Kind: toolkind.KindMCP, Tool: tool, Skip: true}
	}
	if kind, ok := cursorToolKinds[toolName]; ok {
		return classification{Kind: kind, Tool: toolName}
	}
	// Keep the heuristic fallback so a tool Cursor adds later is still classified
	// rather than dropped.
	return classification{Kind: toolkind.Kind(toolName), Tool: toolName}
}

// classifyCursorMCP classifies a Cursor beforeMCPExecution call. Its tool name is
// the bare tool and the server comes from the payload's display name.
func classifyCursorMCP(toolName, serverDisplayName string) classification {
	return classification{
		Kind:       toolkind.KindMCP,
		Tool:       toolName,
		ServerName: serverDisplayName,
	}
}

// cutPrefixFold removes prefix from s, comparing case-insensitively.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}

// Call is a normalized tool call plus the resolution evidence behind it.
type Call struct {
	// Request is the parameter-free decision request to submit.
	Request types.EnforcementDecisionRequest
	// Skip reports that no decision is needed. It is set only for a Cursor
	// preToolUse call on an MCP-shaped tool name, which beforeMCPExecution has
	// already decided.
	Skip bool
	// Trace lists the configuration sources the resolver consulted. Empty for a
	// non-MCP call, which has nothing to resolve.
	Trace []TraceStep
}

// normalizeCall decodes a native pre-tool payload and resolves it into the request
// Obot decides on.
//
// An error means the payload could not be understood at all, which the caller
// turns into a deny. Failing to *resolve* the target is not an error: it is
// reported on the request as unresolved with a specific reason, and Obot decides.
func normalizeCall(env Env, agent localagent.Agent, event Event, raw []byte) (Call, error) {
	if agent == localagent.Cursor && event == EventCursorBeforeMCPExecution {
		return normalizeCursorMCP(env, raw)
	}
	if agent == localagent.Cursor {
		return normalizeCursorPreTool(env, raw)
	}
	return normalizePreTool(env, agent, raw)
}

// normalizePreTool handles the Claude Code and Codex PreToolUse event.
func normalizePreTool(env Env, agent localagent.Agent, raw []byte) (Call, error) {
	var hook preToolPayload
	if err := decodePayload(raw, &hook); err != nil {
		return Call{}, err
	}
	toolName := strings.TrimSpace(hook.ToolName)
	if toolName == "" {
		return Call{}, errors.New("pre-tool hook payload has no tool_name")
	}

	class := classifyPreTool(toolName)
	return buildCall(env, agent, class, ResolveRequest{
		Agent:      agent,
		ServerName: class.ServerName,
		CWD:        hook.CWD,
	}), nil
}

// normalizeCursorPreTool handles Cursor's preToolUse event.
func normalizeCursorPreTool(env Env, raw []byte) (Call, error) {
	var hook cursorPreToolPayload
	if err := decodePayload(raw, &hook); err != nil {
		return Call{}, err
	}
	toolName := strings.TrimSpace(hook.ToolName)
	if toolName == "" {
		return Call{}, errors.New("pre-tool hook payload has no tool_name")
	}

	class := classifyCursorPreTool(toolName)
	if class.Skip {
		return Call{Skip: true}, nil
	}
	// No project context is threaded in: this event never names a server, and
	// resolve() returns before touching a file when the server name is empty.
	return buildCall(env, localagent.Cursor, class, ResolveRequest{Agent: localagent.Cursor}), nil
}

// normalizeCursorMCP handles Cursor's beforeMCPExecution event.
func normalizeCursorMCP(env Env, raw []byte) (Call, error) {
	var hook cursorMCPPayload
	if err := decodePayload(raw, &hook); err != nil {
		return Call{}, err
	}
	toolName := strings.TrimSpace(hook.ToolName)
	if toolName == "" {
		return Call{}, errors.New("pre-tool hook payload has no tool_name")
	}

	// An absent mcp_server_name leaves the server unnamed, which resolve() reports
	// as unresolved rather than deciding on: Obot gets the call and decides.
	class := classifyCursorMCP(toolName, strings.TrimSpace(hook.MCPServerName))
	return buildCall(env, localagent.Cursor, class, ResolveRequest{
		Agent:          localagent.Cursor,
		ServerName:     class.ServerName,
		WorkspaceRoots: hook.WorkspaceRoots,
	}), nil
}

// buildCall assembles the decision request, resolving the target server for an
// MCP call. Non-MCP calls have nothing to resolve and so are never unresolved:
// they always get a real verdict from the built-in-tools toggle.
func buildCall(env Env, agent localagent.Agent, class classification, req ResolveRequest) Call {
	call := Call{Request: types.EnforcementDecisionRequest{
		Agent: wireAgent(agent),
		Tool:  class.Tool,
		Kind:  class.Kind,
	}}
	if class.Kind != toolkind.KindMCP {
		return call
	}

	res, tool := resolveSplits(env, class, req)
	call.Trace = res.Trace
	call.Request.Tool = tool
	call.Request.ServerName = res.ServerName
	call.Request.Server = res.Identity
	call.Request.Unresolved = res.Unresolved
	if res.Unresolved {
		call.Request.UnresolvedReason = res.Reason
	}
	return call
}

// resolveSplits resolves the target server, trying every way the tool name could
// divide into a server and a tool. It returns the resolution and the tool name from
// the reading that won.
//
// Almost every name divides one way and this is a single Resolve. When more than one
// reading names a configured server we cannot tell which one ran, and the difference
// is not cosmetic: given servers "dot" and "dot..dot", a call to the latter's tool
// arrives as "mcp__dot__dot__echo" and the leftmost reading names "dot". If "dot" is
// allowlisted, taking that reading reports a permitted server for a call that went
// somewhere else. Both agents' own parsers do take the leftmost; obot-sentry is the
// one for whom that is a bypass rather than a mislabel.
//
// Every candidate is resolved, without a cap. A name with many "__" runs costs a
// configuration read per candidate, which is real but bounded by the name's length
// and small beside the network round trip this call is about to make — and capping
// would mean silently not considering a reading, which is the failure being fixed.
func resolveSplits(env Env, class classification, req ResolveRequest) (Resolution, string) {
	if len(class.Splits) <= 1 {
		return Resolve(env, req), class.Tool
	}

	var (
		trace    []TraceStep
		first    Resolution
		resolved []Resolution
		tools    []string
	)
	for i, split := range class.Splits {
		candidate := req
		candidate.ServerName = split.Server
		res := Resolve(env, candidate)
		trace = append(trace, res.Trace...)
		if i == 0 {
			first = res
		}
		if !res.Unresolved {
			resolved = append(resolved, res)
			tools = append(tools, split.Tool)
		}
	}

	switch len(resolved) {
	case 0:
		// Nothing matched under any reading. Report the leftmost, which is what the
		// agent itself believes it called.
		first.Trace = trace
		return first, class.Tool
	case 1:
		resolved[0].Trace = trace
		return resolved[0], tools[0]
	default:
		names := make([]string, 0, len(resolved))
		for _, res := range resolved {
			names = append(names, res.ServerName)
		}
		out := ambiguousToolName(req.Agent, class.ServerName, names)
		out.Trace = trace
		return out, class.Tool
	}
}
