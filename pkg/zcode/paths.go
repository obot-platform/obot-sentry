// Package zcode contains the filesystem contract shared by ZCode scanning and
// enforcement. ZCode 3.14+ uses a user config under ~/.zcode/cli and a
// workspace config under <project>/.zcode; the .agents/mcp.json files are
// compatibility fallbacks.
package zcode

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	UserConfigRel    = ".zcode/cli/config.json"
	ProjectConfigRel = ".zcode/config.json"
	ProjectAliasRel  = "zcode.json"
	UserCompatRel    = ".agents/mcp.json"
	ProjectCompatRel = ".agents/mcp.json"
)

// Config describes the ZCode paths for one user. ZCODE_DATA_BASE_DIR is the
// runtime's absolute data-base override; the product default remains ~/.zcode.
type Config struct {
	Home        string
	DataBaseDir string
}

func NewConfig(home string, getenv func(string) string) Config {
	if getenv == nil {
		getenv = os.Getenv
	}
	return Config{
		Home:        home,
		DataBaseDir: strings.TrimSpace(getenv("ZCODE_DATA_BASE_DIR")),
	}
}

func (c Config) Dir() string {
	if c.DataBaseDir != "" && filepath.IsAbs(c.DataBaseDir) {
		return filepath.Clean(c.DataBaseDir)
	}
	return filepath.Join(c.Home, ".zcode")
}

func (c Config) UserConfigPath() string {
	return filepath.Join(c.Dir(), "cli", "config.json")
}

func (c Config) ProjectConfigPath(project string) string {
	return filepath.Join(project, ".zcode", "config.json")
}

func (c Config) UserCompatPath() string {
	return filepath.Join(c.Home, UserCompatRel)
}

func (c Config) ProjectCompatPath(project string) string {
	return filepath.Join(project, ProjectCompatRel)
}
