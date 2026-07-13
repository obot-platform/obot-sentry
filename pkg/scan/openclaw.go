package scan

import "os"

// openclawScanner is presence-only: OpenClaw has no public config or
// plugin format we scan today. Its config directory varies with
// $OPENCLAW_PROFILE, so it owns that resolution rather than leaking a
// special case into shared presence code.
type openclawScanner struct{}

func (openclawScanner) Name() string { return "openclaw" }

func (openclawScanner) Presence(string) presenceDef {
	configPath := ".openclaw"
	if profile := os.Getenv("OPENCLAW_PROFILE"); profile != "" {
		configPath = ".openclaw-" + profile
	}
	return presenceDef{
		binaries:    []string{"openclaw"},
		appBundles:  []string{"OpenClaw.app"},
		configPaths: []string{configPath},
	}
}

func (openclawScanner) GlobalConfigs(string) []string { return nil }

func (openclawScanner) ProjectConfigs() []string { return nil }

func (openclawScanner) ScanHome(*state) observations { return observations{} }

func (openclawScanner) ScanProject(*state, string) observations { return observations{} }
