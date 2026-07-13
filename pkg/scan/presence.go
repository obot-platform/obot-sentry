package scan

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/obot-platform/obot/apiclient/types"
)

// presenceDef describes how to detect that a given AI client is
// installed. Each field is a list because most clients have one or two
// canonical names; the first match wins per category.
type presenceDef struct {
	// binaries are command names resolved against the host $PATH.
	binaries []string
	// appBundles are .app bundle names checked under the macOS
	// application directories. Ignored on other platforms.
	appBundles []string
	// installDirs are install locations checked on Windows —
	// home-relative (e.g. AppData/Local/Programs/...) or absolute
	// (Program Files). Ignored on other platforms, where binaries and
	// appBundles cover installs.
	installDirs []string
	// configPaths are root-relative directories whose existence
	// indicates an install.
	configPaths []string
}

// appBundleDirs is overridable in tests so detection doesn't depend on
// the real /Applications tree. nil → platform defaults (/Applications
// and ~/Applications on darwin).
var appBundleDirs []string

// detectPresence runs presence detection for every registered scanner
// against the primary root's host OS and adds a clients[] row whenever
// any signal fires.
func detectPresence(s *state) {
	for _, c := range scanners {
		def := c.Presence(s.platform)
		binary, install, configPath := detectClientPresence(def, s.base, s.platform)
		if binary == "" && install == "" && configPath == "" {
			continue
		}
		s.addClient(types.DeviceScanClient{
			Name:        c.Name(),
			BinaryPath:  binary,
			InstallPath: install,
			ConfigPath:  configPath,
		})
	}
}

// detectClientPresence returns the first-matching binary, install path,
// and config path for def. Empty strings mean no signal in that
// category.
func detectClientPresence(def presenceDef, home, platform string) (binary, install, configPath string) {
	for _, b := range def.binaries {
		if p, err := exec.LookPath(b); err == nil && p != "" {
			binary = p
			break
		}
	}

	switch platform {
	case "darwin":
		bundles := appBundleDirs
		if bundles == nil {
			bundles = []string{"/Applications", filepath.Join(home, "Applications")}
		}
	bundleLoop:
		for _, name := range def.appBundles {
			for _, dir := range bundles {
				candidate := filepath.Join(dir, name)
				if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
					install = candidate
					break bundleLoop
				}
			}
		}
	case "windows":
		for _, dir := range def.installDirs {
			candidate := dir
			if !filepath.IsAbs(dir) {
				candidate = filepath.Join(home, filepath.FromSlash(dir))
			}
			if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
				install = candidate
				break
			}
		}
	}

	for _, rel := range def.configPaths {
		candidate := filepath.Join(home, filepath.FromSlash(rel))
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			configPath = candidate
			break
		}
	}
	return
}
