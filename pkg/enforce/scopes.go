package enforce

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

const (
	mcpServersKey   = "mcpServers"
	codexServersKey = "mcp_servers"
)

// serverSet is a decoded MCP servers table, keyed as its file spells the names.
type serverSet map[string]mcpEntry

// scope is one place an agent can declare an MCP server.
type scope struct {
	// path and key name the source, for the trace.
	path string
	key  string
	// rank orders precedence: lower wins, and scopes sharing a rank are peers.
	rank int
	// closed marks a scope whose server set the agent treats as exclusive, so a
	// miss here ends resolution instead of falling through to lower ranks.
	closed bool
	load   func() (serverSet, loadResult)
}

func (s scope) traceKey(matched string) string {
	if matched == "" {
		return s.key
	}
	return fmt.Sprintf("%s[%q]", s.key, matched)
}

// lookup is the ladder of names to try against a server set, most literal first.
type lookup struct {
	names []string
	form  namespaceForm
}

// matchName runs a name ladder against the keys of m. Every name is tried exactly
// before any is retried through the form, so a later rung can never beat an exact
// hit on an earlier one.
func matchName[T any](l lookup, m map[string]T) (value T, key string, found bool) {
	for _, name := range l.names {
		if v, ok := m[name]; ok {
			return v, name, true
		}
	}
	if l.form == nil {
		return value, "", false
	}
	keys := slices.Sorted(maps.Keys(m))
	for _, name := range l.names {
		for _, k := range keys {
			if l.form(k) == name {
				return m[k], k, true
			}
		}
	}
	return value, "", false
}

// note describes a match whose key is not the name the call reported.
func (l lookup) note(matched string) string {
	if len(l.names) > 0 && matched == l.names[0] {
		return ""
	}
	return fmt.Sprintf("matched as %q", matched)
}

// outcome is what walking a scope list concluded.
type outcome int

const (
	// outcomeMiss means no scope declared the name.
	outcomeMiss outcome = iota
	// outcomeFound means one identity was established.
	outcomeFound
	// outcomeClosed means a closed scope ended resolution without a match.
	outcomeClosed
	// outcomeAmbiguous means peer scopes defined the name differently.
	outcomeAmbiguous
)

// match is a server declaration found in some scope.
type match struct {
	key   string
	entry mcpEntry
}

// resolveScopes walks scopes, which must be ordered by rank, and returns the
// declaration that governs the call.
//
// Rank is the whole of precedence, so scopes sharing a rank are peers: nothing
// orders them, and a name they define differently is ambiguous rather than settled
// by search order.
func resolveScopes(scopes []scope, names lookup, tr *tracer) (match, outcome) {
	for i := 0; i < len(scopes); {
		var (
			peers  []match
			closed bool
		)
		j := i
		for ; j < len(scopes) && scopes[j].rank == scopes[i].rank; j++ {
			s := scopes[j]
			set, res := s.load()
			if res != loadOK {
				tr.miss(s.path, s.traceKey(""), res)
				continue
			}
			entry, key, ok := matchName(names, set)
			if !ok {
				tr.miss(s.path, s.traceKey(""), res)
				closed = closed || s.closed
				continue
			}
			tr.hit(s.path, s.traceKey(key), names.note(key))
			peers = append(peers, match{key: key, entry: entry})
		}
		i = j

		switch {
		case len(peers) == 1:
			return peers[0], outcomeFound
		case len(peers) > 1:
			if agreed, ok := agree(peers); ok {
				return agreed, outcomeFound
			}
			return match{}, outcomeAmbiguous
		case closed:
			return match{}, outcomeClosed
		}
	}
	return match{}, outcomeMiss
}

// agree collapses peers that repeat one definition, which identifies the server
// just as well as a single scope would. Only a genuine conflict is unresolvable.
func agree(peers []match) (match, bool) {
	for _, peer := range peers[1:] {
		if !sameEntry(peers[0].entry, peer.entry) {
			return match{}, false
		}
	}
	return peers[0], true
}

func sameEntry(a, b mcpEntry) bool {
	return a.URL == b.URL && a.Command == b.Command && slices.Equal(a.Args, b.Args)
}

func ambiguous(agent localagent.Agent, name string) Resolution {
	return unresolved(name, fmt.Sprintf(
		"MCP server %q is declared with conflicting definitions in more than one %s configuration scope and the hook cannot tell which one ran; rename one of them",
		name, agent.DisplayName()))
}

// ambiguousToolName reports a tool name that divides into a server and a tool in
// more than one way, with more than one reading naming a configured server.
//
// The sibling of ambiguous(): that one is two scopes claiming one name, this one is
// one name claiming two servers. Both end the same way, because the honest answer is
// that the hook cannot say which server ran.
func ambiguousToolName(agent localagent.Agent, reported string, candidates []string) Resolution {
	slices.Sort(candidates)
	return unresolved(reported, fmt.Sprintf(
		"the tool name divides into an MCP server and a tool in more than one way, and %s configuration declares more than one of the resulting servers (%s), so the hook cannot tell which one ran; rename one of them",
		agent.DisplayName(), strings.Join(candidates, ", ")))
}

func jsonServers(path string) func() (serverSet, loadResult) {
	return func() (serverSet, loadResult) {
		var doc struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if res := loadJSON(path, &doc); res != loadOK {
			return nil, res
		}
		return decodeServers(doc.MCPServers), loadOK
	}
}

func codexServers(path string) func() (serverSet, loadResult) {
	return func() (serverSet, loadResult) {
		var doc struct {
			MCPServers serverSet `toml:"mcp_servers"`
		}
		if res := loadTOML(path, &doc); res != loadOK {
			return nil, res
		}
		return doc.MCPServers, loadOK
	}
}

// fixedServers serves an already-decoded table, for scopes that share one file.
func fixedServers(set serverSet, res loadResult) func() (serverSet, loadResult) {
	return func() (serverSet, loadResult) { return set, res }
}

// decodeServers decodes each entry on its own so one malformed sibling cannot cost
// the whole table, and with it every other server configured in it. A key that
// fails to decode stays present with a zero entry: it names a configured server
// that identifies nothing, which is a match rather than an invitation for a
// lower-ranked scope to answer for it.
func decodeServers(raw map[string]json.RawMessage) serverSet {
	set := make(serverSet, len(raw))
	for name, msg := range raw {
		var entry mcpEntry
		_ = json.Unmarshal(msg, &entry)
		set[name] = entry
	}
	return set
}
