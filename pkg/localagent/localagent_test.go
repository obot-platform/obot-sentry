package localagent

import "testing"

func TestZCodeIsExternalPluginAgent(t *testing.T) {
	if got := ZCode.DisplayName(); got != "ZCode" {
		t.Fatalf("ZCode display name = %q, want ZCode", got)
	}
	for _, agent := range All() {
		if agent == ZCode {
			t.Fatal("ZCode must not be included in native hook-install agents")
		}
	}
}

func TestOpenCodeIsExternalPluginAgent(t *testing.T) {
	if got := OpenCode.DisplayName(); got != "OpenCode" {
		t.Fatalf("OpenCode display name = %q, want OpenCode", got)
	}
	for _, agent := range All() {
		if agent == OpenCode {
			t.Fatal("OpenCode must not be included in native hook-install agents")
		}
	}
}
