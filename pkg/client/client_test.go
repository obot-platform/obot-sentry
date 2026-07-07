package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/obot-platform/obot/apiclient/types"

	"github.com/obot-platform/obocop/pkg/identity"
)

// Each live submit (and each spool replay) must mint a fresh short-lived device
// JWT rather than reusing a cached token.
func TestSubmitLocalAgentAuditLogsMintsFreshJWTPerCall(t *testing.T) {
	var tokens []string
	var requests []types.LocalAgentToolCallAuditLogSubmitRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/local-agent-audit-logs" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		tokens = append(tokens, r.Header.Get("Authorization"))
		var request types.LocalAgentToolCallAuditLogSubmitRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode audit request: %v", err)
		} else {
			requests = append(requests, request)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	id, err := identity.Load(t.TempDir())
	if err != nil {
		t.Fatalf("load identity: %v", err)
	}
	c := New(srv.URL)
	logs := []types.LocalAgentToolCallAuditLogInput{{
		Action:  types.LocalAgentToolCallAuditLogAction{Name: "Bash", Kind: "shell"},
		Target:  types.LocalAgentToolCallAuditLogTarget{TargetType: types.AuditLogTargetTypeLocalTool, Name: "Bash"},
		Outcome: types.LocalAgentToolCallAuditLogOutcome{Status: types.AuditLogOutcomeStatusSuccess},
		Details: types.LocalAgentToolCallAuditLogReportedDetails{
			Trace: types.LocalAgentToolCallAuditLogTrace{IdempotencyKey: "e1"},
		},
	}}

	for i := range 2 {
		if err := c.SubmitLocalAgentAuditLogs(t.Context(), id, logs); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}

	if len(tokens) != 2 {
		t.Fatalf("expected two submits, got %d", len(tokens))
	}
	if len(requests) != 2 || len(requests[0].Events) != 1 {
		t.Fatalf("expected two nested event requests, got %#v", requests)
	}
	event := requests[0].Events[0]
	if event.Target.TargetType != types.AuditLogTargetTypeLocalTool ||
		event.Outcome.Status != types.AuditLogOutcomeStatusSuccess ||
		event.Details.Trace.IdempotencyKey != "e1" {
		t.Fatalf("unexpected submitted event shape: %#v", event)
	}
	for i, tok := range tokens {
		if len(tok) <= len("Bearer ") || tok[:len("Bearer ")] != "Bearer " {
			t.Fatalf("submit %d missing bearer device JWT, got %q", i, tok)
		}
	}
	if tokens[0] == tokens[1] {
		t.Fatal("expected a freshly minted device JWT per submit, got identical tokens")
	}
}
