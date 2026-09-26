package enforce

import (
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

func TestAllowEmitsNoBytes(t *testing.T) {
	for _, agent := range []localagent.Agent{localagent.ClaudeCode, localagent.Codex, localagent.WorkBuddy} {
		if got := Allow(agent); len(got) != 0 {
			t.Errorf("Allow(%s) emitted %d bytes (%q), want zero", agent, len(got), got)
		}
	}
}

func TestAllowNeverGrantsPermission(t *testing.T) {
	denial := Denial{UserMessage: "blocked", AgentMessage: "blocked"}
	for _, agent := range []localagent.Agent{localagent.ClaudeCode, localagent.Codex, localagent.WorkBuddy} {
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
			agent: localagent.WorkBuddy,
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

func TestDenialsContainNoInvocationDetails(t *testing.T) {
	secrets := []string{"tool-secret", "server-secret", "token-secret", "reason-secret"}
	for name, denial := range map[string]Denial{
		"policy":         PolicyDenial(),
		"infrastructure": InfrastructureDenial(),
	} {
		for channel, message := range map[string]string{
			"user":  denial.UserMessage,
			"agent": denial.AgentMessage,
		} {
			for _, secret := range secrets {
				if strings.Contains(message, secret) {
					t.Errorf("%s %s denial leaked %q: %q", name, channel, secret, message)
				}
			}
		}
		if !strings.Contains(denial.AgentMessage, "Do not attempt the same result by any other route") {
			t.Errorf("%s denial lost the anti-workaround guardrails", name)
		}
	}
}

func TestCompactReasonBoundsInternalDiagnostic(t *testing.T) {
	reason := "first line\n" + strings.Repeat("padding ", 200)
	got := compactReason(reason)
	if strings.Contains(got, "\n") {
		t.Fatalf("compactReason retained a newline: %q", got)
	}
	if len([]rune(got)) > maxReasonRunes+1 {
		t.Fatalf("compactReason returned %d runes, want at most %d plus ellipsis", len([]rune(got)), maxReasonRunes)
	}
}
