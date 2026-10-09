package enforce

import (
	"bytes"
	"context"
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
	// InvalidReason is a bounded-input classification failure. It is represented
	// as unresolved so Obot records and denies it like any unidentified MCP call.
	InvalidReason string
}

// mcpSplit is one reading of the server and tool halves of an mcp__ tool name.
type mcpSplit struct {
	Server string
	Tool   string
}

const maxMCPSplitCandidates = 32

// mcpSplits returns every way rest divides into a non-empty server and a non-empty
// tool at a "__" boundary, shortest server first.
//
// Usually there is exactly one. There is more than one when the server half itself
// contains "__", which happens because "__" is both the delimiter and a legal
// sequence inside a namespace: a Codex key of "a..b" is namespaced "a__b", so
// "mcp__a__b__echo" reads as either (a, b__echo) or (a__b, echo).
func mcpSplits(rest string) ([]mcpSplit, bool) {
	var out []mcpSplit
	for i := 1; i+2 < len(rest); i++ {
		if rest[i] == '_' && rest[i+1] == '_' {
			if len(out) == maxMCPSplitCandidates {
				return nil, false
			}
			out = append(out, mcpSplit{Server: rest[:i], Tool: rest[i+2:]})
		}
	}
	return out, true
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
	splits, ok := mcpSplits(rest)
	if !ok {
		return classification{
			Kind:          kind,
			Tool:          toolName,
			ServerName:    rest,
			InvalidReason: "the MCP server name could not be determined",
		}
	}
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
	return normalizeCallContext(context.Background(), env, agent, event, raw)
}

func normalizeCallContext(ctx context.Context, env Env, agent localagent.Agent, event Event, raw []byte) (Call, error) {
	if agent == localagent.Cursor && event == EventCursorBeforeMCPExecution {
		return normalizeCursorMCP(ctx, env, raw)
	}
	if agent == localagent.Cursor {
		return normalizeCursorPreTool(ctx, env, raw)
	}
	if agent == localagent.Kiro {
		return normalizeKiroPreTool(ctx, env, raw)
	}
	return normalizePreTool(ctx, env, agent, raw)
}

const (
	maxToolNameBytes      = 1024
	maxServerNameBytes    = 1024
	maxWorkingDirBytes    = 4096
	maxWorkspaceRoots     = 64
	maxWorkspaceRootBytes = 4096
)

func boundedField(name, value string, maximum int) error {
	if len(value) > maximum {
		return fmt.Errorf("pre-tool hook payload %s exceeds %d bytes", name, maximum)
	}
	return nil
}

func normalizePreTool(ctx context.Context, env Env, agent localagent.Agent, raw []byte) (Call, error) {
	var hook preToolPayload
	if err := decodePayload(raw, &hook); err != nil {
		return Call{}, err
	}
	toolName := strings.TrimSpace(hook.ToolName)
	if toolName == "" {
		return Call{}, errors.New("pre-tool hook payload has no tool_name")
	}
	if err := boundedField("tool_name", toolName, maxToolNameBytes); err != nil {
		return Call{}, err
	}
	if err := boundedField("cwd", hook.CWD, maxWorkingDirBytes); err != nil {
		return Call{}, err
	}

	class := classifyPreTool(toolName)
	return buildCall(ctx, env, agent, class, ResolveRequest{
		Agent:      agent,
		ServerName: class.ServerName,
		CWD:        hook.CWD,
	}), nil
}

func normalizeCursorPreTool(ctx context.Context, env Env, raw []byte) (Call, error) {
	var hook cursorPreToolPayload
	if err := decodePayload(raw, &hook); err != nil {
		return Call{}, err
	}
	toolName := strings.TrimSpace(hook.ToolName)
	if toolName == "" {
		return Call{}, errors.New("pre-tool hook payload has no tool_name")
	}
	if err := boundedField("tool_name", toolName, maxToolNameBytes); err != nil {
		return Call{}, err
	}

	class := classifyCursorPreTool(toolName)
	if class.Skip {
		return Call{Skip: true}, nil
	}
	// No project context is threaded in: this event never names a server, and
	// resolve() returns before touching a file when the server name is empty.
	return buildCall(ctx, env, localagent.Cursor, class, ResolveRequest{Agent: localagent.Cursor}), nil
}

func normalizeCursorMCP(ctx context.Context, env Env, raw []byte) (Call, error) {
	var hook cursorMCPPayload
	if err := decodePayload(raw, &hook); err != nil {
		return Call{}, err
	}
	toolName := strings.TrimSpace(hook.ToolName)
	if toolName == "" {
		return Call{}, errors.New("pre-tool hook payload has no tool_name")
	}
	if err := boundedField("tool_name", toolName, maxToolNameBytes); err != nil {
		return Call{}, err
	}
	if err := boundedField("mcp_server_name", hook.MCPServerName, maxServerNameBytes); err != nil {
		return Call{}, err
	}
	if len(hook.WorkspaceRoots) > maxWorkspaceRoots {
		return Call{}, fmt.Errorf("pre-tool hook payload has more than %d workspace_roots", maxWorkspaceRoots)
	}
	for _, root := range hook.WorkspaceRoots {
		if err := boundedField("workspace_roots entry", root, maxWorkspaceRootBytes); err != nil {
			return Call{}, err
		}
	}

	// An absent mcp_server_name leaves the server unnamed, which resolve() reports
	// as unresolved rather than deciding on: Obot gets the call and decides.
	class := classifyCursorMCP(toolName, strings.TrimSpace(hook.MCPServerName))
	return buildCall(ctx, env, localagent.Cursor, class, ResolveRequest{
		Agent:          localagent.Cursor,
		ServerName:     class.ServerName,
		WorkspaceRoots: hook.WorkspaceRoots,
	}), nil
}

// buildCall assembles the decision request, resolving the target server for an
// MCP call. Non-MCP calls have nothing to resolve and so are never unresolved:
// they always get a real verdict from the built-in-tools toggle.
func buildCall(ctx context.Context, env Env, agent localagent.Agent, class classification, req ResolveRequest) Call {
	call := Call{Request: types.EnforcementDecisionRequest{
		Agent: wireAgent(agent),
		Tool:  class.Tool,
		Kind:  class.Kind,
	}}
	if class.Kind != toolkind.KindMCP {
		return call
	}
	if class.InvalidReason != "" {
		call.Request.ServerName = class.ServerName
		call.Request.Unresolved = true
		call.Request.UnresolvedReason = class.InvalidReason
		return call
	}

	res, tool := resolveSplits(ctx, env, class, req)
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
// Candidate generation is capped by mcpSplits. All candidates in that bounded set
// share one cached configuration snapshot and observe cancellation between reads.
func resolveSplits(ctx context.Context, env Env, class classification, req ResolveRequest) (Resolution, string) {
	loader := newConfigLoader()
	if len(class.Splits) <= 1 {
		tr := &tracer{}
		res := resolve(ctx, loader, env, req, tr)
		res.Trace = tr.steps
		return res, class.Tool
	}

	var (
		trace      []TraceStep
		first      Resolution
		candidates []Resolution
		tools      []string
	)
	for i, split := range class.Splits {
		if err := ctx.Err(); err != nil {
			out := unresolved(req.ServerName, "MCP server resolution was cancelled")
			out.Trace = trace
			return out, class.Tool
		}
		candidate := req
		candidate.ServerName = split.Server
		tr := &tracer{}
		res := resolve(ctx, loader, env, candidate, tr)
		res.Trace = tr.steps
		trace = append(trace, res.Trace...)
		if i == 0 {
			first = res
		}
		// A configuration match remains a viable reading even when its entry could
		// not be reduced to an enforcement identity. Discarding it here would let a
		// fully resolved, allowlisted shorter name answer for a call that may have
		// gone to the matched-but-unresolved longer name.
		if splitCandidateMatched(res) {
			candidates = append(candidates, res)
			tools = append(tools, split.Tool)
		}
	}

	switch len(candidates) {
	case 0:
		// Nothing matched under any reading. Report the leftmost, which is what the
		// agent itself believes it called.
		first.Trace = trace
		return first, class.Tool
	case 1:
		candidates[0].Trace = trace
		return candidates[0], tools[0]
	default:
		names := make([]string, 0, len(candidates))
		for _, res := range candidates {
			names = append(names, res.ServerName)
		}
		out := ambiguousToolName(req.Agent, class.ServerName, names)
		out.Trace = trace
		return out, class.Tool
	}
}

// splitCandidateMatched distinguishes a configuration miss from a configured
// server whose identity could not be resolved. A successful resolution is also
// viable when it has no trace match, as with a built-in or hosted connector.
func splitCandidateMatched(res Resolution) bool {
	if !res.Unresolved {
		return true
	}
	for _, step := range res.Trace {
		if step.Matched {
			return true
		}
	}
	return false
}
