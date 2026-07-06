package mdmconfig

import (
	"testing"
)

func TestFromSource_TrimsValues(t *testing.T) {
	cfg, err := FromSource(mapSource{KeyServerURL: "  https://obot.example.com  "})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerURL != "https://obot.example.com" {
		t.Errorf("ServerURL = %q, want trimmed value", cfg.ServerURL)
	}
}

// TestMerge pins the precedence contract: the receiver (flags/env)
// wins per field; the fallback (MDM store) only fills gaps.
func TestMerge(t *testing.T) {
	flags := Config{ServerURL: "https://flag.example.com"}
	mdm := Config{
		ServerURL:     "https://mdm.example.com",
		EnrollmentKey: "ode1-1-2-secret",
	}
	got := flags.Merge(mdm)
	if got.ServerURL != "https://flag.example.com" {
		t.Errorf("flag ServerURL should win, got %q", got.ServerURL)
	}
	if got.EnrollmentKey != "ode1-1-2-secret" {
		t.Errorf("MDM EnrollmentKey should fill the gap, got %q", got.EnrollmentKey)
	}
}
