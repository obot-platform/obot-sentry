package mdmconfig

import (
	"testing"
	"time"
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

// TestFromSource_ScanInterval pins the lenient parse: numbers parse,
// anything else (absent, junk, negative) reads as unset — a bad MDM
// value must not brick every scan on the machine.
func TestFromSource_ScanInterval(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value string
		want  int
	}{
		{"valid", "30", 30},
		{"padded", " 45 ", 45},
		{"absent", "", 0},
		{"junk", "soon", 0},
		{"negative", "-5", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := mapSource{}
			if tt.value != "" {
				src[KeyScanIntervalMinutes] = tt.value
			}
			cfg, err := FromSource(src)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ScanIntervalMinutes != tt.want {
				t.Errorf("ScanIntervalMinutes = %d, want %d", cfg.ScanIntervalMinutes, tt.want)
			}
		})
	}
}

// TestMerge pins the precedence contract: the receiver (flags/env)
// wins per field; the fallback (MDM store) only fills gaps.
func TestMerge(t *testing.T) {
	flags := Config{ServerURL: "https://flag.example.com"}
	mdm := Config{
		ServerURL:           "https://mdm.example.com",
		EnrollmentKey:       "ode1-1-2-secret",
		ScanIntervalMinutes: 30,
	}
	got := flags.Merge(mdm)
	if got.ServerURL != "https://flag.example.com" {
		t.Errorf("flag ServerURL should win, got %q", got.ServerURL)
	}
	if got.EnrollmentKey != "ode1-1-2-secret" {
		t.Errorf("MDM EnrollmentKey should fill the gap, got %q", got.EnrollmentKey)
	}
	if got.ScanIntervalMinutes != 30 {
		t.Errorf("MDM ScanIntervalMinutes should fill the gap, got %d", got.ScanIntervalMinutes)
	}

	flags.ScanIntervalMinutes = 120
	if got := flags.Merge(mdm); got.ScanIntervalMinutes != 120 {
		t.Errorf("flag ScanIntervalMinutes should win, got %d", got.ScanIntervalMinutes)
	}
}

// TestScanInterval pins the default and the clamp to the schema bounds
// (build/manifest.json fields.scanIntervalMinutes).
func TestScanInterval(t *testing.T) {
	for _, tt := range []struct {
		minutes int
		want    time.Duration
	}{
		{0, 60 * time.Minute},        // unset -> default
		{45, 45 * time.Minute},       // in range
		{5, 15 * time.Minute},        // below floor -> clamped up
		{100000, 1440 * time.Minute}, // above ceiling -> clamped down
	} {
		if got := (Config{ScanIntervalMinutes: tt.minutes}).ScanInterval(); got != tt.want {
			t.Errorf("ScanInterval(%d) = %s, want %s", tt.minutes, got, tt.want)
		}
	}
}
