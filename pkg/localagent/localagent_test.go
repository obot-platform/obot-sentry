package localagent

import "testing"

// OpenCode is enforced by an external plugin rather than by a managed native
// hook, so it must stay out of All(): hook installation has no OpenCode
// destination to write, and a missing destination would be reported as a
// failure on every install.
func TestOpenCodeIsExcludedFromManagedAgents(t *testing.T) {
	for _, agent := range All() {
		if agent == OpenCode {
			t.Fatal("OpenCode must not appear in All()")
		}
	}

	if got := OpenCode.DisplayName(); got != "OpenCode" {
		t.Fatalf("OpenCode.DisplayName() = %q", got)
	}
}
