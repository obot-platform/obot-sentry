package scan

import "github.com/obot-platform/obot/apiclient/types"

// Scanner is the per-client integration surface. Each AI client
// (Claude Code, Cursor, etc.) implements this with one struct in its
// own file. Methods are pure: they read the fs and return observations
// instead of mutating shared state. State that genuinely is shared (the
// file and client tables) lives on state and is updated via its methods
// (addFile, addClient).
//
// Platform-dependent paths (e.g. VS Code's user config directory) take
// the root's platform so the same scanner works against a macOS,
// Linux, or Windows home.
type Scanner interface {
	// Name is the wire `client` tag this scanner emits.
	Name() string

	// Presence returns the binary/app-bundle/config-dir signals used to
	// decide whether this client is installed, regardless of whether it
	// has any config to scan.
	Presence(platform string) presenceDef

	// GlobalConfigs returns root-relative paths ScanHome opens. The
	// pipeline uses these to suppress redundant walk hits on the same
	// path (e.g. ~/.cursor/mcp.json matches Cursor's project config
	// suffix too).
	GlobalConfigs(platform string) []string

	// ProjectConfigs returns root-relative path suffixes that identify
	// this client's project-scope config files (e.g. ".cursor/mcp.json"
	// matches any */.cursor/mcp.json). Empty for clients with no
	// project-scope config. Suffixes are disjoint across scanners by
	// design; the first match wins.
	ProjectConfigs() []string

	// ScanHome scans the client's global configs and installed plugins.
	ScanHome(s *state) observations

	// ScanProject parses one project-scope config file (already known
	// to match one of ProjectConfigs).
	ScanProject(s *state, configRel string) observations
}

// scanners is the static registry consumed by the pipeline. Adding a
// new client = appending here + writing one file with a struct
// implementing Scanner. Order is alphabetical so emit order is
// deterministic.
var scanners = []Scanner{
	claudeCodeScanner{},
	claudeDesktopScanner{},
	codexScanner{},
	cursorScanner{},
	gooseScanner{},
	hermesScanner{},
	openclawScanner{},
	opencodeScanner{},
	vscodeScanner{},
	windsurfScanner{},
	zedScanner{},
}

// observations groups what scanners and skill discovery emit. Slices
// accumulate in the orchestrator; the shared file/client tables live on
// state instead.
type observations struct {
	servers []types.DeviceScanMCPServer
	skills  []skill
	plugins []types.DeviceScanPlugin
}

func (o *observations) add(other observations) {
	o.servers = append(o.servers, other.servers...)
	o.skills = append(o.skills, other.skills...)
	o.plugins = append(o.plugins, other.plugins...)
}
