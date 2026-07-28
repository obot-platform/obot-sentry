// Package audit parses native local-agent hook payloads into provider-neutral
// entries that the command maps to Obot's local-agent audit ingest API.
package audit

import (
	"encoding/json"
	"time"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

// Agent is a type alias because it is currently too much work to replace all usages of
// these constants in this package with localagent.Agent.
type Agent = localagent.Agent

const (
	AgentClaudeCode = localagent.ClaudeCode
	AgentCodex      = localagent.Codex
	AgentVSCode     = localagent.VSCode
	AgentCursor     = localagent.Cursor
)

type Phase string

const (
	PhasePostTool Phase = "post-tool"
	PhaseFailure  Phase = "failure"
)

type Status string

const (
	StatusSuccess Status = "success"
	StatusFailure Status = "failure"
	StatusDenied  Status = "denied"
	StatusTimeout Status = "timeout"
)

type Entry struct {
	AgentProvider string `json:"agentProvider"`
	AgentVersion  string `json:"agentVersion,omitempty"`
	CLIName       string `json:"cliName,omitempty"`
	CLIVersion    string `json:"cliVersion"`

	Status      Status     `json:"status"`
	FailureType string     `json:"failureType,omitempty"`
	OccurredAt  time.Time  `json:"occurredAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	DurationMs  int64      `json:"durationMs,omitempty"`
	Error       string     `json:"error,omitempty"`

	IdempotencyKey string `json:"idempotencyKey"`
	ToolUseID      string `json:"toolUseID,omitempty"`
	SessionID      string `json:"sessionID,omitempty"`
	TurnID         string `json:"turnID,omitempty"`

	ToolName      string `json:"toolName"`
	ToolKind      string `json:"toolKind,omitempty"`
	MCPServerHint string `json:"mcpServerHint,omitempty"`
	MCPToolName   string `json:"mcpToolName,omitempty"`

	Model          string `json:"model,omitempty"`
	ModelID        string `json:"modelID,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`

	Hostname          string `json:"hostname,omitempty"`
	OS                string `json:"os,omitempty"`
	Arch              string `json:"arch,omitempty"`
	LocalUsername     string `json:"localUsername,omitempty"`
	ReportedUserEmail string `json:"reportedUserEmail,omitempty"`

	CWD           string   `json:"cwd,omitempty"`
	GitRepoRoot   string   `json:"gitRepoRoot,omitempty"`
	GitRemoteURLs []string `json:"gitRemoteURLs,omitempty"`
	GitBranch     string   `json:"gitBranch,omitempty"`
	GitCommitSHA  string   `json:"gitCommitSHA,omitempty"`

	TranscriptPath string `json:"transcriptPath,omitempty"`

	ToolInput      json.RawMessage `json:"toolInput"`
	ToolOutput     json.RawMessage `json:"toolOutput"`
	RawHookPayload json.RawMessage `json:"rawHookPayload"`
}

type ProcessOptions struct {
	Agent Agent
	Phase Phase

	Now        func() time.Time // for unit testing purposes
	Enrichment *Enrichment
	SkipLocal  bool
}

type Result struct {
	Entries  []Entry
	Warnings []string
}
