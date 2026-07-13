package scan

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// DefaultRoots returns the scan roots for this machine: the current
// user's home directory, plus (on Windows) the home directories of
// every installed WSL distribution. WSL homes are separate Linux
// environments — a skill installed under the Windows profile is not
// visible inside WSL and vice versa — so they are scanned as
// additional roots with the Linux config layout, reached through the
// \\wsl.localhost\ (or legacy \\wsl$\) share. WSL enumeration failures
// never abort the primary scan.
func DefaultRoots(ctx context.Context) ([]Root, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	roots := []Root{{
		FS:       os.DirFS(home),
		Path:     home,
		Platform: runtime.GOOS,
		Primary:  true,
	}}
	return append(roots, wslRoots(ctx)...), nil
}

// wslUtilityDistros are infrastructure distributions (container
// runtimes) that hold no user configuration worth scanning.
var wslUtilityDistros = map[string]bool{
	"docker-desktop":         true,
	"docker-desktop-data":    true,
	"rancher-desktop":        true,
	"rancher-desktop-data":   true,
	"podman-machine-default": true,
}

// parseWSLDistros extracts distribution names from `wsl --list --quiet`
// output (one name per line), dropping blanks and utility distros.
func parseWSLDistros(output string) []string {
	var distros []string
	for line := range strings.SplitSeq(output, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || wslUtilityDistros[strings.ToLower(name)] {
			continue
		}
		distros = append(distros, name)
	}
	return distros
}
