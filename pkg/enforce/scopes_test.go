package enforce

import (
	"context"
	"slices"
	"testing"
)

// fixedScope builds a scope serving set, for exercising the engine without a
// filesystem.
func fixedScope(name string, rank int, set serverSet) scope {
	return scope{path: name, key: mcpServersKey, rank: rank, load: fixedServers(set, loadOK)}
}

func urlSet(name, url string) serverSet {
	return serverSet{name: {URL: url}}
}

func exactly(name string) lookup {
	return lookup{names: []string{name}}
}

func TestResolveScopesRankOrder(t *testing.T) {
	scopes := []scope{
		fixedScope("high", 0, urlSet("linear", "https://high.example.com/sse")),
		fixedScope("low", 1, urlSet("linear", "https://low.example.com/sse")),
	}

	m, out := resolveScopes(t.Context(), scopes, exactly("linear"), &tracer{})
	if out != outcomeFound {
		t.Fatalf("outcome = %v, want outcomeFound", out)
	}
	if m.entry.URL != "https://high.example.com/sse" {
		t.Fatalf("URL = %q, want the lower rank to win", m.entry.URL)
	}
}

// TestResolveScopesStopsAtTheFirstRankThatMatches covers the rule that makes rank
// the whole of precedence: a lower rank is never even loaded once a higher one has
// answered.
func TestResolveScopesStopsAtTheFirstRankThatMatches(t *testing.T) {
	loaded := false
	scopes := []scope{
		fixedScope("high", 0, urlSet("linear", "https://high.example.com/sse")),
		{path: "low", key: mcpServersKey, rank: 1, load: func(context.Context) (serverSet, loadResult) {
			loaded = true
			return urlSet("linear", "https://low.example.com/sse"), loadOK
		}},
	}

	if _, out := resolveScopes(t.Context(), scopes, exactly("linear"), &tracer{}); out != outcomeFound {
		t.Fatalf("outcome = %v, want outcomeFound", out)
	}
	if loaded {
		t.Fatal("a lower-ranked scope was loaded after a higher one answered")
	}
}

func TestResolveScopesMiss(t *testing.T) {
	scopes := []scope{fixedScope("only", 0, urlSet("github", "https://github.example.com/sse"))}

	if _, out := resolveScopes(t.Context(), scopes, exactly("linear"), &tracer{}); out != outcomeMiss {
		t.Fatalf("outcome = %v, want outcomeMiss", out)
	}
}

// TestResolveScopesPeersThatConflict covers the reason peers exist: with nothing
// to order two scopes, a name they define differently cannot be resolved by
// picking one.
func TestResolveScopesPeersThatConflict(t *testing.T) {
	scopes := []scope{
		fixedScope("a", 0, urlSet("linear", "https://a.example.com/sse")),
		fixedScope("b", 0, urlSet("linear", "https://b.example.com/sse")),
	}

	if _, out := resolveScopes(t.Context(), scopes, exactly("linear"), &tracer{}); out != outcomeAmbiguous {
		t.Fatalf("outcome = %v, want outcomeAmbiguous", out)
	}
}

// TestResolveScopesPeersThatAgree covers the other half: peers repeating one
// definition identify it as well as a single scope would, so which one ran does
// not matter.
func TestResolveScopesPeersThatAgree(t *testing.T) {
	scopes := []scope{
		fixedScope("a", 0, serverSet{"linear": {Command: "npx", Args: []string{"-y", "linear-mcp"}}}),
		fixedScope("b", 0, serverSet{"linear": {Command: "npx", Args: []string{"-y", "linear-mcp"}}}),
	}

	m, out := resolveScopes(t.Context(), scopes, exactly("linear"), &tracer{})
	if out != outcomeFound {
		t.Fatalf("outcome = %v, want outcomeFound", out)
	}
	if !slices.Equal(m.entry.Args, []string{"-y", "linear-mcp"}) {
		t.Fatalf("entry = %+v", m.entry)
	}
}

func TestResolveScopesPeersWithDifferentEnvironmentConflict(t *testing.T) {
	base := mcpEntry{
		Command: "npx",
		Args:    []string{"-y", "linear-mcp"},
		Environment: map[string]string{
			"LINEAR_TOKEN": "project-token",
		},
	}
	shadow := base
	shadow.Environment = map[string]string{"LINEAR_TOKEN": "user-token"}
	scopes := []scope{
		fixedScope("project", 0, serverSet{"linear": base}),
		fixedScope("user", 0, serverSet{"linear": shadow}),
	}

	if _, out := resolveScopes(t.Context(), scopes, exactly("linear"), &tracer{}); out != outcomeAmbiguous {
		t.Fatalf("outcome = %v, want outcomeAmbiguous for definitions that differ by environment", out)
	}
}

// TestResolveScopesPeersRecordEveryMatch covers the diagnostic: for a conflict,
// the two FOUND lines are the whole explanation of the denial.
func TestResolveScopesPeersRecordEveryMatch(t *testing.T) {
	scopes := []scope{
		fixedScope("a", 0, urlSet("linear", "https://a.example.com/sse")),
		fixedScope("b", 0, urlSet("linear", "https://b.example.com/sse")),
	}

	tr := &tracer{}
	resolveScopes(t.Context(), scopes, exactly("linear"), tr)

	var matched []string
	for _, step := range tr.steps {
		if step.Matched {
			matched = append(matched, step.Path)
		}
	}
	if !slices.Equal(matched, []string{"a", "b"}) {
		t.Fatalf("matched steps = %v, want both scopes", matched)
	}
}

func TestMatchNameExactBeatsFormAcrossTheLadder(t *testing.T) {
	set := serverSet{
		"user-linear": {URL: "https://prefixed.example.com/sse"},
		"user.linear": {URL: "https://folded.example.com/sse"},
	}

	names := lookup{names: []string{"user-linear", "user_linear"}, form: formCodex}
	entry, key, ok := matchName(names, set)
	if !ok {
		t.Fatal("no match")
	}
	if key != "user-linear" || entry.URL != "https://prefixed.example.com/sse" {
		t.Fatalf("matched %q (%s), want the exact hit on the first rung", key, entry.URL)
	}
}

func TestMatchNameFormMatchesForward(t *testing.T) {
	set := serverSet{"my-linear": {URL: "https://linear.example.com/sse"}}

	if _, _, ok := matchName(exactly("my_linear"), set); ok {
		t.Fatal("matched without a form; only an exact key may match then")
	}
	_, key, ok := matchName(lookup{names: []string{"my_linear"}, form: formCodex}, set)
	if !ok {
		t.Fatal("Codex's fold did not match its own configuration key")
	}
	if key != "my-linear" {
		t.Fatalf("matched key %q, want the configuration key as the user wrote it", key)
	}

	// Not the reverse: the report is never folded. A key that is already the folded
	// form does not match a report carrying the unfolded one, because no agent sends
	// that.
	folded := serverSet{"my_linear": {URL: "https://linear.example.com/sse"}}
	if _, _, ok := matchName(lookup{names: []string{"my-linear"}, form: formCodex}, folded); ok {
		t.Fatal("matched backwards; the reported name must not be transformed")
	}
}

func TestMatchNameFormIsDeterministicOnCollision(t *testing.T) {
	set := serverSet{
		"my-server": {URL: "https://hyphen.example.com/sse"},
		"my.server": {URL: "https://dot.example.com/sse"},
	}
	names := lookup{names: []string{"my_server"}, form: formCodex}

	_, first, ok := matchName(names, set)
	if !ok {
		t.Fatal("no match")
	}
	if first != "my-server" {
		t.Fatalf("matched %q, want the first key in sorted order", first)
	}
	for range 20 {
		if _, key, _ := matchName(names, set); key != first {
			t.Fatalf("matched %q then %q; collision resolution is not deterministic", first, key)
		}
	}
}

// TestDecodeServersKeepsMalformedSiblings covers the tolerance rule: one entry
// that does not decode must not cost every other server in the file.
func TestDecodeServersKeepsMalformedSiblings(t *testing.T) {
	f := newFixture(t, "darwin")
	path := f.write(f.path("mcp.json"), `{"mcpServers":{
		"broken": ["not", "an", "object"],
		"linear": {"url":"https://linear.example.com/sse"}
	}}`)

	set, res := jsonServers(newConfigLoader(), path)(t.Context())
	if res != loadOK {
		t.Fatalf("load = %v, want loadOK", res)
	}
	if set["linear"].URL != "https://linear.example.com/sse" {
		t.Fatalf("the healthy sibling did not decode: %+v", set)
	}
	// The broken key stays present: it names a configured server that identifies
	// nothing, which is a match rather than an invitation to look further down.
	entry, ok := set["broken"]
	if !ok {
		t.Fatal("the malformed entry went missing")
	}
	if !sameEntry(entry, mcpEntry{}) {
		t.Fatalf("malformed entry = %+v, want the zero entry", entry)
	}
}
