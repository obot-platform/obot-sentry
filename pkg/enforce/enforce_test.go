package enforce

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
	"github.com/obot-platform/obot/apiclient/types"
)

// hookCase is one invocation of the hook against a stubbed decision endpoint.
type hookCase struct {
	agent string
	event string
	// payload is the raw stdin bytes. Ignored when input is set.
	payload string
	// input replaces the payload reader, for the cases where reading itself fails.
	input io.Reader
	// resp and err are what the stubbed decision call answers with.
	resp types.EnforcementDecisionResponse
	err  error
	// noDecider omits the decision client entirely.
	noDecider       bool
	dryRun          bool
	printNormalized bool
}

// hookRun is everything one invocation produced.
type hookRun struct {
	Result
	stdout   string
	stderr   string
	requests []types.EnforcementDecisionRequest
}

// runHook runs the hook against env, recording every decision request issued.
func runHook(t *testing.T, env Env, c hookCase) hookRun {
	t.Helper()

	var (
		stdout, stderr bytes.Buffer
		requests       []types.EnforcementDecisionRequest
	)
	opts := Options{
		Env:             env,
		Agent:           c.agent,
		Event:           c.event,
		Input:           c.input,
		Stdout:          &stdout,
		Stderr:          &stderr,
		PrintNormalized: c.printNormalized,
		DryRun:          c.dryRun,
	}
	if opts.Input == nil {
		opts.Input = strings.NewReader(c.payload)
	}
	if !c.noDecider {
		opts.Decide = func(_ context.Context, req types.EnforcementDecisionRequest) (types.EnforcementDecisionResponse, error) {
			requests = append(requests, req)
			return c.resp, c.err
		}
	}

	result := Run(t.Context(), opts)
	return hookRun{Result: result, stdout: stdout.String(), stderr: stderr.String(), requests: requests}
}

// errReader stands in for a stdin that fails mid-read.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("broken pipe") }

// claudeMCPCall is a Claude Code MCP call on a server the fixture configures.
const claudeMCPCall = `{"tool_name":"mcp__probe-npx-stdio__echo","cwd":"/Users/dev/probe-workspace"}`

// claudeShellCall is a Claude Code built-in tool call, which resolves nothing.
const claudeShellCall = `{"tool_name":"Bash","cwd":"/Users/dev/probe-workspace"}`

// claudeUnknownServerCall names an MCP server that appears in no configuration
// file, which is the most common unresolvable shape in the field.
const claudeUnknownServerCall = `{"tool_name":"mcp__not-configured__do","cwd":"/Users/dev/probe-workspace"}`

const allowResponse = `{"permission":"allow"}`

// TestRunFailsClosed is the fail-closed matrix: every condition that is not an
// explicit allow from Obot blocks the call. A row that let a call through would
// be a hole in the control, so each is asserted on the emitted bytes rather than
// on an internal flag alone.
func TestRunFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		hook hookCase
		// wantReason is a substring the recorded reason must name.
		wantReason string
		// wantNoResponse expects an invocation that cannot speak any agent's
		// protocol, and so must fail closed through a non-zero exit instead.
		wantNoResponse bool
		wantRequests   int
	}{
		{
			name:         "stdin cannot be read",
			hook:         hookCase{agent: "claude-code", event: "PreToolUse", input: errReader{}},
			wantReason:   "broken pipe",
			wantRequests: 0,
		},
		{
			name:         "payload is not JSON",
			hook:         hookCase{agent: "claude-code", event: "PreToolUse", payload: "not json at all"},
			wantReason:   "invalid pre-tool hook payload",
			wantRequests: 0,
		},
		{
			name:         "payload names no tool",
			hook:         hookCase{agent: "claude-code", event: "PreToolUse", payload: `{"cwd":"/Users/dev"}`},
			wantReason:   "no tool_name",
			wantRequests: 0,
		},
		{
			// No agent means no response shape: there is nothing to write, so the
			// caller has to exit non-zero instead.
			name:           "unsupported agent",
			hook:           hookCase{agent: "vscode", event: "PreToolUse", payload: claudeShellCall},
			wantReason:     `unsupported enforcement agent "vscode"`,
			wantNoResponse: true,
			wantRequests:   0,
		},
		{
			// The agent is known, so this one still gets a real deny in its own
			// protocol.
			name:         "unsupported event",
			hook:         hookCase{agent: "claude-code", event: "PostToolUse", payload: claudeShellCall},
			wantReason:   `unsupported claude-code enforcement event "PostToolUse"`,
			wantRequests: 0,
		},
		{
			name:         "cursor event on the wrong agent",
			hook:         hookCase{agent: "codex", event: "beforeMCPExecution", payload: claudeShellCall},
			wantReason:   "unsupported codex enforcement event",
			wantRequests: 0,
		},
		{
			name:         "no decision client",
			hook:         hookCase{agent: "claude-code", event: "PreToolUse", payload: claudeShellCall, noDecider: true},
			wantReason:   "no decision client",
			wantRequests: 0,
		},
		{
			name:         "connection error",
			hook:         hookCase{agent: "claude-code", event: "PreToolUse", payload: claudeShellCall, err: errors.New("dial tcp: connection refused")},
			wantReason:   "connection refused",
			wantRequests: 1,
		},
		{
			name:         "timeout",
			hook:         hookCase{agent: "claude-code", event: "PreToolUse", payload: claudeShellCall, err: context.DeadlineExceeded},
			wantReason:   context.DeadlineExceeded.Error(),
			wantRequests: 1,
		},
		{
			name: "unauthorized device",
			hook: hookCase{agent: "claude-code", event: "PreToolUse", payload: claudeShellCall,
				err: &types.ErrHTTP{Code: http.StatusUnauthorized, Message: "device not enrolled"}},
			wantReason:   "device not enrolled",
			wantRequests: 1,
		},
		{
			name: "server error",
			hook: hookCase{agent: "claude-code", event: "PreToolUse", payload: claudeShellCall,
				err: &types.ErrHTTP{Code: http.StatusInternalServerError, Message: "boom"}},
			wantReason:   "boom",
			wantRequests: 1,
		},
		{
			// A zero-valued response decodes to an empty decision, so anything
			// unrecognized has to block rather than read as "not a deny".
			name:         "response carries no decision",
			hook:         hookCase{agent: "claude-code", event: "PreToolUse", payload: claudeShellCall},
			wantReason:   "no recognized decision",
			wantRequests: 1,
		},
		{
			name: "response carries an unrecognized decision",
			hook: hookCase{agent: "claude-code", event: "PreToolUse", payload: claudeShellCall,
				resp: types.EnforcementDecisionResponse{Decision: "maybe"}},
			wantReason:   `no recognized decision ("maybe")`,
			wantRequests: 1,
		},
		{
			name: "policy deny",
			hook: hookCase{agent: "claude-code", event: "PreToolUse", payload: claudeMCPCall,
				resp: types.EnforcementDecisionResponse{Decision: types.EnforcementDecisionDeny, Reason: "no matching allowlist entry"}},
			wantReason:   "no matching allowlist entry",
			wantRequests: 1,
		},
		{
			// The device reports what it could not identify and honors the answer:
			// Obot is the only decider. Here it denies, as it does under every
			// allowlist configuration.
			name: "unresolvable call that Obot denies",
			hook: hookCase{agent: "claude-code", event: "PreToolUse", payload: claudeUnknownServerCall,
				resp: types.EnforcementDecisionResponse{Decision: types.EnforcementDecisionDeny, Reason: "the call could not be identified"}},
			wantReason:   "the call could not be identified",
			wantRequests: 1,
		},
		{
			name: "cursor policy deny",
			hook: hookCase{agent: "cursor", event: "beforeMCPExecution",
				payload: `{"tool_name":"echo","mcp_server_name":"probe-npx-stdio","workspace_roots":["/Users/dev/probe-workspace"]}`,
				resp:    types.EnforcementDecisionResponse{Decision: types.EnforcementDecisionDeny, Reason: "no matching allowlist entry"}},
			wantReason:   "no matching allowlist entry",
			wantRequests: 1,
		},
	}

	env := normalizeFixture(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := runHook(t, env, tc.hook)

			if !run.Denied {
				t.Fatalf("the call was not denied: %+v", run.Result)
			}
			if !strings.Contains(run.Reason, tc.wantReason) {
				t.Errorf("reason = %q, want it to name %q", run.Reason, tc.wantReason)
			}
			if len(run.requests) != tc.wantRequests {
				t.Errorf("issued %d decision requests, want %d", len(run.requests), tc.wantRequests)
			}
			if tc.wantNoResponse {
				if len(run.Response) != 0 || run.stdout != "" {
					t.Errorf("wrote %q to the protocol channel, want nothing for an unusable invocation", run.stdout)
				}
				if !run.Unusable {
					t.Error("an invocation with no response shape must report itself unusable")
				}
				return
			}
			if run.Unusable {
				t.Error("an invocation that produced a response must not report itself unusable")
			}
			if run.stdout != string(run.Response) {
				t.Errorf("stdout = %q, want exactly the protocol response %q", run.stdout, run.Response)
			}
			assertDenyResponse(t, tc.hook.agent, run)
		})
	}
}

// assertDenyResponse checks that a deny reached the model in the agent's own
// shape, carrying the reason and the do-not-work-around instructions.
func assertDenyResponse(t *testing.T, agent string, run hookRun) {
	t.Helper()
	if len(run.Response) == 0 {
		t.Fatal("no protocol response was emitted for a deny")
	}

	var message string
	if agent == string(localagent.Cursor) {
		var out struct {
			Permission  string `json:"permission"`
			UserMessage string `json:"user_message"`
		}
		if err := json.Unmarshal(run.Response, &out); err != nil {
			t.Fatalf("deny response is not JSON: %v (%s)", err, run.Response)
		}
		if out.Permission != "deny" {
			t.Errorf("permission = %q, want deny", out.Permission)
		}
		message = out.UserMessage
	} else {
		var out struct {
			HookSpecificOutput struct {
				PermissionDecision       string `json:"permissionDecision"`
				PermissionDecisionReason string `json:"permissionDecisionReason"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(run.Response, &out); err != nil {
			t.Fatalf("deny response is not JSON: %v (%s)", err, run.Response)
		}
		if out.HookSpecificOutput.PermissionDecision != "deny" {
			t.Errorf("permissionDecision = %q, want deny", out.HookSpecificOutput.PermissionDecision)
		}
		message = out.HookSpecificOutput.PermissionDecisionReason
	}

	if message == "" {
		t.Fatal("the deny carried no message to the model")
	}
	wantRetryGuard := "Do not attempt the same result by any other route"
	if agent == string(localagent.Cursor) {
		wantRetryGuard = "Do not try again"
	}
	if !strings.Contains(message, wantRetryGuard) {
		t.Errorf("deny message does not tell the reader not to retry (wanted %q): %q", wantRetryGuard, message)
	}
}

// A permitted call is answered by withholding the denial, never by granting
// permission: zero bytes for Claude Code and Codex, and Cursor's explicit allow,
// which means "this hook does not object" rather than "skip the user's approval".
func TestRunAllowWithholdsPermissionRatherThanGrantingIt(t *testing.T) {
	env := normalizeFixture(t)
	cases := []struct {
		agent   string
		event   string
		payload string
		want    string
	}{
		{"claude-code", "PreToolUse", claudeMCPCall, ""},
		{"claude-code", "PreToolUse", claudeShellCall, ""},
		{"codex", "PreToolUse", `{"tool_name":"mcp__probe_npx_stdio__echo","cwd":"/Users/dev/probe-workspace"}`, ""},
		{"cursor", "beforeMCPExecution", `{"tool_name":"echo","mcp_server_name":"probe-npx-stdio","workspace_roots":["/Users/dev/probe-workspace"]}`, allowResponse},
		{"cursor", "preToolUse", `{"tool_name":"Shell"}`, allowResponse},
	}

	for _, tc := range cases {
		t.Run(tc.agent+"/"+tc.event, func(t *testing.T) {
			run := runHook(t, env, hookCase{
				agent:   tc.agent,
				event:   tc.event,
				payload: tc.payload,
				resp:    types.EnforcementDecisionResponse{Decision: types.EnforcementDecisionAllow},
			})

			if run.Denied {
				t.Fatalf("an allowed call was denied: %s", run.Reason)
			}
			if run.stdout != tc.want {
				t.Fatalf("stdout = %q, want %q", run.stdout, tc.want)
			}
			if strings.Contains(run.stdout, `"permissionDecision":"allow"`) {
				t.Fatalf("the allow path granted permission: %s", run.stdout)
			}
			if run.stderr != "" {
				t.Errorf("an allowed call wrote to stderr: %q", run.stderr)
			}
		})
	}
}

func TestRunIssuesExactlyOneDecisionPerCall(t *testing.T) {
	env := normalizeFixture(t)
	for _, tc := range []struct {
		name    string
		agent   string
		event   string
		payload string
	}{
		{"resolved mcp call", "claude-code", "PreToolUse", claudeMCPCall},
		{"built-in tool call", "claude-code", "PreToolUse", claudeShellCall},
		{"unresolvable mcp call", "claude-code", "PreToolUse", claudeUnknownServerCall},
		{"cursor mcp call", "cursor", "beforeMCPExecution", `{"tool_name":"echo","mcp_server_name":"probe-npx-stdio","workspace_roots":["/Users/dev/probe-workspace"]}`},
		{"cursor built-in call", "cursor", "preToolUse", `{"tool_name":"Read"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := runHook(t, env, hookCase{
				agent:   tc.agent,
				event:   tc.event,
				payload: tc.payload,
				resp:    types.EnforcementDecisionResponse{Decision: types.EnforcementDecisionAllow},
			})
			if len(run.requests) != 1 {
				t.Fatalf("issued %d decision requests, want exactly one", len(run.requests))
			}
			if !run.Requested {
				t.Error("the result does not record that a decision was requested")
			}
		})
	}
}

func TestRunReportsUnresolvedAndHonorsTheAnswer(t *testing.T) {
	env := normalizeFixture(t)
	run := runHook(t, env, hookCase{
		agent:   "claude-code",
		event:   "PreToolUse",
		payload: claudeUnknownServerCall,
		resp:    types.EnforcementDecisionResponse{Decision: types.EnforcementDecisionAllow},
	})

	if len(run.requests) != 1 {
		t.Fatalf("issued %d decision requests, want exactly one", len(run.requests))
	}
	req := run.requests[0]
	if !req.Unresolved {
		t.Error("the request did not report the call as unresolved")
	}
	if !strings.Contains(req.UnresolvedReason, `"not-configured" was not found`) {
		t.Errorf("unresolvedReason = %q, want it to name the server and the miss", req.UnresolvedReason)
	}
	if req.ServerName != "not-configured" {
		t.Errorf("serverName = %q, want the only name we have", req.ServerName)
	}
	if req.Kind != "mcp" || req.Tool != "do" {
		t.Errorf("request = %+v, want the MCP call it was", req)
	}

	if run.Denied {
		t.Fatalf("the device overrode Obot's allow on an unresolved call: %s", run.Reason)
	}
	if run.stdout != "" {
		t.Errorf("stdout = %q, want the withheld denial", run.stdout)
	}
}

func TestRunReportsPartialIdentityForAMatchedButUnresolvableEntry(t *testing.T) {
	f := newFixture(t, "darwin")
	f.write(f.homePath(".claude.json"), `{"mcpServers":{
		"local-binary": {"command": "/opt/homebrew/bin/some-server", "args": ["--token", "secret"]}
	}}`)

	run := runHook(t, f.Env, hookCase{
		agent:   "claude-code",
		event:   "PreToolUse",
		payload: `{"tool_name":"mcp__local-binary__do","cwd":"` + f.Home + `"}`,
		resp:    types.EnforcementDecisionResponse{Decision: types.EnforcementDecisionDeny, Reason: "unidentified"},
	})

	if len(run.requests) != 1 {
		t.Fatalf("issued %d decision requests, want exactly one", len(run.requests))
	}
	req := run.requests[0]
	if !req.Unresolved || req.ServerName != "local-binary" {
		t.Errorf("request = %+v, want an unresolved call named by its configuration key", req)
	}
	if req.Server.Command != "/opt/homebrew/bin/some-server" {
		t.Errorf("command = %q, want the bare executable", req.Server.Command)
	}
	if strings.Contains(string(mustJSON(req)), "secret") {
		t.Errorf("the request carried a command argument: %s", mustJSON(req))
	}
}

func TestRunSkipsCursorMCPPreToolUse(t *testing.T) {
	env := normalizeFixture(t)
	run := runHook(t, env, hookCase{
		agent:   "cursor",
		event:   "preToolUse",
		payload: string(loadPayload(t, "cursor-pretooluse-mcp.json")),
		// Any answer here would be a failure: the stub must not be called.
		resp: types.EnforcementDecisionResponse{Decision: types.EnforcementDecisionDeny, Reason: "should never be asked"},
	})

	if len(run.requests) != 0 {
		t.Fatalf("issued %d decision requests for an already-decided call, want none", len(run.requests))
	}
	if !run.Skipped {
		t.Error("the result does not record the skip")
	}
	if run.Denied || run.stdout != allowResponse {
		t.Fatalf("stdout = %q (denied=%v), want Cursor's explicit allow", run.stdout, run.Denied)
	}
}

func TestRunDryRunNeitherAsksNorAnswers(t *testing.T) {
	env := normalizeFixture(t)
	for _, tc := range []struct {
		name       string
		payload    string
		wantStderr string
	}{
		{"resolved call", claudeMCPCall, "would: ALLOW (policy not consulted; --dry-run)"},
		{"unresolvable call", claudeUnknownServerCall, "would: DENY (unresolved:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := runHook(t, env, hookCase{
				agent:   "claude-code",
				event:   "PreToolUse",
				payload: tc.payload,
				dryRun:  true,
				resp:    types.EnforcementDecisionResponse{Decision: types.EnforcementDecisionDeny, Reason: "should never be asked"},
			})

			if len(run.requests) != 0 {
				t.Errorf("a dry run issued %d decision requests, want none", len(run.requests))
			}
			if run.stdout != "" {
				t.Errorf("a dry run wrote %q to the protocol channel, want nothing", run.stdout)
			}
			if !strings.Contains(run.stderr, tc.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", run.stderr, tc.wantStderr)
			}
		})
	}
}

func TestRunPrintNormalizedStillDecides(t *testing.T) {
	env := normalizeFixture(t)
	run := runHook(t, env, hookCase{
		agent:           "claude-code",
		event:           "PreToolUse",
		payload:         claudeMCPCall,
		printNormalized: true,
		resp:            types.EnforcementDecisionResponse{Decision: types.EnforcementDecisionAllow},
	})

	if len(run.requests) != 1 {
		t.Fatalf("issued %d decision requests, want exactly one", len(run.requests))
	}
	var printed types.EnforcementDecisionRequest
	if err := json.Unmarshal([]byte(run.stdout), &printed); err != nil {
		t.Fatalf("stdout is not the normalized request: %v (%q)", err, run.stdout)
	}
	if printed.Agent != "claude_code" || printed.ServerName != "probe-npx-stdio" || printed.Kind != "mcp" {
		t.Errorf("printed request = %+v, want the normalized call", printed)
	}
	// The tool input is never read, so it can never be printed.
	if strings.Contains(run.stdout, "tool_input") {
		t.Errorf("the normalized output carried tool parameters: %s", run.stdout)
	}
}

func TestRunKeepsWarningsOffTheProtocolChannel(t *testing.T) {
	env := normalizeFixture(t)
	run := runHook(t, env, hookCase{
		agent:   "claude-code",
		event:   "PreToolUse",
		payload: claudeMCPCall,
		err:     errors.New("dial tcp: connection refused"),
	})

	if run.stdout != string(run.Response) {
		t.Fatalf("stdout = %q, want exactly the protocol response", run.stdout)
	}
	if !json.Valid([]byte(run.stdout)) {
		t.Fatalf("stdout is not valid JSON: %q", run.stdout)
	}
	if !strings.Contains(run.stderr, "connection refused") {
		t.Errorf("stderr = %q, want the blocking reason", run.stderr)
	}
}

func TestRunRefusesAnOversizedPayload(t *testing.T) {
	env := normalizeFixture(t)
	run := runHook(t, env, hookCase{
		agent: "claude-code",
		event: "PreToolUse",
		input: io.MultiReader(
			strings.NewReader(`{"tool_name":"Bash","padding":"`),
			io.LimitReader(zeroes{}, maxPayloadBytes),
			strings.NewReader(`"}`),
		),
	})

	if !run.Denied || !strings.Contains(run.Reason, "exceeds") {
		t.Fatalf("result = %+v, want a deny naming the size bound", run.Result)
	}
	if len(run.requests) != 0 {
		t.Errorf("issued %d decision requests for an unreadable payload, want none", len(run.requests))
	}
}

// Neither channel may carry an unbounded server error. A non-2xx surfaces the
// whole response body — half a megabyte of HTML through a reverse proxy — and
// stderr is not a safe place for it either: Claude Code surfaces hook stderr into
// the transcript.
func TestRunBoundsAnUnboundedServerError(t *testing.T) {
	env := normalizeFixture(t)
	run := runHook(t, env, hookCase{
		agent:   "claude-code",
		event:   "PreToolUse",
		payload: claudeMCPCall,
		err:     errors.New("error code 404 (Not Found): <!doctype html>\n" + strings.Repeat("padding ", 100_000)),
	})

	if !run.Denied {
		t.Fatal("a non-2xx did not block the call")
	}
	if len(run.stderr) > 2_000 {
		t.Errorf("stderr is %d bytes; an unbounded server body reached the transcript", len(run.stderr))
	}
	if len(run.stdout) > 4_000 {
		t.Errorf("the protocol response is %d bytes; an unbounded server body reached the model", len(run.stdout))
	}
	for name, text := range map[string]string{"stderr": run.stderr, "stdout": run.stdout} {
		if !strings.Contains(text, "404") {
			t.Errorf("%s lost the part that identifies the failure: %q", name, text)
		}
	}
}

// zeroes is an endless source of a harmless filler byte.
type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}
