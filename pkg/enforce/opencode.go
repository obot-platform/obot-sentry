package enforce

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
	"github.com/obot-platform/obot-sentry/pkg/toolkind"
)

// openCodePermissionPayload is the helper input for one OpenCode V2
// permission.evaluate decision.
//
// It is deliberately minimal. OpenCode's permission event also carries
// resources, metadata, a source call id, and the agent profile, and none of
// them belong in a policy decision: the public enforcement request is
// parameter-free by design. The action names the tool, and cwd selects the
// configuration scope that the resolver reads.
//
// In particular there is no MCP server name here. OpenCode sanitizes the server
// half into the action, so that half cannot be trusted to identify a server, and
// accepting a caller-supplied server hint would let a call borrow a shorter
// allowlisted name by simply omitting the longer one. Sentry enumerates the
// candidates from OpenCode's own effective configuration instead.
type openCodePermissionPayload struct {
	Action string `json:"action"`
	CWD    string `json:"cwd,omitempty"`
}

func normalizeOpenCodePermission(ctx context.Context, env Env, raw []byte) (Call, error) {
	var hook openCodePermissionPayload
	if err := decodePayload(raw, &hook); err != nil {
		return Call{}, err
	}

	action := strings.TrimSpace(hook.Action)
	if action == "" {
		return Call{}, errors.New("OpenCode permission.evaluate payload has no action")
	}
	if err := boundedField("action", action, maxToolNameBytes); err != nil {
		return Call{}, err
	}
	if err := boundedField("cwd", hook.CWD, maxWorkingDirBytes); err != nil {
		return Call{}, err
	}

	class := classifyOpenCodeAction(ctx, env, action, hook.CWD)
	return buildCall(ctx, env, localagent.OpenCode, class, ResolveRequest{
		Agent:      localagent.OpenCode,
		ServerName: class.ServerName,
		CWD:        hook.CWD,
	}), nil
}

// openCodeBuiltinKinds is the exact action-to-kind table for OpenCode's own
// permission-aware tools.
//
// The table is explicit rather than derived from a substring heuristic because
// the alternative fails open: an action that is not in the table might be an
// MCP tool from a server Sentry could not read, and the built-in-tools toggle
// allows the generic kind. Classifying an unrecognized action as generic would
// therefore hand every unconfigured MCP tool a free pass. An action missing
// from this table is unresolved until it is added here, which refuses the call
// and is the safe direction for an opt-in enforcement mode.
var openCodeBuiltinKinds = map[string]string{
	"read":  "read",
	"edit":  "write",
	"write": "write",
	"patch": "write",

	"shell":    "shell",
	"subagent": "task",

	"glob":               "generic",
	"grep":               "generic",
	"question":           "generic",
	"skill":              "generic",
	"webfetch":           "generic",
	"websearch":          "generic",
	"external_directory": "generic",
	"list":               "generic",
	"todowrite":          "generic",
	"doom_loop":          "generic",

	"opencode_list_mcp_resources": "generic",
	"opencode_read_mcp_resource":  "generic",
}

// classifyOpenCodeAction decides what a permission action is, using only
// information Sentry can verify for itself.
func classifyOpenCodeAction(ctx context.Context, env Env, action, cwd string) classification {
	if kind, ok := openCodeBuiltinKinds[strings.ToLower(action)]; ok {
		return classification{Kind: kind, Tool: action}
	}

	// Not a built-in, so this is most likely an MCP tool whose action is
	// "<sanitized server>_<sanitized tool>". That split is lossy, so the only
	// safe source of candidates is OpenCode's own effective configuration.
	candidates, ambiguous, err := openCodeServerCandidates(ctx, env, action, cwd)
	if err != nil {
		return classification{
			Kind:          toolkind.KindMCP,
			Tool:          action,
			InvalidReason: "the OpenCode configuration could not be read: " + err.Error(),
		}
	}
	if ambiguous {
		return classification{
			Kind:          toolkind.KindMCP,
			Tool:          action,
			InvalidReason: "two configured MCP server names collapse to the same OpenCode namespace",
		}
	}
	if len(candidates) == 0 {
		return classification{
			Kind:          toolkind.KindMCP,
			Tool:          action,
			InvalidReason: fmt.Sprintf("OpenCode action %q is not a known built-in and matches no configured MCP server", action),
		}
	}

	splits := make([]mcpSplit, 0, len(candidates))
	for _, server := range candidates {
		splits = append(splits, mcpSplit{Server: server, Tool: strings.TrimPrefix(action, server+"_")})
	}
	// More than one configured name explains the action. resolveSplits keeps
	// them all and Obot decides, rather than this file picking a winner.
	return classification{
		Kind:       toolkind.KindMCP,
		Tool:       splits[0].Tool,
		ServerName: splits[0].Server,
		Splits:     splits,
	}
}
