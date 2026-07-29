package enforce

import (
	"fmt"
	"strings"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
	"github.com/obot-platform/obot/apiclient/types"
)

// mcpEntry is the entirety of an MCP server configuration entry the resolver
// reads: a URL, or a command plus its arguments.
type mcpEntry struct {
	URL     string   `json:"url" toml:"url"`
	Command string   `json:"command" toml:"command"`
	Args    []string `json:"args" toml:"args"`
}

// TraceStep records one source the resolver consulted. The trace is what makes
// "why was this denied" answerable without adding logging to the hot path.
type TraceStep struct {
	// Path is the file that was consulted.
	Path string
	// Key names the location within the file, such as "mcpServers" or
	// `projects["/Users/me/proj"]`. Empty when the file itself is the source.
	Key string
	// Exists reports whether the file was present.
	Exists bool
	// Matched reports whether the server was found here. Normally at most one
	// step in a trace matches, and it is the last step — but a Cursor name
	// declared in more than one scope records a match per scope, because two
	// FOUND lines are the whole diagnostic for that denial (see resolveCursor).
	Matched bool
	// Note carries the detail behind a step: why a present file yielded nothing,
	// or which lookup form matched.
	Note string
}

// Resolution is what the resolver concluded about a call's target server, plus
// the evidence behind it.
type Resolution struct {
	ServerName string
	// Identity is the resolved server. Empty for a built-in agent server, which
	// has no URL, package, or command but is still fully identified by name.
	Identity types.EnforcementDecisionServer
	// Unresolved reports that we could not establish what the call targets.
	// Obot is told, and Obot decides; this is never a verdict of its own.
	Unresolved bool
	// Reason names the specific cause, and is populated only when Unresolved.
	Reason string
	// Trace lists every source consulted, in order.
	Trace []TraceStep
}

// String renders the human-readable summary of a resolved identity, for the
// resolve diagnostic and for deny copy.
func (r Resolution) String() string {
	switch {
	case r.Identity.URL != "":
		return r.Identity.URL
	case r.Identity.Package != nil:
		version := r.Identity.Package.Version
		if version == "" {
			version = "any version"
		}
		return fmt.Sprintf("%s / %s / %s", r.Identity.Package.Source, r.Identity.Package.Name, version)
	case r.Identity.Connector != "":
		return "connector " + r.Identity.Connector
	case r.Identity.Command != "":
		return r.Identity.Command
	default:
		return ""
	}
}

// ResolveRequest is what the per-agent resolvers need from a decoded hook
// payload.
type ResolveRequest struct {
	// Agent is the agent that issued the call.
	Agent localagent.Agent
	// ServerName is the server hint from the tool name, or for Cursor the display
	// name from the payload.
	ServerName string
	// CWD is the working directory the call was made from.
	CWD string
	// WorkspaceRoots are Cursor's open workspace roots. They are the only project
	// context a Cursor payload carries: it sends no usable cwd.
	WorkspaceRoots []string
}

// Resolve maps a server name to a concrete server identity by reading the
// agent's own MCP configuration.
//
// It answers "what does this name point to", never "would the agent have run
// it". Anything that only governs whether a server starts is either unreachable
// for us or a source of false denies. The one deliberate exception is Claude
// Code's managed MCP config, whose server set the agent documents as
// non-overridable — see claudecode.go.
//
// A file that is absent, unreadable, or malformed is skipped rather than fatal.
// When no source matches, the result is marked unresolved with a specific reason
// and reported to Obot anyway; the resolver never decides.
func Resolve(env Env, req ResolveRequest) Resolution {
	tr := &tracer{}
	res := resolve(env, req, tr)
	res.Trace = tr.steps
	return res
}

func resolve(env Env, req ResolveRequest, tr *tracer) Resolution {
	serverName := strings.TrimSpace(req.ServerName)
	if serverName == "" {
		return unresolved("", "the tool call did not name an MCP server")
	}
	switch req.Agent {
	case localagent.ClaudeCode:
		return resolveClaudeCode(env, req, serverName, tr)
	case localagent.Codex:
		return resolveCodex(env, serverName, tr)
	case localagent.Cursor:
		return resolveCursor(env, req, serverName, tr)
	default:
		return unresolved(serverName, fmt.Sprintf("unsupported agent %q", req.Agent))
	}
}

// resolved converts a matched configuration entry into a Resolution. A package
// runner that cannot be resolved strictly (resolvePackage) yields an unresolved
// result that still carries the executable, so the decision-log row says what
// ran.
//
// matchedKey is the configuration key the lookup matched, and it is the name
// reported on every path through here — including the unresolved ones. "Matched
// but unresolvable", where the entry was found and its command is not npx or
// uvx, is still a match; reporting the tool-name hint there would name a server
// that appears in no configuration file.
func resolved(env Env, matchedKey string, entry mcpEntry) Resolution {
	if url := strings.TrimSpace(entry.URL); url != "" {
		return Resolution{ServerName: matchedKey, Identity: types.EnforcementDecisionServer{URL: url}}
	}

	command := strings.TrimSpace(entry.Command)
	if command == "" {
		return unresolved(matchedKey, "the MCP server entry has neither a URL nor a command")
	}

	// Only ever the executable, never the arguments: MCP args routinely carry API
	// keys and inline environment assignments, and truncating on the device means
	// a secret never crosses the wire in the first place.
	executable := executableOf(command)

	pkg, err := resolvePackage(command, entry.Args, env.GOOS)
	if err != nil {
		res := unresolved(matchedKey, err.Error())
		res.Identity.Command = executable
		return res
	}
	return Resolution{
		ServerName: matchedKey,
		Identity: types.EnforcementDecisionServer{
			Package: pkg,
			Command: executable,
		},
	}
}

// builtinAgentMCPServers is the per-agent set of MCP servers that ship inside the
// agent itself. A call to one of these appears in no configuration file, so it is
// reported by name alone and gated behind Obot's "Allow all built-in agent MCP
// servers" toggle. This table must agree with obot's pkg/enforcement copy. Nothing
// here enforces that agreement — there is no lockstep test across the two
// repositories — so a name added on one side has to be added on the other by hand.
//
// Claude Code is the only agent here, and it is the only one where name-based
// matching is sound: it publishes a hardcoded list and will not let a configured
// server take one of those names, so a built-in name can never also be a
// configuration key. That is what makes the single call site correct — this is
// consulted only from notFound(), and with no possible collision a built-in call
// always misses every configuration source and reaches it.
var builtinAgentMCPServers = map[localagent.Agent]map[string]struct{}{
	localagent.ClaudeCode: {
		"workspace":        {},
		"claude-in-chrome": {},
		"computer-use":     {},
		"Claude Preview":   {},
		"Claude Browser":   {},
	},
}

// isBuiltinAgentMCP reports whether serverName names a built-in MCP server of
// agent.
func isBuiltinAgentMCP(agent localagent.Agent, serverName string) bool {
	if serverName == "" {
		return false
	}
	servers, ok := builtinAgentMCPServers[agent]
	if !ok {
		return false
	}
	_, _, ok = matchName(lookup{names: []string{serverName}, form: agentNamespaceForm(agent)}, servers)
	return ok
}

// agentNamespaceForm is the transform an agent applies when building a tool
// namespace from a server name. Cursor has none: it reports names verbatim.
func agentNamespaceForm(agent localagent.Agent) namespaceForm {
	switch agent {
	case localagent.ClaudeCode:
		return formClaudeCode
	case localagent.Codex:
		return formCodex
	default:
		return nil
	}
}

// notFound is the terminal result when no configuration source matched. A
// built-in agent MCP server is reported by name — it has no URL, package, or
// command, and its name is what the built-in toggle matches on — and everything
// else is unresolved.
func notFound(agent localagent.Agent, serverName, reason string) Resolution {
	if isBuiltinAgentMCP(agent, serverName) {
		return Resolution{ServerName: serverName}
	}
	return unresolved(serverName, reason)
}

// unresolved builds a Resolution reporting that the target could not be
// established. Reason strings are the useful product of this path — they land in
// the decision log and are the first thing an administrator reads — so they name
// the specific cause.
func unresolved(serverName, reason string) Resolution {
	return Resolution{ServerName: serverName, Unresolved: true, Reason: reason}
}

// executableOf keeps only the executable of a stdio command, mirroring the
// backend's own truncation. A quoted path containing spaces is cut at the first
// space: a cosmetic loss, never a secret.
func executableOf(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// tracer accumulates the resolution trace.
type tracer struct {
	steps []TraceStep
}

// miss records a consulted source that did not yield the server.
func (t *tracer) miss(path, key string, res loadResult) {
	note := res.note()
	if res == loadOK && note == "" {
		note = "no match"
	}
	t.steps = append(t.steps, TraceStep{
		Path:   path,
		Key:    key,
		Exists: res != loadAbsent,
		Note:   note,
	})
}

// hit records the source that yielded the server. note describes which lookup
// form matched, when more than one was tried.
func (t *tracer) hit(path, key, note string) {
	t.steps = append(t.steps, TraceStep{
		Path:    path,
		Key:     key,
		Exists:  true,
		Matched: true,
		Note:    note,
	})
}
