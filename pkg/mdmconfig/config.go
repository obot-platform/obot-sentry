// Package mdmconfig reads the deployment configuration an MDM pushes
// to the machine: the obot server URL and enrollment key. Sources are
// per-OS (Windows registry, macOS managed preferences); CLI flags and
// env vars take precedence and are layered on by the command layer via
// Merge.
package mdmconfig

import (
	"strconv"
	"strings"
	"time"
)

// Canonical key names, shared verbatim by registry value names and
// plist keys; the MDM-delivered configuration uses them exactly.
const (
	KeyServerURL           = "ServerURL"
	KeyEnrollmentKey       = "EnrollmentKey"
	KeyScanIntervalMinutes = "ScanIntervalMinutes"
	KeyHookIntervalMinutes = "HookIntervalMinutes"
	KeyEnforcementEnabled  = "EnforcementEnabled"
)

// ScanInterval bounds, mirrored by the fields schema in
// build/manifest.json (obot's admin form enforces the same range).
const (
	defaultScanIntervalMinutes = 60
	minScanIntervalMinutes     = 15
	maxScanIntervalMinutes     = 1440
)

// HookInterval bounds, mirrored by the fields schema in build/manifest.json.
// The minimum is the scheduler's poll period; a shorter interval could not be
// honored.
const (
	defaultHookIntervalMinutes = 15
	minHookIntervalMinutes     = 5
	maxHookIntervalMinutes     = 1440
)

// Config is the resolved deployment configuration.
type Config struct {
	// ServerURL is the obot server base URL (with or without /api).
	ServerURL string
	// EnrollmentKey is the ode1-... enrollment credential. Only needed
	// until the device is enrolled.
	EnrollmentKey string
	// ScanIntervalMinutes is the minimum number of minutes between
	// submitted scans; 0 means unset. The OS scheduler polls faster and
	// obot-sentry throttles to this at runtime, so admins change the cadence
	// by updating the MDM configuration alone.
	ScanIntervalMinutes int
	// HookIntervalMinutes is the minimum number of minutes between scheduled
	// hook convergences; 0 means unset.
	HookIntervalMinutes int
	// EnforcementEnabled turns the pre-tool enforcement hooks on. It is a
	// tri-state: nil means the key was absent, so a lower-precedence layer may
	// still supply it. Merge cannot use a bare bool because it fills only
	// zero-valued fields, and false is a meaningful value here — it is how an
	// administrator turns enforcement back off.
	EnforcementEnabled *bool
}

func (c Config) Enforcement() bool {
	return c.EnforcementEnabled != nil && *c.EnforcementEnabled
}

// ParseBool interprets the boolean spellings the configuration layers can
// produce, reporting whether the value was recognized at all. An unrecognized
// value is treated as absent rather than as false, so a typo falls through to
// the next layer instead of silently disabling a security control.
//
// Every store spells booleans differently and all of them have to work: a macOS
// profile carries a real plist boolean, `defaults write -bool true` reads back
// as 1, a Windows REG_DWORD reads back as 0 or 1, and a hand-written REG_SZ or
// environment variable says true.
func ParseBool(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

// ScanInterval returns the effective time between submitted scans: the
// configured minutes clamped to the schema bounds, defaulting when
// unset.
func (c Config) ScanInterval() time.Duration {
	return clampMinutes(c.ScanIntervalMinutes, defaultScanIntervalMinutes, minScanIntervalMinutes, maxScanIntervalMinutes)
}

func (c Config) HookInterval() time.Duration {
	return clampMinutes(c.HookIntervalMinutes, defaultHookIntervalMinutes, minHookIntervalMinutes, maxHookIntervalMinutes)
}

func clampMinutes(minutes, def, lo, hi int) time.Duration {
	switch {
	case minutes == 0:
		minutes = def
	case minutes < lo:
		minutes = lo
	case minutes > hi:
		minutes = hi
	}
	return time.Duration(minutes) * time.Minute
}

// Source is one place MDM-pushed values can come from. Read returns
// string values keyed by the canonical key names; missing stores return
// an empty map, not an error.
type Source interface {
	Read() (map[string]string, error)
}

// Load reads the platform MDM store (registry on Windows, managed
// preferences on macOS, nothing elsewhere).
func Load() (Config, error) {
	return FromSource(platformSource())
}

// FromSource resolves a Config from a single source.
func FromSource(src Source) (Config, error) {
	values, err := src.Read()
	if err != nil {
		return Config{}, err
	}
	get := func(key string) string {
		return strings.TrimSpace(values[key])
	}
	cfg := Config{
		ServerURL:           get(KeyServerURL),
		EnrollmentKey:       get(KeyEnrollmentKey),
		ScanIntervalMinutes: parseMinutes(get(KeyScanIntervalMinutes)),
		HookIntervalMinutes: parseMinutes(get(KeyHookIntervalMinutes)),
	}
	if enabled, ok := ParseBool(get(KeyEnforcementEnabled)); ok {
		cfg.EnforcementEnabled = &enabled
	}
	return cfg, nil
}

// parseMinutes reads an absent or non-numeric value as 0 (unset), so a bad MDM
// value falls back to the default rather than failing every run.
func parseMinutes(value string) int {
	minutes, err := strconv.Atoi(value)
	if err != nil || minutes < 0 {
		return 0
	}
	return minutes
}

// Merge returns c with any empty field filled from fallback. Callers
// layer flag/env values (c) over MDM values (fallback).
func (c Config) Merge(fallback Config) Config {
	if c.ServerURL == "" {
		c.ServerURL = fallback.ServerURL
	}
	if c.EnrollmentKey == "" {
		c.EnrollmentKey = fallback.EnrollmentKey
	}
	if c.ScanIntervalMinutes == 0 {
		c.ScanIntervalMinutes = fallback.ScanIntervalMinutes
	}
	if c.HookIntervalMinutes == 0 {
		c.HookIntervalMinutes = fallback.HookIntervalMinutes
	}
	// Absent, not false: a higher layer that says false keeps it.
	if c.EnforcementEnabled == nil {
		c.EnforcementEnabled = fallback.EnforcementEnabled
	}
	return c
}

// mapSource adapts a plain map (tests, empty platforms).
type mapSource map[string]string

func (m mapSource) Read() (map[string]string, error) { return m, nil }
