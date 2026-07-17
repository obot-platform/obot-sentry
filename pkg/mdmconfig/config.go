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
)

// ScanInterval bounds, mirrored by the fields schema in
// build/manifest.json (obot's admin form enforces the same range).
const (
	defaultScanIntervalMinutes = 60
	minScanIntervalMinutes     = 15
	maxScanIntervalMinutes     = 1440
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
}

// ScanInterval returns the effective time between submitted scans: the
// configured minutes clamped to the schema bounds, defaulting when
// unset.
func (c Config) ScanInterval() time.Duration {
	minutes := c.ScanIntervalMinutes
	switch {
	case minutes == 0:
		minutes = defaultScanIntervalMinutes
	case minutes < minScanIntervalMinutes:
		minutes = minScanIntervalMinutes
	case minutes > maxScanIntervalMinutes:
		minutes = maxScanIntervalMinutes
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
	// Lenient: an absent or non-numeric interval reads as 0 (unset) and
	// the default applies, rather than failing every scan on the machine.
	interval, err := strconv.Atoi(get(KeyScanIntervalMinutes))
	if err != nil || interval < 0 {
		interval = 0
	}
	return Config{
		ServerURL:           get(KeyServerURL),
		EnrollmentKey:       get(KeyEnrollmentKey),
		ScanIntervalMinutes: interval,
	}, nil
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
	return c
}

// mapSource adapts a plain map (tests, empty platforms).
type mapSource map[string]string

func (m mapSource) Read() (map[string]string, error) { return m, nil }
