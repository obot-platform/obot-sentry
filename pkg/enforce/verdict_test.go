package enforce

import (
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

func TestAllowEmitsNoBytes(t *testing.T) {
	for _, agent := range []localagent.Agent{localagent.ClaudeCode, localagent.Codex} {
		if got := Allow(agent); len(got) != 0 {
			t.Errorf("Allow(%s) emitted %d bytes (%q), want zero", agent, len(got), got)
		}
	}
}

func TestAllowNeverGrantsPermission(t *testing.T) {
	denial := Denial{UserMessage: "blocked", AgentMessage: "blocked"}
	for _, agent := range []localagent.Agent{localagent.ClaudeCode, localagent.Codex} {
		for _, out := range [][]byte{Allow(agent), Deny(agent, EventPreToolUse, denial)} {
			if strings.Contains(string(out), `"permissionDecision":"allow"`) {
				t.Errorf("%s output granted permission: %s", agent, out)
			}
		}
	}
}

func TestCursorAllowIsExplicit(t *testing.T) {
	const want = `{"permission":"allow"}`
	if got := string(Allow(localagent.Cursor)); got != want {
		t.Fatalf("Allow(cursor) = %s, want %s", got, want)
	}
}

// TestDenyGolden pins the exact bytes of every deny shape. These strings are a
// protocol: a typo in one fails closed on every tool call for that agent.
func TestDenyGolden(t *testing.T) {
	denial := Denial{UserMessage: "user copy", AgentMessage: "agent copy"}

	cases := []struct {
		agent localagent.Agent
		event Event
		want  string
	}{
		{
			agent: localagent.ClaudeCode,
			event: EventPreToolUse,
			want:  `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"agent copy"}}`,
		},
		{
			agent: localagent.Codex,
			event: EventPreToolUse,
			want:  `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"agent copy"}}`,
		},
		{
			agent: localagent.Cursor,
			event: EventCursorBeforeMCPExecution,
			want:  `{"permission":"deny","user_message":"user copy"}`,
		},
		{
			agent: localagent.Cursor,
			event: EventCursorPreToolUse,
			want:  `{"permission":"deny","user_message":"user copy"}`,
		},
	}

	for _, tc := range cases {
		if got := string(Deny(tc.agent, tc.event, denial)); got != tc.want {
			t.Errorf("Deny(%s, %s) =\n%s\nwant\n%s", tc.agent, tc.event, got, tc.want)
		}
	}
}

// A reason is a label, not a payload. An infrastructure denial's text is
// whatever a transport, a proxy, or a non-2xx body produced — a 404 through a
// reverse proxy is an entire HTML page — and unbounded that would bury the
// instructions the model has to read. Bound it, and fold it onto one line so it
// stays a single line in the block.
func TestDenyBoundsTheReason(t *testing.T) {
	body := "error code 404 (Not Found): <!doctype html>\n<html>\n<body>" + strings.Repeat("padding ", 200) + "</body></html>"
	ctx := DenialContext{Tool: "echo", ServerName: "everything"}
	denial := InfrastructureDenial(body, ctx)
	// Bound growth, not total length: the difference from a one-rune reason is
	// everything the body contributed, so rewording the block can't relax it.
	baseline := InfrastructureDenial("x", ctx)

	for name, msg := range map[string][2]string{
		"agent message": {denial.AgentMessage, baseline.AgentMessage},
		"user message":  {denial.UserMessage, baseline.UserMessage},
	} {
		if grew := len([]rune(msg[0])) - len([]rune(msg[1])); grew > maxReasonRunes {
			t.Errorf("%s grew %d runes over a one-rune reason; an unbounded server body reached the model", name, grew)
		}
		if strings.Contains(msg[0], "</body></html>") {
			t.Errorf("%s carried the response body through to its end", name)
		}
	}
	if !strings.Contains(denial.AgentMessage, "error code 404 (Not Found)") {
		t.Errorf("the reason lost the part that identifies the failure: %q", denial.AgentMessage)
	}
	if !strings.Contains(denial.AgentMessage, "Do not attempt the same result by any other route") {
		t.Error("the guardrails did not survive a long reason")
	}
	// One line: a multi-line body must not break the block it sits in.
	causeLine := ""
	for line := range strings.SplitSeq(denial.AgentMessage, "\n") {
		if strings.HasPrefix(line, "Cause:") {
			causeLine = line
		}
	}
	if causeLine == "" || strings.Contains(causeLine, "\n") {
		t.Errorf("the reason is not a single line: %q", causeLine)
	}
	if len([]rune(causeLine)) > maxReasonRunes+len("Cause: ")+1 {
		t.Errorf("the cause line is %d runes, want it bounded by maxReasonRunes", len([]rune(causeLine)))
	}
}
