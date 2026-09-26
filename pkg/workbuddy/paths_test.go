package workbuddy

import (
	"path/filepath"
	"testing"
)

func TestConfigDirPrefersWorkBuddyVariable(t *testing.T) {
	home := t.TempDir()
	workbuddyDir := filepath.Join(home, "custom-workbuddy")
	codebuddyDir := filepath.Join(home, "custom-codebuddy")
	config := NewConfig(home, func(key string) string {
		switch key {
		case "WORKBUDDY_CONFIG_DIR":
			return workbuddyDir
		case "CODEBUDDY_CONFIG_DIR":
			return codebuddyDir
		default:
			return ""
		}
	})
	if got := config.Dir(); got != workbuddyDir {
		t.Fatalf("Dir() = %q, want %q", got, workbuddyDir)
	}
	if got := config.SettingsPath(); got != filepath.Join(workbuddyDir, "settings.json") {
		t.Fatalf("SettingsPath() = %q", got)
	}
}

func TestConfigDirIgnoresRelativeEnvironmentValues(t *testing.T) {
	home := t.TempDir()
	config := NewConfig(home, func(key string) string {
		if key == "WORKBUDDY_CONFIG_DIR" {
			return "relative-workbuddy"
		}
		return ""
	})
	want := filepath.Join(home, DefaultConfigDirRel)
	if got := config.Dir(); got != want {
		t.Fatalf("Dir() = %q, want default %q", got, want)
	}
}

func TestUserMCPPathsKeepsFirstExistingOrder(t *testing.T) {
	home := t.TempDir()
	config := NewConfig(home, func(string) string { return "" })
	got := config.UserMCPPaths()
	want := []string{
		filepath.Join(home, DefaultConfigDirRel, ".mcp.json"),
		filepath.Join(home, DefaultConfigDirRel, "mcp.json"),
		filepath.Join(home, ".codebuddy.json"),
		filepath.Join(home, ".workbuddy.json"),
	}
	if len(got) != len(want) {
		t.Fatalf("UserMCPPaths() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("UserMCPPaths()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
