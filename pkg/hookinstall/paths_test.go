package hookinstall

import (
	"path/filepath"
	"testing"
)

func TestResolvePathUserAndMachine(t *testing.T) {
	u := &TargetUser{HomeDir: filepath.FromSlash("/home/alice")}

	userDest := Destination{Scope: ScopeUser, Rel: ".claude/settings.json", Label: "Claude Code"}
	got, err := userDest.ResolvePath(u)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/alice", filepath.FromSlash(".claude/settings.json")); got != want {
		t.Fatalf("user path = %q, want %q", got, want)
	}

	machineDest := Destination{Scope: ScopeMachine, Abs: "/etc/codex/requirements.toml", Label: "Codex"}
	got, err = machineDest.ResolvePath(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/etc/codex/requirements.toml" {
		t.Fatalf("machine path = %q", got)
	}

	if _, err := userDest.ResolvePath(nil); err == nil {
		t.Fatal("expected a user-scoped resolve without a target user to fail")
	}
}

func TestRelWithinRejectsEscape(t *testing.T) {
	base := filepath.FromSlash("/home/alice")
	if _, err := relWithin(base, filepath.Join(base, "sub", "file")); err != nil {
		t.Fatalf("a contained path should resolve: %v", err)
	}
	if _, err := relWithin(base, filepath.FromSlash("/etc/passwd")); err == nil {
		t.Fatal("expected an escaping absolute path to be rejected")
	}
	if _, err := relWithin(base, filepath.Join(base, "..", "bob", "file")); err == nil {
		t.Fatal("expected a parent-escaping path to be rejected")
	}
}

func TestAnchorForMachineUsesVolumeRoot(t *testing.T) {
	anchorDir, rel, err := anchorFor(ScopeMachine, "", filepath.FromSlash("/etc/codex/requirements.toml"))
	if err != nil {
		t.Fatal(err)
	}
	wantAnchor := filepath.VolumeName(filepath.FromSlash("/etc/codex/requirements.toml")) + string(filepath.Separator)
	if anchorDir != wantAnchor {
		t.Fatalf("machine anchor = %q, want %q", anchorDir, wantAnchor)
	}
	if filepath.Join(anchorDir, rel) != filepath.Clean(filepath.FromSlash("/etc/codex/requirements.toml")) {
		t.Fatalf("anchor+rel = %q", filepath.Join(anchorDir, rel))
	}
}
