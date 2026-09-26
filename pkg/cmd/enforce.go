package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	obotcmd "github.com/obot-platform/cmd"
	"github.com/obot-platform/obot-sentry/pkg/client"
	"github.com/obot-platform/obot-sentry/pkg/datadir"
	"github.com/obot-platform/obot-sentry/pkg/enforce"
	"github.com/obot-platform/obot-sentry/pkg/identity"
	"github.com/obot-platform/obot-sentry/pkg/localagent"
	"github.com/obot-platform/obot-sentry/pkg/mdmconfig"
	"github.com/obot-platform/obot/apiclient/types"
	"github.com/spf13/cobra"
)

const enforceHookBudget = 5 * time.Second

type Enforce struct {
	// These are deliberately hand-built flags rather than tagged command fields.
	// The command builder gives every exported field an implicit environment
	// binding, but an enforcement hook must consume the agent's stdin and make a
	// real, protocol-clean decision regardless of the environment it inherits.
	// Every value may still be supplied explicitly; installed hooks pin agent,
	// event, and managed-by while diagnostics may opt into the others.
	agent           string
	event           string
	managedBy       string
	input           string
	dryRun          bool
	printNormalized bool
	serverURL       string
	loadMDMConfig   func() (mdmconfig.Config, error)
}

func newEnforceCommand() (*cobra.Command, *Enforce) {
	hook := &Enforce{}
	return obotcmd.Command(hook,
		cobra.Command{Use: "enforce", SilenceUsage: true, SilenceErrors: true},
		obotcmd.Command(&EnforceResolve{}),
	), hook
}

func (e *Enforce) Customize(cmd *cobra.Command) {
	cmd.Use = "enforce"
	cmd.Short = "Decide a pre-tool hook payload against Obot's allowlist"
	cmd.Hidden = true
	cmd.Flags().StringVar(&e.agent, "agent", "", "local agent provider: claude-code, codex, cursor, workbuddy, zcode, opencode")
	cmd.Flags().StringVar(&e.event, "event", "", "the agent's own pre-tool event: PreToolUse, beforeMCPExecution, preToolUse, permission.evaluate")
	cmd.Flags().StringVar(&e.managedBy, "managed-by", "", "managed hook marker")
	if err := cmd.Flags().MarkHidden("managed-by"); err != nil {
		panic(err)
	}
	cmd.Flags().StringVar(&e.input, "input", "-", "hook payload input path, or - for stdin")
	cmd.Flags().BoolVar(&e.dryRun, "dry-run", false, "normalize and resolve the call without asking for a verdict or answering the agent")
	cmd.Flags().BoolVar(&e.printNormalized, "print-normalized", false, "print the normalized decision request to stdout")
	cmd.Flags().StringVar(&e.serverURL, "server-url", "", "Obot server base URL (overrides the MDM-configured value)")

	// Flag and argument errors have to fail closed as well. Cobra reports them
	// before RunE, so no protocol response is written, and a plain error exits 2
	// so that agents handle it as "fail closed".
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &ExitCodeError{Code: 2, Err: err}
	})
	cmd.Args = func(c *cobra.Command, args []string) error {
		if err := cobra.NoArgs(c, args); err != nil {
			return &ExitCodeError{Code: 2, Err: err}
		}
		return nil
	}
}

func (e *Enforce) Run(cmd *cobra.Command, _ []string) error {
	// --managed-by is accepted and ignored. It is the sole ownership marker
	// hook-install recognizes when it converges its own hook entries. A foreign
	// value is an invocation we do not understand, so it fails closed through the
	// same blocking exit as a flag we cannot parse.
	if e.managedBy != "" && e.managedBy != "obot-sentry" {
		return &ExitCodeError{Code: 2, Err: errors.New("--managed-by must be empty or obot-sentry")}
	}

	input, closeInput, err := openEnforceInput(e.input)
	if err != nil {
		// Fail closed with no protocol channel available: nothing about the
		// invocation is usable yet, not even which agent is waiting on stdout.
		return &ExitCodeError{Code: 2, Err: err}
	}
	defer closeInput()

	env, envErr := enforce.NewEnv()

	ctx, cancel := context.WithTimeout(cmd.Context(), enforceHookBudget)
	defer cancel()

	result := enforce.Run(ctx, enforce.Options{
		Env:             env,
		Agent:           e.agent,
		Event:           e.event,
		Input:           input,
		Stdout:          cmd.OutOrStdout(),
		Stderr:          cmd.ErrOrStderr(),
		Decide:          e.decider(envErr),
		PrintNormalized: e.printNormalized,
		DryRun:          e.dryRun,
	})
	if result.Unusable {
		return &ExitCodeError{Code: 2, Err: errors.New(result.Reason)}
	}
	if result.ResponseWriteErr != nil {
		return &ExitCodeError{Code: 2, Err: result.ResponseWriteErr}
	}
	return nil
}

// resolveConfig reads the protected MDM configuration and applies only an
// explicit command-line override. Unlike the general ConfigFlags resolver it
// has no environment-variable input.
func (e *Enforce) resolveConfig() (mdmconfig.Config, error) {
	load := e.loadMDMConfig
	if load == nil {
		load = mdmconfig.Load
	}
	cfg, err := load()
	if err != nil {
		return mdmconfig.Config{}, fmt.Errorf("reading MDM configuration: %w", err)
	}
	if e.serverURL != "" {
		cfg.ServerURL = e.serverURL
	}
	return cfg, nil
}

// decider returns the call that asks Obot for a verdict. Every piece of setup it
// needs is deferred into the closure so that a failure at any step — an
// unreadable MDM store, no configured server, a missing device identity — becomes
// one infrastructure deny carrying its own specific reason, rather than a
// separate early-return path per step. A --dry-run never calls it, so a dry run
// reads no deployment configuration at all.
func (e *Enforce) decider(envErr error) enforce.DecideFunc {
	return func(ctx context.Context, req types.EnforcementDecisionRequest) (types.EnforcementDecisionResponse, error) {
		var zero types.EnforcementDecisionResponse
		// A home directory we could not resolve means the resolver read no
		// configuration at all, so the call was reported unresolved for a reason
		// that has nothing to do with the user's configuration. Say so instead.
		if envErr != nil {
			return zero, fmt.Errorf("the machine environment could not be resolved: %w", envErr)
		}
		cfg, err := e.resolveConfig()
		if err != nil {
			return zero, fmt.Errorf("the deployment configuration could not be read: %w", err)
		}
		if cfg.ServerURL == "" {
			return zero, errors.New("no Obot server URL is configured on this device")
		}
		idDir, err := datadir.IdentityDir()
		if err != nil {
			return zero, fmt.Errorf("the device identity directory could not be resolved: %w", err)
		}
		id, err := identity.Load(idDir)
		if err != nil {
			return zero, fmt.Errorf("the device identity could not be loaded: %w", err)
		}
		return client.New(cfg.ServerURL).Decide(ctx, id, req)
	}
}

// EnforceResolve is `obot-sentry enforce resolve`, the payload-free diagnostic
// and the answer to "why was this call denied".
//
// It prints the trace the production resolver built, from the same code path the
// hook uses rather than a reimplementation, so a trace that says FOUND is
// evidence about production behavior.
type EnforceResolve struct {
	Agent  string `usage:"local agent provider: claude-code, codex, cursor, workbuddy, zcode, opencode"`
	Server string `usage:"MCP server name, as the tool call reports it"`
	CWD    string `usage:"working directory to resolve project configuration against (default: the current directory)" name:"cwd"`
}

func (r *EnforceResolve) Customize(cmd *cobra.Command) {
	cmd.Use = "resolve"
	cmd.Short = "Show what an MCP server name resolves to on this machine"
	cmd.Long = `Show what an MCP server name resolves to on this machine

Prints every configuration source the pre-tool hook would consult, in order, and
what it concluded. Exits non-zero when the server could not be identified, which
is the state that makes a real tool call fail closed.`
	cmd.Hidden = true
	cmd.Args = cobra.NoArgs
}

func (r *EnforceResolve) Run(cmd *cobra.Command, _ []string) error {
	agent, err := enforce.ParseAgent(r.Agent)
	if err != nil {
		return err
	}
	if strings.TrimSpace(r.Server) == "" {
		return errors.New("--server is required")
	}

	env, err := enforce.NewEnv()
	if err != nil {
		return err
	}

	cwd := r.CWD
	if cwd == "" {
		if cwd, err = os.Getwd(); err != nil {
			return err
		}
	}

	req := enforce.ResolveRequest{Agent: agent, ServerName: r.Server, CWD: cwd}
	if agent == localagent.Cursor {
		// Cursor sends no usable cwd, so its resolver reads workspace roots. The
		// directory being diagnosed stands in for the workspace root a live call
		// would carry.
		req.WorkspaceRoots = []string{cwd}
	}

	res := enforce.Resolve(env, req)
	printResolution(cmd.OutOrStdout(), res)
	if res.Unresolved {
		// Non-zero so this is usable in a script: an unresolved server is exactly
		// what makes a real tool call fail closed. The reason is already on stdout,
		// so the error itself stays bare rather than printing it a second time.
		return &ExitCodeError{Code: 1, Err: errors.New("unresolved")}
	}
	return nil
}

// printResolution renders a resolution the way an operator reads it: every
// source consulted in order, then what it concluded.
func printResolution(out io.Writer, res enforce.Resolution) {
	for i, step := range res.Trace {
		_, _ = fmt.Fprintf(out, "%d  %s\n", i+1, formatTraceStep(step))
	}
	if res.ServerName != "" {
		_, _ = fmt.Fprintf(out, "server name: %s\n", res.ServerName)
	}
	switch {
	case res.Unresolved:
		_, _ = fmt.Fprintf(out, "unresolved: %s\n", res.Reason)
	case res.String() != "":
		_, _ = fmt.Fprintf(out, "resolved: %s\n", res.String())
	default:
		// A built-in agent MCP server: fully identified by name, with no URL,
		// package, or command to report.
		_, _ = fmt.Fprintln(out, "resolved: built-in agent MCP server")
	}
}

// formatTraceStep renders one consulted source. Both matches of a name declared
// in two scopes print, because two FOUND lines are the whole diagnostic for that
// denial.
func formatTraceStep(step enforce.TraceStep) string {
	var b strings.Builder
	b.WriteString(step.Path)
	// The key is where in the file we looked, which says nothing when the file is
	// not there.
	if step.Key != "" && step.Exists {
		b.WriteString("  ")
		b.WriteString(step.Key)
	}
	switch {
	case step.Matched:
		b.WriteString("  FOUND")
		if step.Note != "" {
			b.WriteString(" (")
			b.WriteString(step.Note)
			b.WriteString(")")
		}
	case !step.Exists:
		b.WriteString("  absent")
	case step.Note != "":
		b.WriteString("  ")
		b.WriteString(step.Note)
	}
	return b.String()
}

// openEnforceInput opens the hook payload source. Unlike audit's reader this
// does not read the whole file up front: the read is bounded inside pkg/enforce,
// where exceeding the bound is a deny.
func openEnforceInput(path string) (*os.File, func(), error) {
	if path == "" || path == "-" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		return nil, func() {}, err
	}
	return f, func() { _ = f.Close() }, nil
}
