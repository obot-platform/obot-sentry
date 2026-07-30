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

// DenialContext names the call that was blocked.
type DenialContext struct {
	Tool       string
	ServerName string
	Server     string
}

const (
	outcomeRefusedByPolicy  = "refused-by-policy"
	outcomeRefusedUnchecked = "refused-unchecked"
)

const agentGuardrails = `Tell the user their organization blocked this call, then stop and wait for them — and state it as a fact, because whether the policy is right about this particular call is not decidable from here.

Do not attempt the same result by any other route (another tool, another server, a shell command), and do not open, summarize, or propose edits to hook definitions, settings files, or MCP server configuration: the policy is not stored on this device, so nothing changed here decides anything.`

// shortGuardrails is the one-line form of agentGuardrails, for the single Cursor
// string that has to be readable as a notification and still instruct the model.
const shortGuardrails = "Do not try again, and do not change any hook or MCP configuration."

// maxReasonRunes bounds a reason string in a denial. Reasons arrive from
// outside: Obot writes the policy ones, and an infrastructure one is whatever
// error text a transport, a proxy, or a non-2xx body produced — a 404 from a
// reverse proxy is an entire HTML page. Unbounded, that lands in the model's
// context verbatim and buries the instructions after it.
const maxReasonRunes = 240

type denialOutcome struct {
	name    string
	summary string
	footer  string
}

var (
	refusedByPolicy = denialOutcome{
		name:    outcomeRefusedByPolicy,
		summary: "Obot checked this call against your organization's tool policy and the policy refused it. The call did not run.",
		footer:  "Ask your Obot administrator to review the tool allowlist if this call should be permitted.",
	}
	refusedUnchecked = denialOutcome{
		name:    outcomeRefusedUnchecked,
		summary: "Obot could not reach a verdict on this call — the tool policy was never checked — so the call was refused unchecked and did not run. Nothing about it was found to be prohibited.",
		footer:  "Tell your Obot administrator if this recurs.",
	}
)

func PolicyDenial(reason string, ctx DenialContext) Denial {
	blocked := "this action is not permitted by your organization's tool policy"
	if ctx.ServerName != "" {
		blocked = fmt.Sprintf("MCP server %q is not permitted by your organization's tool policy", ctx.ServerName)
	}
	return Denial{
		UserMessage:  fmt.Sprintf("Blocked: %s. %s Your Obot administrator can add it to the tool allowlist.", blocked, shortGuardrails),
		AgentMessage: violation(refusedByPolicy, reason, ctx),
	}
}

func InfrastructureDenial(reason string, ctx DenialContext) Denial {
	return Denial{
		UserMessage: fmt.Sprintf(
			"Blocked: your organization's tool policy could not be checked, so this action was refused. %s Report it to your Obot administrator if it persists.",
			shortGuardrails),
		AgentMessage: violation(refusedUnchecked, reason, ctx),
	}
}

// compactReason folds a reason onto one line and bounds its length, so it stays
// one line in the block below rather than swallowing it. A reason is a label,
// not a payload — see the note on deny() for where the rest of a server error
// goes, which is nowhere.
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
	Details    string
	Reason     string
	Footer     string
}

func violation(out denialOutcome, reason string, ctx DenialContext) string {
	var b strings.Builder
	if err := denialTmpl.Execute(&b, denialView{
		Summary:    out.summary,
		Guardrails: agentGuardrails,
		Details:    detailLine(out, ctx),
		Reason:     compactReason(reason),
		Footer:     out.footer,
	}); err != nil {
		panic("enforce: rendering the denial block: " + err.Error())
	}
	return b.String()
}

func detailLine(out denialOutcome, ctx DenialContext) string {
	parts := make([]string, 0, 4)
	parts = append(parts, "outcome "+out.name)
	if ctx.Tool != "" {
		parts = append(parts, "call "+ctx.Tool)
	}
	if ctx.ServerName != "" {
		parts = append(parts, "MCP server "+ctx.ServerName)
	}
	if ctx.Server != "" {
		parts = append(parts, "resolved as "+ctx.Server)
	}
	return "Blocked: " + strings.Join(parts, "; ") + "."
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
func Deny(agent localagent.Agent, event Event, denial Denial) []byte {
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
