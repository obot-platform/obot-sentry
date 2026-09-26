// Package workbuddy contains the filesystem contract shared by WorkBuddy
// discovery, scanning, enforcement, and hook installation. WorkBuddy's desktop
// process sets WORKBUDDY_CONFIG_DIR/CODEBUDDY_CONFIG_DIR for its sidecar; the
// default remains the product's ~/.workbuddy directory when those variables are
// absent.
package workbuddy

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultConfigDirRel     = ".workbuddy"
	DefaultUserMCPPathRel   = ".workbuddy/.mcp.json"
	DefaultUserMCPAltRel    = ".workbuddy/mcp.json"
	DefaultLegacyMCPPathRel = ".workbuddy.json"
	DefaultSettingsPathRel  = ".workbuddy/settings.json"
)

// DefaultUserMCPPathRels is ordered according to WorkBuddy's first-existing
// user MCP file rule. The legacy .workbuddy.json spelling is retained for
// older desktop data; it is not merged with a preferred file.
func DefaultUserMCPPathRels() []string {
	return []string{DefaultUserMCPPathRel, DefaultUserMCPAltRel, DefaultLegacyMCPPathRel}
}

// Config describes the paths for one user. The environment values are kept
// separate so callers can inspect or test the resolution without reaching into
// process-global state.
type Config struct {
	Home               string
	WorkBuddyConfigDir string
	CodeBuddyConfigDir string
}

// NewConfig captures WorkBuddy's optional configuration-root variables. A
// non-empty relative value is ignored deliberately: resolving it against the
// Sentry process's working directory would make a hook read a different file
// than WorkBuddy does.
func NewConfig(home string, getenv func(string) string) Config {
	if getenv == nil {
		getenv = os.Getenv
	}
	return Config{
		Home:               home,
		WorkBuddyConfigDir: strings.TrimSpace(getenv("WORKBUDDY_CONFIG_DIR")),
		CodeBuddyConfigDir: strings.TrimSpace(getenv("CODEBUDDY_CONFIG_DIR")),
	}
}

// Dir returns the active WorkBuddy configuration directory.
func (c Config) Dir() string {
	for _, candidate := range []string{c.WorkBuddyConfigDir, c.CodeBuddyConfigDir} {
		if candidate != "" && filepath.IsAbs(candidate) {
			return filepath.Clean(candidate)
		}
	}
	return filepath.Join(c.Home, DefaultConfigDirRel)
}

// UserMCPPaths returns the ordered absolute candidates WorkBuddy checks for a
// user MCP document. The CodeBuddy filename is a compatibility fallback for
// the sidecar; the .workbuddy.json spelling is retained for old desktop data.
func (c Config) UserMCPPaths() []string {
	dir := c.Dir()
	paths := []string{
		filepath.Join(dir, ".mcp.json"),
		filepath.Join(dir, "mcp.json"),
		filepath.Join(c.Home, ".codebuddy.json"),
		filepath.Join(c.Home, DefaultLegacyMCPPathRel),
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, candidate := range paths {
		if candidate == "" {
			continue
		}
		clean := filepath.Clean(candidate)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

// SettingsPath returns the active WorkBuddy settings.json path.
func (c Config) SettingsPath() string {
	return filepath.Join(c.Dir(), "settings.json")
}
