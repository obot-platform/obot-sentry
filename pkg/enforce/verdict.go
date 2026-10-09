package enforce

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

type Denial struct {
	UserMessage  string
	AgentMessage string
}

const agentGuardrails = `Tell the user their organization blocked this call, then stop and wait for them — and state it as a fact, because whether the policy is right about this particular call is not decidable from here.

Do not attempt the same result by any other route (another tool, another server, a shell command), and do not open, summarize, or propose edits to hook definitions, settings files, or MCP server configuration: the policy is not stored on this device, so nothing changed here decides anything.`

// shortGuardrails is the one-line form of agentGuardrails, for the single Cursor
// string that has to be readable as a notification and still instruct the model.
const shortGuardrails = "Do not try again, and do not change any hook or MCP configuration."

// maxReasonRunes bounds the internal diagnostic retained in Result and shown by
// an explicit dry run. Denial protocol responses and ordinary hook stderr never
// include the reason.
const maxReasonRunes = 240

type denialOutcome struct {
	summary string
	footer  string
}

var (
	refusedByPolicy = denialOutcome{
		summary: "Obot checked this call against your organization's tool policy and the policy refused it. The call did not run.",
		footer:  "Ask your Obot administrator to review the tool allowlist if this call should be permitted.",
	}
	refusedUnchecked = denialOutcome{
		summary: "Obot could not reach a verdict on this call — the tool policy was never checked — so the call was refused unchecked and did not run. Nothing about it was found to be prohibited.",
		footer:  "Tell your Obot administrator if this recurs.",
	}
)

func PolicyDenial() Denial {
	return Denial{
		UserMessage:  fmt.Sprintf("Blocked: this action is not permitted by your organization's tool policy. %s Your Obot administrator can review the tool allowlist.", shortGuardrails),
		AgentMessage: violation(refusedByPolicy),
	}
}

func InfrastructureDenial() Denial {
	return Denial{
		UserMessage: fmt.Sprintf(
			"Blocked: your organization's tool policy could not be checked, so this action was refused. %s Report it to your Obot administrator if it persists.",
			shortGuardrails),
		AgentMessage: violation(refusedUnchecked),
	}
}

// compactReason folds a diagnostic onto one line and bounds its length.
func compactReason(reason string) string {
	reason = strings.Join(strings.Fields(reason), " ")
	runes := []rune(reason)
	if len(runes) <= maxReasonRunes {
		return reason
	}
	return strings.TrimSpace(string(runes[:maxReasonRunes])) + "…"
}

//go:embed denial.md.tmpl
var denialTemplate string

var denialTmpl = template.Must(template.New("denial").Parse(denialTemplate))

type denialView struct {
	Summary    string
	Guardrails string
	Footer     string
}

func violation(out denialOutcome) string {
	var b strings.Builder
	if err := denialTmpl.Execute(&b, denialView{
		Summary:    out.summary,
		Guardrails: agentGuardrails,
		Footer:     out.footer,
	}); err != nil {
		panic("enforce: rendering the denial block: " + err.Error())
	}
	return b.String()
}

// claudeHookOutput is the PreToolUse response shape Claude Code and Codex share.
type claudeHookOutput struct {
	HookSpecificOutput claudeHookSpecificOutput `json:"hookSpecificOutput"`
}

type claudeHookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason"`
}

// cursorHookOutput is Cursor's response shape for beforeMCPExecution and
// preToolUse.
type cursorHookOutput struct {
	Permission  string `json:"permission"`
	UserMessage string `json:"user_message,omitempty"`
}

// Allow renders the response for a permitted tool call.
//
// For Claude Code and Codex this is ZERO BYTES, and that is not an
// oversight to tidy up. Claude Code's permissionDecision distinguishes "defer"
// ("let the normal permission flow handle it, equivalent to no decision") from
// "allow" ("allow the tool call to proceed", which approves the call and
// suppresses the prompt the user would otherwise have seen). Emitting nothing is
// defer.
//
// Emitting permissionDecision "allow" on every permitted call would turn an
// enforcement hook into a permission bypass: every shell command and
// every file write auto-approved because the allowlist said the tool call was
// permitted. The allowlist and the agent's own permission model are different
// controls, and enforcement must not collapse them. This path withholds a denial;
// it never grants permission.
//
// Cursor is the exception by protocol — its hooks require an explicit allow — and
// "allow" there means "this hook does not object", not "skip the user's approval".
func Allow(agent localagent.Agent) []byte {
	if agent != localagent.Cursor {
		return nil
	}
	return marshal(cursorHookOutput{Permission: "allow"})
}

// Deny renders the response that blocks a tool call.
//
// Kiro's is not a JSON document: Kiro blocks only on exit code 2 and hands the
// hook's stderr to the model as the reason, so its response is the agent-facing
// text itself, and blocksByExit routes it to stderr (see Run).
func Deny(agent localagent.Agent, event Event, denial Denial) []byte {
	if agent == localagent.Kiro {
		return []byte(denial.AgentMessage)
	}
	if agent == localagent.Cursor {
		return marshal(cursorHookOutput{
			Permission:  "deny",
			UserMessage: denial.UserMessage,
		})
	}

	return marshal(claudeHookOutput{HookSpecificOutput: claudeHookSpecificOutput{
		HookEventName:            string(event),
		PermissionDecision:       "deny",
		PermissionDecisionReason: denial.AgentMessage,
	}})
}

// marshal encodes a protocol response. The shapes here cannot fail to marshal, so
// an error would be a programming mistake; returning no bytes on one would be a
// silent allow, so it is not tolerated as a normal outcome.
func marshal(out any) []byte {
	data, err := json.Marshal(out)
	if err != nil {
		panic("enforce: hook protocol response failed to marshal: " + err.Error())
	}
	return data
}

// blocksByExit reports whether agent's protocol blocks a call by exit status
// rather than by a response document. For such an agent a denial is written to
// stderr and the command exits 2; stdout carries nothing.
func blocksByExit(agent localagent.Agent) bool {
	return agent == localagent.Kiro
}
