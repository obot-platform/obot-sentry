package client

import (
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/obot-platform/obot/apiclient/types"

	"github.com/obot-platform/obot-sentry/pkg/identity"
)

// decisionPath is where NormalizeBaseURL plus apiclient's own path have to land.
const decisionPath = "/api/enforcement/decisions"

func decideClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func testIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Load(t.TempDir())
	if err != nil {
		t.Fatalf("load identity: %v", err)
	}
	return id
}

func assertDeviceJWT(t *testing.T, id *identity.Identity, authorization string) {
	t.Helper()
	raw, ok := stripBearer(authorization)
	if !ok {
		t.Fatalf("Authorization = %q, want a bearer token", authorization)
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"ES256", "ES384", "ES512", "EdDSA"}),
		jwt.WithAudience(identity.DeviceTokenAudience),
		jwt.WithExpirationRequired(),
	)
	claims := &jwt.RegisteredClaims{}
	if _, err := parser.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) {
		return x509.ParsePKIXPublicKey(id.PublicKeyDER)
	}); err != nil {
		t.Fatalf("bearer is not a valid device JWT: %v", err)
	}
	if !slices.Contains(claims.Audience, identity.DeviceTokenAudience) {
		t.Errorf("aud = %v, want %q", claims.Audience, identity.DeviceTokenAudience)
	}
	if claims.Subject != id.DeviceID || claims.Issuer != claims.Subject {
		t.Errorf("iss = %q, sub = %q, want both %q", claims.Issuer, claims.Subject, id.DeviceID)
	}
}

func stripBearer(authorization string) (string, bool) {
	const prefix = "Bearer "
	if len(authorization) <= len(prefix) || authorization[:len(prefix)] != prefix {
		return "", false
	}
	return authorization[len(prefix):], true
}

func TestDecideReturnsTheVerdict(t *testing.T) {
	for _, want := range []types.EnforcementDecisionResponse{
		{Decision: types.EnforcementDecisionAllow, Reason: "matched an allowlisted server entry"},
		{Decision: types.EnforcementDecisionDeny, Reason: "no matching allowlist entry"},
		{Decision: types.EnforcementDecisionAllow},
	} {
		t.Run(want.Decision+"/"+want.Reason, func(t *testing.T) {
			var (
				gotPath string
				gotBody []byte
			)
			id := testIdentity(t)
			c := decideClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotBody, _ = io.ReadAll(r.Body)
				assertDeviceJWT(t, id, r.Header.Get("Authorization"))
				_ = json.NewEncoder(w).Encode(want)
			})

			req := types.EnforcementDecisionRequest{
				Agent:      "claude_code",
				Tool:       "search_issues",
				Kind:       "mcp",
				ServerName: "linear",
				Server: types.EnforcementDecisionServer{
					Package: &types.AllowlistServerPackage{
						Source: types.AllowlistServerPackageSourceNPM,
						Name:   "linear-mcp",
					},
					Command: "npx",
				},
			}
			got, err := c.Decide(t.Context(), id, req)
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if got != want {
				t.Fatalf("Decide() = %+v, want %+v", got, want)
			}
			if gotPath != decisionPath {
				t.Errorf("path = %q, want %q", gotPath, decisionPath)
			}
			var sent types.EnforcementDecisionRequest
			if err := json.Unmarshal(gotBody, &sent); err != nil {
				t.Fatalf("decode submitted request: %v", err)
			}
			if sent.Agent != req.Agent || sent.Tool != req.Tool || sent.Kind != req.Kind ||
				sent.ServerName != req.ServerName || sent.Server.Command != req.Server.Command ||
				sent.Server.Package == nil || sent.Server.Package.Name != "linear-mcp" {
				t.Errorf("submitted request = %+v, want the normalized call intact", sent)
			}
		})
	}
}

func TestDecideCarriesUnresolved(t *testing.T) {
	var sent types.EnforcementDecisionRequest
	id := testIdentity(t)
	c := decideClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode submitted request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(types.EnforcementDecisionResponse{
			Decision: types.EnforcementDecisionDeny,
			Reason:   "the call could not be identified",
		})
	})

	got, err := c.Decide(t.Context(), id, types.EnforcementDecisionRequest{
		Agent:            "codex",
		Tool:             "read_file",
		Kind:             "mcp",
		ServerName:       "probe-npx-stdio",
		Server:           types.EnforcementDecisionServer{Command: "node"},
		Unresolved:       true,
		UnresolvedReason: `stdio command "node" is not a supported package runner`,
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.Decision != types.EnforcementDecisionDeny {
		t.Errorf("decision = %q, want deny", got.Decision)
	}
	if !sent.Unresolved || sent.UnresolvedReason != `stdio command "node" is not a supported package runner` {
		t.Errorf("submitted request lost the unresolved pair: %+v", sent)
	}
	if sent.ServerName != "probe-npx-stdio" || sent.Server.Command != "node" {
		t.Errorf("submitted request lost the partial identity: %+v", sent)
	}
}

func TestDecideFailuresAreErrorsNotVerdicts(t *testing.T) {
	for _, tt := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"unauthorized", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("device not enrolled"))
		}},
		{"forbidden", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}},
		{"server error", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
		}},
		{"malformed body", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"decision": `))
		}},
		{"empty body", func(w http.ResponseWriter, _ *http.Request) {}},
		{"not json", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<html>proxy error</html>"))
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := testIdentity(t)
			c := decideClient(t, tt.handler)
			got, err := c.Decide(t.Context(), id, types.EnforcementDecisionRequest{Agent: "cursor", Tool: "echo", Kind: "mcp"})
			if err == nil {
				t.Fatalf("Decide() = %+v, want an error", got)
			}
			if got != (types.EnforcementDecisionResponse{}) {
				t.Errorf("Decide() = %+v, want the zero response alongside the error", got)
			}
		})
	}
}

func TestDecideTimesOut(t *testing.T) {
	orig := decisionTimeout
	decisionTimeout = 50 * time.Millisecond
	t.Cleanup(func() { decisionTimeout = orig })

	release := make(chan struct{})
	id := testIdentity(t)
	c := decideClient(t, func(_ http.ResponseWriter, r *http.Request) {
		// Drain the body before blocking: the server only starts watching the
		// connection for the client's disconnect once the handler has consumed
		// the request, and without that r.Context() never fires here.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	// Registered after the server's own Close cleanup so it runs first (LIFO);
	// Close waits for outstanding connections.
	t.Cleanup(func() { close(release) })

	start := time.Now()
	got, err := c.Decide(t.Context(), id, types.EnforcementDecisionRequest{Agent: "claude_code", Tool: "Bash", Kind: "shell"})
	if err == nil {
		t.Fatalf("Decide() = %+v, want a timeout error", got)
	}
	if got != (types.EnforcementDecisionResponse{}) {
		t.Errorf("Decide() = %+v, want the zero response alongside the error", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Decide blocked for %v, want the bounded budget", elapsed)
	}
}

// The shipped budget, pinned because the test above runs with a shortened one.
// 5s matches the audit submit timeout and is a hard latency ceiling on every
// tool call in every enforced agent.
func TestDecisionTimeoutIsFiveSeconds(t *testing.T) {
	if decisionTimeout != 5*time.Second {
		t.Fatalf("decisionTimeout = %v, want 5s", decisionTimeout)
	}
}

func TestDecideDoesNotInventAVerdict(t *testing.T) {
	id := testIdentity(t)
	c := decideClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"reason":"missing the decision field"}`))
	})

	got, err := c.Decide(t.Context(), id, types.EnforcementDecisionRequest{Agent: "codex", Tool: "shell", Kind: "shell"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.Decision != "" {
		t.Fatalf("decision = %q, want the empty string", got.Decision)
	}
	if got.Decision == types.EnforcementDecisionAllow {
		t.Fatal("a body with no decision must never read as an allow")
	}
}

func TestDecideMintsAFreshJWTPerCall(t *testing.T) {
	var tokens []string
	id := testIdentity(t)
	c := decideClient(t, func(w http.ResponseWriter, r *http.Request) {
		tokens = append(tokens, r.Header.Get("Authorization"))
		assertDeviceJWT(t, id, r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(types.EnforcementDecisionResponse{Decision: types.EnforcementDecisionAllow})
	})

	for i := range 2 {
		if _, err := c.Decide(t.Context(), id, types.EnforcementDecisionRequest{Agent: "claude_code", Tool: "Read", Kind: "read"}); err != nil {
			t.Fatalf("decide %d: %v", i, err)
		}
	}
	if len(tokens) != 2 {
		t.Fatalf("expected two requests, got %d", len(tokens))
	}
	if tokens[0] == tokens[1] {
		t.Fatal("expected a freshly minted device JWT per decision, got identical tokens")
	}
}
