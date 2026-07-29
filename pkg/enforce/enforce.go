package enforce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
	"github.com/obot-platform/obot/apiclient/types"
)

const maxPayloadBytes = 8 << 20

// DecideFunc asks Obot for a verdict on a normalized tool call. Any error is a
// deny: nothing here resolves an error into a verdict of its own.
type DecideFunc func(context.Context, types.EnforcementDecisionRequest) (types.EnforcementDecisionResponse, error)

type Options struct {
	Env             Env
	Agent           string
	Event           string
	Input           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	Decide          DecideFunc
	PrintNormalized bool
	DryRun          bool
}

type Result struct {
	Request  types.EnforcementDecisionRequest
	Trace    []TraceStep
	Response []byte
	Denied   bool
	Reason   string

	// Requested reports whether a decision request was issued. Every path issues
	// exactly one, except a Cursor preToolUse call on an MCP-shaped name.
	Requested bool
	// Skipped reports the one call shape that needs no decision.
	Skipped bool
	// Unusable reports that the invocation could not be understood well enough to
	// speak any agent's protocol — an unsupported --agent, nothing more. There is
	// no response shape to emit, so the caller must fail closed by exiting
	// non-zero instead of by writing bytes.
	Unusable bool
}

// Run is the whole pre-tool hook: read the payload, normalize it, ask Obot for a
// verdict, and answer in the agent's own protocol.
func Run(ctx context.Context, opts Options) Result {
	result := evaluate(ctx, opts)

	if opts.PrintNormalized && !result.Unusable {
		if err := printNormalized(opts.Stdout, result.Request); err != nil {
			warn(opts.Stderr, "printing the normalized call: %v", err)
		}
	}

	if opts.DryRun {
		// stderr, not stdout: --dry-run promises to write nothing an agent could
		// read as a verdict.
		warn(opts.Stderr, "would: %s", dryRunVerdict(result))
		return result
	}

	if result.Denied {
		warn(opts.Stderr, "obot-sentry enforce: blocked: %s", result.Reason)
	}
	if len(result.Response) > 0 {
		if _, err := opts.Stdout.Write(result.Response); err != nil {
			// The agent will not have received the verdict. Nothing further can be
			// done here; a Cursor hook fails closed on a missing response, and the
			// other two fall back to their own permission flow.
			warn(opts.Stderr, "obot-sentry enforce: writing the hook response: %v", err)
		}
	}
	return result
}

// dryRunVerdict describes what a real run would have done, without having asked.
func dryRunVerdict(result Result) string {
	switch {
	case result.Denied:
		return fmt.Sprintf("DENY (%s; policy not consulted; --dry-run)", result.Reason)
	case result.Request.Unresolved:
		return fmt.Sprintf("DENY (unresolved: %s; policy not consulted; --dry-run)", result.Request.UnresolvedReason)
	case result.Skipped:
		return "ALLOW (this event needs no decision; --dry-run)"
	default:
		return "ALLOW (policy not consulted; --dry-run)"
	}
}

// evaluate runs the control flow and renders the protocol response, without
// writing anything.
func evaluate(ctx context.Context, opts Options) Result {
	agent, err := ParseAgent(opts.Agent)
	if err != nil {
		// No agent means no response shape. The caller exits non-zero, which is
		// the only fail-closed signal left.
		return Result{Denied: true, Reason: err.Error(), Unusable: true}
	}

	event, err := ParseEvent(agent, opts.Event)
	if err != nil {
		return deny(agent, Events(agent)[0], Result{}, err.Error(), InfrastructureDenial(err.Error(), DenialContext{}))
	}

	raw, err := readPayload(opts.Input)
	if err != nil {
		return infrastructureDeny(agent, event, Result{}, fmt.Sprintf("the hook payload could not be read: %v", err))
	}

	call, err := normalizeCall(opts.Env, agent, event, raw)
	if err != nil {
		return infrastructureDeny(agent, event, Result{}, err.Error())
	}

	result := Result{Request: call.Request, Trace: call.Trace}

	// Cursor fires preToolUse for an MCP call as well, ahead of
	// beforeMCPExecution, which has already decided it. Deciding twice would
	// double-log, and this event's tool name carries no server hint to decide on.
	if call.Skip {
		result.Skipped = true
		result.Response = Allow(agent)
		return result
	}

	if opts.DryRun {
		// A dry run stops one step short of the decision so it cannot log a row or
		// block a call. The verdict it reports is "allow" only in the sense that
		// nothing denied it.
		return result
	}

	if opts.Decide == nil {
		return infrastructureDeny(agent, event, result, "no decision client is configured")
	}

	result.Requested = true
	resp, err := opts.Decide(ctx, call.Request)
	if err != nil {
		return infrastructureDeny(agent, event, result, err.Error())
	}

	switch resp.Decision {
	case types.EnforcementDecisionAllow:
		result.Response = Allow(agent)
		return result
	case types.EnforcementDecisionDeny:
		reason := resp.Reason
		if reason == "" {
			reason = "the call is not permitted by your organization's tool policy"
		}
		return deny(agent, event, result, reason, PolicyDenial(reason, denialContext(call)))
	default:
		// Neither an allow nor a deny. A zero-valued response decodes to an empty
		// decision, so anything unrecognized has to block rather than read as
		// "not a deny".
		return infrastructureDeny(agent, event, result,
			fmt.Sprintf("the decision response carried no recognized decision (%q)", resp.Decision))
	}
}

// infrastructureDeny blocks a call for a reason that is not a policy match: a
// payload that could not be read, a policy that could not be checked, an answer
// that could not be understood.
func infrastructureDeny(agent localagent.Agent, event Event, result Result, reason string) Result {
	return deny(agent, event, result, reason, InfrastructureDenial(reason, denialContextOf(result.Request)))
}

// deny records the block. The reason is compacted here as well as in the
// messages it renders, because it also reaches the hook's stderr — and an
// unbounded one is a real hazard there too: a non-2xx body can be half a
// megabyte of HTML, and Claude Code surfaces hook stderr into the transcript.
// Nothing on the device keeps the untruncated text; for a transport failure the
// status and the first line of the body are the diagnosis, and for a policy deny
// the decision log has the row.
func deny(agent localagent.Agent, event Event, result Result, reason string, denial Denial) Result {
	result.Denied = true
	result.Reason = compactReason(reason)
	result.Response = Deny(agent, event, denial)
	return result
}

// denialContext describes the blocked call to the model, including how the
// target server was resolved — an administrator reading the same row in the
// decision log sees the same identity.
func denialContext(call Call) DenialContext {
	ctx := denialContextOf(call.Request)
	ctx.Server = Resolution{Identity: call.Request.Server}.String()
	return ctx
}

func denialContextOf(req types.EnforcementDecisionRequest) DenialContext {
	return DenialContext{Tool: req.Tool, ServerName: req.ServerName}
}

// readPayload reads at most maxPayloadBytes from the hook's input. A nil input
// is an empty payload, which fails to decode and so denies.
func readPayload(in io.Reader) ([]byte, error) {
	if in == nil {
		return nil, errors.New("no input")
	}
	raw, err := io.ReadAll(io.LimitReader(in, maxPayloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxPayloadBytes {
		return nil, fmt.Errorf("payload exceeds %d bytes", maxPayloadBytes)
	}
	return raw, nil
}

// printNormalized writes the decision request the hook built, for diagnosing
// what a live tool call actually reports.
func printNormalized(out io.Writer, req types.EnforcementDecisionRequest) error {
	if out == nil {
		return nil
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(req)
}

func warn(out io.Writer, format string, args ...any) {
	if out == nil {
		return
	}
	_, _ = fmt.Fprintf(out, format+"\n", args...)
}
