package zcode

import (
	"path/filepath"
	"testing"
)

func TestConfigPaths(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "user")
	config := NewConfig(home, func(string) string { return "" })
	if got := config.UserConfigPath(); got != filepath.Join(home, ".zcode", "cli", "config.json") {
		t.Fatalf("user config path = %q", got)
	}
	if got := config.ProjectConfigPath(filepath.Join(home, "project")); got != filepath.Join(home, "project", ".zcode", "config.json") {
		t.Fatalf("project config path = %q", got)
	}
}

func TestAbsoluteDataBaseDirOverride(t *testing.T) {
	override, err := filepath.Abs(filepath.Join("managed", "zcode"))
	if err != nil {
		t.Fatal(err)
	}
	config := NewConfig(filepath.Join(string(filepath.Separator), "home", "user"), func(name string) string {
		if name == "ZCODE_DATA_BASE_DIR" {
			return override
		}
		return ""
	})
	if got := config.UserConfigPath(); got != filepath.Join(override, "cli", "config.json") {
		t.Fatalf("override config path = %q", got)
	}
}
