// Package scan inventories the AI client configuration on a device.
//
// Scan reads known config locations under one or more roots (a root is
// usually a user home directory, exposed as an fs.FS), parses MCP
// server, skill, and plugin observations, and returns a
// types.DeviceScanManifest suitable for submission to the Obot backend.
//
// The package is organized around three tables rather than a per-client
// interface, because the conventions it reads are increasingly
// client-neutral — ~/.agents/skills is read by four clients and owned by
// none:
//
//   - sources (source.go): places the scan reads, each with the decoder
//     for its format. One client file per format (claudecode.go,
//     codex.go, …) holds the decoders and nothing else.
//   - skillDirs and skillTrees (skills.go): where skills live and which
//     clients read them.
//   - clients (client.go): identity plus how to tell a client is on the
//     device.
//
// The pipeline per root is: home sources → one filesystem walk →
// project-source dispatch → skill discovery → client detection.
// Observations from every root are merged into a single manifest by
// build, which attributes each artifact to the readers of the location
// it came from, intersected with the clients actually found.
package scan

import (
	"context"
	"io/fs"
	"slices"

	"github.com/obot-platform/obot/apiclient/types"
)

// DefaultMaxDepth caps how deep the walk descends below each root when
// looking for project-scope configs and SKILL.md files.
const DefaultMaxDepth = 5

// Root is one filesystem tree to scan, usually a user home directory.
type Root struct {
	// FS exposes the tree. Paths inside are slash-separated and
	// relative to the root.
	FS fs.FS
	// Path is the absolute path of the root as the scanning host sees
	// it. Wire output (file paths, project paths) is joined onto it.
	Path string
	// NativePath is the root's path as programs inside the root's own
	// environment see it, when that differs from Path — e.g. /home/user
	// for a WSL home accessed from Windows via \\wsl$. Config files
	// inside the root reference this form. Empty defaults to Path.
	NativePath string
	// Platform is the GOOS-style platform (darwin, linux, windows)
	// whose config layout applies under this root.
	Platform string
	// Primary marks the root the scanner process itself runs in.
	// Host-level presence checks ($PATH lookups, app bundle stats) only
	// make sense there, so they run for the primary root only.
	Primary bool
}

// Options configures Scan.
type Options struct {
	// Roots are the trees to scan. See DefaultRoots.
	Roots []Root
	// MaxDepth caps the walk depth in path segments below each root;
	// <= 0 means DefaultMaxDepth.
	MaxDepth int
}

// Scan runs the collection pipeline over every root and returns the
// assembled manifest. Per-file errors are dropped (a missing or
// malformed config never aborts the rest of the scan); only context
// cancellation aborts.
//
// Envelope fields (ScannerVersion, ScannedAt, DeviceID, Hostname, OS,
// Arch, Username) are left zero; the caller fills them in.
func Scan(ctx context.Context, opts Options) (types.DeviceScanManifest, error) {
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDepth
	}

	var (
		obs     observations
		files   = map[string]types.DeviceScanFile{}
		clients = map[string]types.DeviceScanClient{}
	)
	for _, root := range opts.Roots {
		if root.FS == nil {
			continue
		}
		o, err := scanRoot(ctx, newState(root, maxDepth, files, clients))
		if err != nil {
			return types.DeviceScanManifest{}, err
		}
		obs.add(o)
	}
	return build(files, clients, obs), nil
}

// scanRoot runs the pipeline against one root:
//
//  1. Every client's home scan: global configs and installed plugins.
//  2. One walk collecting project-scope config hits and SKILL.md
//     markers, skipping paths already opened as global configs.
//  3. Project hits dispatched to their owning scanner.
//  4. Skill discovery: documented skills directories first, then the
//     walk markers, attributed to the set of clients that read each
//     location.
//  5. Client detection.
func scanRoot(ctx context.Context, s *state) (observations, error) {
	var (
		obs       observations
		skipPaths = map[string]bool{}
		srcs = sources(s.platform)
	)
	for _, src := range srcs {
		if err := ctx.Err(); err != nil {
			return obs, err
		}
		if !src.Scope.has(Home) {
			continue
		}
		obs.add(src.Read(s, src.Path, ""))
		// A home config can also match its own project-scope suffix
		// (~/.cursor/mcp.json is both); suppress the redundant walk hit.
		skipPaths[src.Path] = true
	}

	hits, skillMarkers := walk(ctx, s, srcs, skipPaths)
	for _, h := range hits {
		if err := ctx.Err(); err != nil {
			return obs, err
		}
		if s.claimedUnder(h.path) {
			continue
		}
		obs.add(h.source.Read(s, h.path, h.source.projectOf(s, h.path)))
	}

	if err := ctx.Err(); err != nil {
		return obs, err
	}
	obs.skills = append(obs.skills, scanSkills(s, skillMarkers)...)

	detectClients(s)
	return obs, nil
}

// build flattens the shared tables and accumulated observations into a
// manifest. Files are path-sorted and clients name-sorted; observations
// stay in emit order.
//
// Skills carry a set of discovering clients internally, but the wire
// type can only name one client per row, so each skill is emitted once
// per client (rows share the same File). A skill is attributed only to
// clients reported() finds on the device; one no reported client
// discovers is emitted once as MultiClient, so nothing is dropped.
func build(files map[string]types.DeviceScanFile, clients map[string]types.DeviceScanClient, obs observations) types.DeviceScanManifest {
	out := types.DeviceScanManifest{
		Files:      make([]types.DeviceScanFile, 0, len(files)),
		Clients:    make([]types.DeviceScanClient, 0, len(clients)),
		MCPServers: obs.servers,
		Skills:     make([]types.DeviceScanSkill, 0, len(obs.skills)),
		Plugins:    obs.plugins,
	}
	if out.MCPServers == nil {
		out.MCPServers = []types.DeviceScanMCPServer{}
	}
	if out.Plugins == nil {
		out.Plugins = []types.DeviceScanPlugin{}
	}

	report := reported(clients, obs)
	for _, sk := range obs.skills {
		entry := sk.DeviceScanSkill
		var attributed bool
		for _, name := range sk.clients {
			if !report[name] {
				continue
			}
			entry.Client = name
			out.Skills = append(out.Skills, entry)
			attributed = true
		}
		if !attributed {
			entry.Client = MultiClient
			out.Skills = append(out.Skills, entry)
		}
	}

	for _, p := range sortedKeys(files) {
		out.Files = append(out.Files, files[p])
	}

	var (
		hasMCP    = map[string]bool{}
		hasSkill  = map[string]bool{}
		hasPlugin = map[string]bool{}
	)
	for _, m := range out.MCPServers {
		hasMCP[m.Client] = true
	}
	for _, sk := range out.Skills {
		hasSkill[sk.Client] = true
	}
	for _, p := range out.Plugins {
		hasPlugin[p.Client] = true
	}
	for name := range report {
		if _, ok := clients[name]; !ok {
			clients[name] = types.DeviceScanClient{Name: name}
		}
	}

	for _, name := range sortedKeys(clients) {
		if !report[name] {
			continue
		}
		c := clients[name]
		c.HasMCPServers = hasMCP[name]
		c.HasSkills = hasSkill[name]
		c.HasPlugins = hasPlugin[name]
		out.Clients = append(out.Clients, c)
	}
	return out
}

// reported returns the client names that earn a clients[] row: those
// detection found on the device, and those that own configured
// artifacts — an MCP server or a plugin parsed out of the client's own
// config tree.
//
// Skills deliberately don't count. A SKILL.md is a file a user can drop
// anywhere, and the shared discovery conventions (~/.claude/skills,
// ~/.agents/skills) mean one file is readable by several clients, so
// attributing a row to each of them reports clients that were never
// installed.
func reported(clients map[string]types.DeviceScanClient, obs observations) map[string]bool {
	report := make(map[string]bool, len(clients))
	for name := range clients {
		report[name] = true
	}
	for _, m := range obs.servers {
		report[m.Client] = true
	}
	for _, p := range obs.plugins {
		report[p.Client] = true
	}
	delete(report, "")
	delete(report, MultiClient)
	return report
}

// sortedKeys returns m's keys in order. Always non-nil so wire slices
// built from it serialize as [] rather than null.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
