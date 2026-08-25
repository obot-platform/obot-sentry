package scan

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/obot-platform/obot/apiclient/types"
)

// Client is a consumer of the locations this package inventories. It
// answers one question — is this program on the device — and names
// itself on the wire.
//
// Detection deliberately does not consult $PATH. exec.LookPath answers
// "can the process that launched me run this", which makes the reported
// client set depend on whether the scan came from a terminal or from a
// scheduler; the paths below answer the same question the same way in
// both.
type Client struct {
	// Name is the wire tag ("claude_code") every observation is
	// attributed to.
	Name string

	// Installed are globs that exist only when the client is on this
	// device: an install location, or a directory it writes by running
	// (session history, auth tokens, logs, state databases). Absolute
	// globs match the host and so apply to the primary root only;
	// relative ones match inside the scanned home. The first match is
	// reported, so install locations come before state directories — an
	// admin would rather see /Applications/Cursor.app than
	// ~/.cursor/ide_state.json.
	//
	// Entries must be things ONLY the client produces. Hand-authorable
	// config does not qualify, and neither does anything hook-install
	// creates (~/.claude/settings.json, the VS Code user settings file):
	// those would make the scanner report its own footprint as an
	// installed client. Use * only for version segments, never as a bare
	// wildcard over a client's own directory.
	Installed []string

	// Config are globs for the client's configuration directories. They
	// report ConfigPath and nothing else: a user can create one, another
	// client can read one, and hook-install creates ~/.claude itself.
	Config []string
}

// hostRoot is prepended to absolute Installed globs. Empty in
// production; tests point it at a temp directory so detection doesn't
// depend on the developer's real /Applications tree.
var hostRoot string

// clients returns the client table with every path already resolved for
// the platform. Platform is a parameter rather than a field because
// several clients keep their state in different places per OS.
func clients(platform string) []Client {
	return []Client{
		{
			// Antigravity 2.0 renamed the app bundle, the Windows install
			// directory and the dot-directory from "Antigravity" to
			// "Antigravity IDE"; both generations are checked. Despite the
			// name, ~/.gemini/antigravity is Antigravity's own state;
			// ~/.gemini/config is its configuration home (see
			// antigravity.go) and so is reported ahead of the
			// dot-directories, which hold only argv.json and extensions.
			Name: "antigravity",
			Installed: []string{
				".gemini/antigravity/installation_id",
				".gemini/antigravity/brain",
				"/Applications/Antigravity IDE.app",
				"/Applications/Antigravity.app",
				"Applications/Antigravity IDE.app",
				"Applications/Antigravity.app",
				`C:\Program Files\Google\Antigravity`,
				"AppData/Local/Programs/Antigravity IDE",
				"AppData/Local/Programs/Antigravity",
				"AppData/Local/agy/bin",
			},
			Config: []string{".gemini/config", ".antigravity-ide", ".antigravity"},
		},
		{
			Name: "claude_code",
			Installed: []string{
				".local/bin/claude",
				".local/bin/claude.exe",
				".claude.json",
				".claude/history.jsonl",
				".claude/projects",
				".claude/ide",
				".claude/shell-snapshots",
			},
			Config: []string{".claude"},
		},
		{
			Name:      "claude_desktop",
			Installed: claudeDesktopInstalled(platform),
			Config:    claudeDesktopDirs(platform),
		},
		{
			Name: "codex",
			Installed: []string{
				".codex/installation_id",
				".codex/history.jsonl",
				".codex/auth.json",
				".codex/sessions",
				".codex/archived_sessions",
			},
			Config: []string{".codex"},
		},
		{
			Name: "cursor",
			Installed: []string{
				"/Applications/Cursor.app",
				"Applications/Cursor.app",
				`C:\Program Files\cursor`,
				"AppData/Local/Programs/cursor",
				cursorAppDataDir(platform),
				".cursor/ide_state.json",
				".cursor/cli-config.json",
			},
			Config: []string{".cursor"},
		},
		{
			Name:      "goose",
			Installed: []string{gooseDataDir(platform)},
			Config:    []string{gooseConfigDir(platform)},
		},
		{
			Name: "hermes",
			Installed: []string{
				".hermes/auth.json",
				".hermes/logs",
				".hermes/bin",
			},
			Config: []string{".hermes"},
		},
		{
			Name: "openclaw",
			Installed: []string{
				path.Join(openclawConfigDir(), "identity"),
				path.Join(openclawConfigDir(), "logs"),
				path.Join(openclawConfigDir(), "update-check.json"),
			},
			Config: []string{openclawConfigDir()},
		},
		{
			Name: "opencode",
			Installed: []string{
				".local/share/opencode/opencode.db",
				".local/share/opencode/auth.json",
				".local/share/opencode/log",
				".local/share/opencode/bin",
			},
			Config: []string{".config/opencode"},
		},
		{
			Name: "vscode",
			Installed: []string{
				"/Applications/Visual Studio Code.app",
				"Applications/Visual Studio Code.app",
				`C:\Program Files\Microsoft VS Code`,
				"AppData/Local/Programs/Microsoft VS Code",
				path.Join(vscodeUserDir(platform), "globalStorage"),
				path.Join(path.Dir(vscodeUserDir(platform)), "logs"),
			},
			Config: []string{".vscode", path.Dir(vscodeUserDir(platform))},
		},
		{
			Name: "zed",
			Installed: []string{
				"/Applications/Zed.app",
				"Applications/Zed.app",
				`C:\Program Files\Zed`,
				"AppData/Local/Programs/Zed",
				zedDataDir(platform),
			},
			Config: []string{path.Dir(zedSettingsRel(platform))},
		},
	}
}

// detectClients records a row for every client this root shows evidence
// of. Clients with only a config directory are skipped: that directory
// is not proof the client is here. build still gives one a row if it
// owns an MCP server or a plugin.
func detectClients(s *state) {
	for _, c := range clients(s.platform) {
		installed := firstMatch(s, c.Installed)
		if installed == "" {
			continue
		}
		s.addClient(types.DeviceScanClient{
			Name: c.Name,
			// InstallPath is where the client was found, which is an
			// install location or a directory it writes by running. The
			// wire doc still describes only the former; obot widens it in
			// the same change that drops BinaryPath.
			InstallPath: installed,
			ConfigPath:  firstMatch(s, c.Config),
		})
	}
}

// firstMatch returns the absolute path of the first glob that matches,
// or "". A literal path is a valid pattern that matches itself when it
// exists, so callers don't distinguish literals from patterns.
//
// Relative patterns match inside the root and so hold for every root;
// absolute ones describe the host and are only meaningful in the root
// the scanner process runs in.
func firstMatch(s *state, patterns []string) string {
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		if isAbs(pattern) {
			if !s.primary {
				continue
			}
			if m, err := filepath.Glob(filepath.Join(hostRoot, pattern)); err == nil && len(m) > 0 {
				return m[0]
			}
			continue
		}
		if m, err := fs.Glob(s.fsys, pattern); err == nil && len(m) > 0 {
			return s.abs(m[0])
		}
	}
	return ""
}

// isAbs reports whether a glob describes a host path rather than one
// inside a scanned root. Windows entries are written with drive letters
// and backslashes, which filepath.IsAbs only recognizes when the scanner
// itself runs on Windows, so check for a drive prefix directly.
func isAbs(pattern string) bool {
	if filepath.IsAbs(pattern) || (len(pattern) > 0 && pattern[0] == '/') {
		return true
	}
	return len(pattern) > 2 && pattern[1] == ':' && (pattern[2] == '\\' || pattern[2] == '/')
}

// claudeDesktopInstalled lists the app bundle and Windows install
// trees, then the runtime state the app writes on first run: its
// Electron user data and the agent-mode session tree.
func claudeDesktopInstalled(platform string) []string {
	out := []string{
		"/Applications/Claude.app",
		"Applications/Claude.app",
		"AppData/Local/AnthropicClaude",
		"AppData/Local/Packages/" + claudeDesktopMSIXPackage,
	}
	for _, sub := range []string{"Local Storage", "local-agent-mode-sessions"} {
		for _, dir := range claudeDesktopDirs(platform) {
			out = append(out, path.Join(dir, sub))
		}
	}
	return out
}

// cursorAppDataDir is Cursor's Electron user-data directory, written on
// first run.
func cursorAppDataDir(platform string) string {
	switch platform {
	case "darwin":
		return "Library/Application Support/Cursor"
	case "windows":
		return "AppData/Roaming/Cursor"
	default:
		return ".config/Cursor"
	}
}

// gooseDataDir is Goose's state directory, distinct from its config
// directory on Unix: https://block.github.io/goose/docs/guides/config-file
func gooseDataDir(platform string) string {
	if platform == "windows" {
		return "AppData/Roaming/Block/goose/data"
	}
	return ".local/share/goose"
}

// zedDataDir is Zed's user-data directory (database, extensions, logs):
// https://zed.dev/docs/configuring-zed
func zedDataDir(platform string) string {
	switch platform {
	case "darwin":
		return "Library/Application Support/Zed"
	case "windows":
		return "AppData/Local/Zed"
	default:
		return ".local/share/zed"
	}
}

// openclawConfigDir honors OPENCLAW_PROFILE, which suffixes the
// directory name.
func openclawConfigDir() string {
	if profile := os.Getenv("OPENCLAW_PROFILE"); profile != "" {
		return ".openclaw-" + profile
	}
	return ".openclaw"
}
