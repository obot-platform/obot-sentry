package scan

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// wslEnumTimeout bounds the `wsl --list` subprocess; wsl.exe can hang
// when the WSL service is wedged, and a stuck enumeration must not
// stall the whole scan.
const wslEnumTimeout = 15 * time.Second

// wslRoots discovers the home directories of installed WSL
// distributions and returns one linux-platform Root per home. Every
// failure is soft: no WSL, no distros, or an unreachable share just
// means fewer roots.
func wslRoots(ctx context.Context) []Root {
	wsl, err := exec.LookPath("wsl.exe")
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, wslEnumTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, wsl, "--list", "--quiet")
	// wsl.exe writes UTF-16LE by default; WSL_UTF8=1 forces UTF-8
	// (https://github.com/microsoft/WSL/releases/tag/0.64.0).
	cmd.Env = append(os.Environ(), "WSL_UTF8=1")
	out, err := cmd.Output()
	if err != nil {
		slog.Debug("wsl: distro enumeration failed", "err", err)
		return nil
	}

	var roots []Root
	for _, distro := range parseWSLDistros(string(out)) {
		roots = append(roots, wslDistroRoots(distro)...)
	}
	return roots
}

// wslDistroRoots returns a Root for each home directory (/home/* and
// /root) inside one distro, preferring the current \\wsl.localhost
// share over the legacy \\wsl$ form.
func wslDistroRoots(distro string) []Root {
	for _, prefix := range []string{`\\wsl.localhost\`, `\\wsl$\`} {
		share := prefix + distro

		var roots []Root
		addHome := func(dir, native string) {
			if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
				return
			}
			roots = append(roots, Root{
				FS:         os.DirFS(dir),
				Path:       dir,
				NativePath: native,
				Platform:   "linux",
			})
		}

		entries, err := os.ReadDir(filepath.Join(share, "home"))
		for _, e := range entries {
			if e.IsDir() {
				addHome(filepath.Join(share, "home", e.Name()), "/home/"+e.Name())
			}
		}
		addHome(filepath.Join(share, "root"), "/root")

		// A readable /home means the share works even if it holds no
		// homes; don't fall through and double-scan via the old name.
		if len(roots) > 0 || err == nil {
			return roots
		}
	}
	slog.Debug("wsl: distro share unreachable", "distro", distro)
	return nil
}
