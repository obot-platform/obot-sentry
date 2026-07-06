// Package mdmconfig reads the deployment configuration an MDM pushes
// to the machine: the obot server URL and enrollment key, plus optional
// overrides. Sources are per-OS (Windows registry, macOS managed
// preferences); CLI flags and env vars take precedence and are layered
// on by the command layer via Merge.
package mdmconfig

import (
	"strings"
)

// Canonical key names, shared verbatim by registry value names and
// plist keys. packaging/CONTRACT.md is the compatibility contract for
// these — the obot UI's generated deployment config writes them.
const (
	KeyServerURL     = "ServerURL"
	KeyEnrollmentKey = "EnrollmentKey"
	KeyUsername      = "Username"
	KeyDeviceName    = "DeviceName"
)

// Config is the resolved deployment configuration.
type Config struct {
	// ServerURL is the obot server base URL (with or without /api).
	ServerURL string
	// EnrollmentKey is the ode1-... enrollment credential. Only needed
	// until the device is enrolled.
	EnrollmentKey string
	// Username optionally overrides the scan manifest's username
	// attribution (e.g. to a corporate email).
	Username string
	// DeviceName optionally overrides the reported hostname.
	DeviceName string
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
	return Config{
		ServerURL:     get(KeyServerURL),
		EnrollmentKey: get(KeyEnrollmentKey),
		Username:      get(KeyUsername),
		DeviceName:    get(KeyDeviceName),
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
	if c.Username == "" {
		c.Username = fallback.Username
	}
	if c.DeviceName == "" {
		c.DeviceName = fallback.DeviceName
	}
	return c
}

// mapSource adapts a plain map (tests, empty platforms).
type mapSource map[string]string

func (m mapSource) Read() (map[string]string, error) { return m, nil }
