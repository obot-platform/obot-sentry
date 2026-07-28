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

func TestFromSource_EnforcementEnabled(t *testing.T) {
	for _, tt := range []struct {
		name    string
		value   string
		present bool
		want    bool
	}{
		{"plist bool / REG_DWORD 1", "1", true, true},
		{"REG_DWORD 0", "0", true, false},
		{"REG_SZ true", "true", true, true},
		{"REG_SZ false", "false", true, false},
		{"mixed case", "TRUE", true, true},
		{"yes", "yes", true, true},
		{"no", "no", true, false},
		{"on", "on", true, true},
		{"off", "off", true, false},
		{"padded", "  true  ", true, true},
		{"junk reads as absent", "sometimes", false, false},
		{"empty reads as absent", "", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := FromSource(mapSource{KeyEnforcementEnabled: tt.value})
			if err != nil {
				t.Fatal(err)
			}
			if tt.present {
				if cfg.EnforcementEnabled == nil {
					t.Fatalf("EnforcementEnabled = nil, want %v", tt.want)
				}
				if *cfg.EnforcementEnabled != tt.want {
					t.Errorf("EnforcementEnabled = %v, want %v", *cfg.EnforcementEnabled, tt.want)
				}
			} else if cfg.EnforcementEnabled != nil {
				t.Errorf("EnforcementEnabled = %v, want absent", *cfg.EnforcementEnabled)
			}
			if got := cfg.Enforcement(); got != tt.want {
				t.Errorf("Enforcement() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMerge_EnforcementEnabled(t *testing.T) {
	ptr := func(b bool) *bool { return &b }

	for _, tt := range []struct {
		name     string
		high     *bool
		fallback *bool
		want     *bool
	}{
		{"flag off beats MDM on", ptr(false), ptr(true), ptr(false)},
		{"flag on beats MDM off", ptr(true), ptr(false), ptr(true)},
		{"absent takes the MDM value", nil, ptr(true), ptr(true)},
		{"absent takes MDM off", nil, ptr(false), ptr(false)},
		{"absent everywhere stays absent", nil, nil, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Config{EnforcementEnabled: tt.high}.Merge(Config{EnforcementEnabled: tt.fallback})
			switch {
			case tt.want == nil && got.EnforcementEnabled != nil:
				t.Fatalf("EnforcementEnabled = %v, want absent", *got.EnforcementEnabled)
			case tt.want != nil && got.EnforcementEnabled == nil:
				t.Fatalf("EnforcementEnabled = nil, want %v", *tt.want)
			case tt.want != nil && *got.EnforcementEnabled != *tt.want:
				t.Errorf("EnforcementEnabled = %v, want %v", *got.EnforcementEnabled, *tt.want)
			}
		})
	}
}
