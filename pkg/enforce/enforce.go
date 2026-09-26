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
	// ResponseWriteErr reports that the agent did not receive the rendered
	// verdict. The command layer turns this into the protocol's blocking exit.
	ResponseWriteErr error

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
		warn(opts.Stderr, "obot-sentry enforce: blocked")
	}
	if len(result.Response) > 0 {
		n, err := opts.Stdout.Write(result.Response)
		if err == nil && n != len(result.Response) {
			err = io.ErrShortWrite
		}
		if err != nil {
			result.ResponseWriteErr = fmt.Errorf("writing the hook response: %w", err)
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
		events := Events(agent)
		if len(events) == 0 {
			return Result{Denied: true, Reason: err.Error(), Unusable: true}
		}
		return deny(agent, events[0], Result{}, err.Error(), InfrastructureDenial())
	}

	raw, err := readPayload(opts.Input)
	if err != nil {
		return infrastructureDeny(agent, event, Result{}, fmt.Sprintf("the hook payload could not be read: %v", err))
	}

	call, err := normalizeCallContext(ctx, opts.Env, agent, event, raw)
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
		// The OpenCode plugin is a real enforcement boundary, not an observer, and
		// a disabled fleet policy answers allow unconditionally. A call whose MCP
		// target was never resolved must still be refused here rather than rely on
		// the server having been configured to refuse it.
		if (agent == localagent.OpenCode || agent == localagent.ZCode) && call.Request.Unresolved {
			return infrastructureDeny(agent, event, result,
				fmt.Sprintf("the %s tool target could not be resolved", agent.DisplayName()))
		}
		result.Response = Allow(agent)
		return result
	case types.EnforcementDecisionDeny:
		reason := resp.Reason
		if reason == "" {
			reason = "the call is not permitted by your organization's tool policy"
		}
		return deny(agent, event, result, reason, PolicyDenial())
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
	return deny(agent, event, result, reason, InfrastructureDenial())
}

// deny records the block. The reason remains available to the command for exit
// handling and to an explicit dry run, but neither the protocol response nor
// ordinary hook stderr renders it.
func deny(agent localagent.Agent, event Event, result Result, reason string, denial Denial) Result {
	result.Denied = true
	result.Reason = compactReason(reason)
	result.Response = Deny(agent, event, denial)
	return result
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
