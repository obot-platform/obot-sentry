package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	obotcmd "github.com/obot-platform/cmd"
	"github.com/obot-platform/obot-sentry/pkg/agent"
	"github.com/obot-platform/obot-sentry/pkg/audit"
	"github.com/obot-platform/obot-sentry/pkg/client"
	"github.com/obot-platform/obot-sentry/pkg/datadir"
	"github.com/obot-platform/obot-sentry/pkg/localagent"
	"github.com/obot-platform/obot/apiclient/types"
	"github.com/spf13/cobra"
)

const (
	auditSubmitTimeout = 5 * time.Second
	auditDrainTimeout  = 3 * time.Second
)

type Audit struct{}

func newAuditCommand() (*cobra.Command, *AuditSubmit) {
	submit := &AuditSubmit{}
	return obotcmd.Command(&Audit{},
		cobra.Command{Use: "audit"},
		obotcmd.Command(submit, cobra.Command{Use: "submit"}),
	), submit
}

func (a *Audit) Customize(cmd *cobra.Command) {
	cmd.Use = "audit"
	cmd.Short = "Manage local agent audit hooks"
	cmd.Hidden = true
	cmd.Args = cobra.NoArgs
}

func (a *Audit) Run(c *cobra.Command, _ []string) error {
	return c.Help()
}

type AuditSubmit struct {
	ConfigFlags
	Agent           string `usage:"local agent provider: claude-code, codex, vscode, cursor"`
	Phase           string `usage:"hook phase: post-tool or failure"`
	Input           string `usage:"hook payload input path, or - for stdin" default:"-"`
	ManagedBy       string `usage:"managed hook marker" name:"managed-by" hidden:"true"`
	DryRun          bool   `usage:"write normalized audit logs to the user cache without submitting" name:"dry-run"`
	PrintNormalized bool   `usage:"print normalized debug JSON to stdout" name:"print-normalized"`
}

func (s *AuditSubmit) Customize(cmd *cobra.Command) {
	cmd.Use = "submit"
	cmd.Short = "Submit a local agent audit hook payload"
	cmd.Hidden = true
	cmd.Args = cobra.NoArgs
}

func (s *AuditSubmit) Run(cmd *cobra.Command, _ []string) error {
	// --managed-by is a flag that we ignore.
	// It's accepted so that obot-sentry can recognize its own managed hook
	// configurations when editing config files.
	if s.ManagedBy != "" && s.ManagedBy != "obot-sentry" {
		return fmt.Errorf("--managed-by must be empty or obot-sentry")
	}

	payload, err := readAuditInput(s.Input)
	if err != nil {
		return err
	}

	processOpts := audit.ProcessOptions{
		Agent: localagent.Agent(s.Agent),
		Phase: audit.Phase(s.Phase),
	}
	result, err := audit.Process(payload, processOpts)
	if err != nil {
		return err
	}

	for _, warning := range result.Warnings {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), warning)
	}
	events := auditEntriesToInputs(result.Entries)

	if s.PrintNormalized {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(events); err != nil {
			return err
		}
	}

	if s.DryRun {
		paths, err := audit.WriteDryRunLogs(events)
		if err != nil {
			return fmt.Errorf("write dry-run audit logs: %w", err)
		}
		for _, path := range paths {
			auditWarn(cmd, "obot-sentry audit: dry-run wrote audit log to %s", path)
		}
		return nil
	}

	if s.PrintNormalized || len(result.Entries) == 0 {
		return nil
	}

	s.submitTerminalEvents(cmd, events)
	return nil
}

func (s *AuditSubmit) submitTerminalEvents(cmd *cobra.Command, events []types.LocalAgentToolCallAuditLogInput) {
	if len(events) == 0 {
		return
	}

	cfg, err := s.resolve()
	if err != nil {
		auditWarn(cmd, "obot-sentry audit: reading deployment configuration: %v", err)
		return
	}
	if cfg.ServerURL == "" {
		auditWarn(cmd, "obot-sentry audit: no ServerURL configured; audit log not submitted")
		return
	}
	dir, err := datadir.Dir()
	if err != nil {
		auditWarn(cmd, "obot-sentry audit: resolving data directory: %v", err)
		return
	}
	idDir, err := datadir.IdentityDir()
	if err != nil {
		auditWarn(cmd, "obot-sentry audit: resolving identity directory: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), auditSubmitTimeout)
	defer cancel()

	a := agent.New(dir, idDir, cfg)
	id, st, err := a.EnsureEnrolled(ctx)
	if err != nil {
		auditWarn(cmd, "obot-sentry audit: device is not enrolled; audit log not submitted: %v", err)
		return
	}
	if err := a.SubmitLocalAgentAuditLogs(ctx, id, st, events); err != nil {
		s.handleProductionSubmitError(cmd, events, err)
		return
	}
	s.drainSpool(cmd, a)
}

func (s *AuditSubmit) handleProductionSubmitError(cmd *cobra.Command, logs []types.LocalAgentToolCallAuditLogInput, err error) {
	if client.IsUnauthorized(err) {
		auditWarn(cmd, "obot-sentry audit: device authorization failed after retry; audit log discarded: %v", err)
		return
	}
	if client.IsClientError(err) {
		auditWarn(cmd, "obot-sentry audit: server rejected audit log; audit log discarded: %v", err)
		return
	}
	if !client.IsTransient(err) {
		auditWarn(cmd, "obot-sentry audit: audit log submit failed; audit log discarded: %v", err)
		return
	}
	spool, spoolErr := audit.DefaultSpool()
	if spoolErr != nil {
		auditWarn(cmd, "obot-sentry audit: audit log submit failed and spool is unavailable: %v; original error: %v", spoolErr, err)
		return
	}
	if spoolErr := spool.Enqueue(logs); spoolErr != nil {
		auditWarn(cmd, "obot-sentry audit: audit log submit failed and spooling failed: %v; original error: %v", spoolErr, err)
		return
	}
	auditWarn(cmd, "obot-sentry audit: audit log submit failed transiently; spooled for retry: %v", err)
}

func (s *AuditSubmit) drainSpool(cmd *cobra.Command, a *agent.Agent) {
	spool, err := audit.DefaultSpool()
	if err != nil {
		auditWarn(cmd, "obot-sentry audit: spool unavailable after successful submit: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), auditDrainTimeout)
	defer cancel()
	id, st, err := a.EnsureEnrolled(ctx)
	if err != nil {
		auditWarn(cmd, "obot-sentry audit: cannot drain spool without enrolled device state: %v", err)
		return
	}
	_, err = spool.Drain(10, func(logs []types.LocalAgentToolCallAuditLogInput) error {
		return a.SubmitLocalAgentAuditLogs(ctx, id, st, logs)
	}, client.IsClientError)
	if err != nil {
		auditWarn(cmd, "obot-sentry audit: spool drain did not complete: %v", err)
	}
}

func auditEntriesToInputs(entries []audit.Entry) []types.LocalAgentToolCallAuditLogInput {
	out := make([]types.LocalAgentToolCallAuditLogInput, 0, len(entries))
	for _, entry := range entries {
		var startedAt *types.Time
		if entry.StartedAt != nil {
			startedAt = types.NewTime(*entry.StartedAt)
		}
		target := types.LocalAgentToolCallAuditLogTarget{
			TargetType: types.AuditLogTargetTypeLocalTool,
			Name:       entry.ToolName,
		}
		if entry.ToolKind == "mcp" {
			target.TargetType = types.AuditLogTargetTypeMCPTool
			target.Name = entry.MCPToolName
			if target.Name == "" {
				target.Name = entry.ToolName
			}
			if entry.MCPServerHint != "" {
				target.Parent = &types.LocalAgentToolCallAuditLogTargetRef{
					TargetType: types.AuditLogTargetTypeMCPServer,
					Name:       entry.MCPServerHint,
				}
			}
		}
		out = append(out, types.LocalAgentToolCallAuditLogInput{
			OccurredAt: *types.NewTime(entry.OccurredAt),
			Action: types.LocalAgentToolCallAuditLogAction{
				Name: entry.ToolName,
				Kind: entry.ToolKind,
			},
			Target: target,
			Outcome: types.LocalAgentToolCallAuditLogOutcome{
				Status:     types.AuditLogOutcomeStatus(entry.Status),
				Reason:     entry.FailureType,
				Error:      entry.Error,
				DurationMs: entry.DurationMs,
			},
			Details: types.LocalAgentToolCallAuditLogReportedDetails{
				StartedAt: startedAt,
				Trace: types.LocalAgentToolCallAuditLogTrace{
					IdempotencyKey: entry.IdempotencyKey,
					ToolUseID:      entry.ToolUseID,
					SessionID:      entry.SessionID,
					TurnID:         entry.TurnID,
				},
				Agent: types.LocalAgentToolCallAuditLogAgent{
					Provider:       types.LocalAgentProvider(entry.AgentProvider),
					Version:        entry.AgentVersion,
					CLIName:        entry.CLIName,
					CLIVersion:     entry.CLIVersion,
					Model:          entry.Model,
					ModelID:        entry.ModelID,
					PermissionMode: entry.PermissionMode,
				},
				Device: types.LocalAgentToolCallAuditLogDevice{
					Hostname:      entry.Hostname,
					OS:            entry.OS,
					Architecture:  entry.Arch,
					LocalUsername: entry.LocalUsername,
				},
				Environment: types.LocalAgentToolCallAuditLogEnvironment{
					CWD:               entry.CWD,
					GitRoot:           entry.GitRepoRoot,
					GitRemotes:        entry.GitRemoteURLs,
					GitBranch:         entry.GitBranch,
					GitCommit:         entry.GitCommitSHA,
					ReportedUserEmail: entry.ReportedUserEmail,
					TranscriptPath:    entry.TranscriptPath,
				},
				Request:  types.LocalAgentToolCallAuditLogPayload{Body: entry.ToolInput},
				Response: types.LocalAgentToolCallAuditLogPayload{Body: entry.ToolOutput},
				RawEvent: entry.RawHookPayload,
			},
		})
	}
	return out
}

func auditWarn(cmd *cobra.Command, format string, args ...any) {
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", args...)
}

func readAuditInput(path string) ([]byte, error) {
	if path == "" || path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(strings.TrimSpace(path))
}
