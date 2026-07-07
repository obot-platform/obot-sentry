// Package datadir resolves the directories obocop persists its state
// under: a machine-scoped one for the shared device identity, a
// per-user data one for enrollment state, and a per-user cache one for
// scan state and scan log records.
package datadir

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/adrg/xdg"
	"github.com/obot-platform/obocop/pkg/fileutil"
)

// CacheDir returns the per-user obocop cache directory (scan state and
// scan log records). The directory is not created by this function.
func CacheDir() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "obot", "obocop"), nil
}

// Dir returns the per-user obocop data directory, creating it with
// owner-only permissions or an equivalent platform-private ACL if needed:
//
//	windows: %LOCALAPPDATA%\obot\obocop
//	darwin:  ~/Library/Application Support/obot/obocop
//	linux:   ${XDG_DATA_HOME:-~/.local/share}/obot/obocop
func Dir() (string, error) {
	dir := filepath.Join(xdg.DataHome, "obot", "obocop")
	if err := fileutil.MkdirAllPrivate(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// MachineDir returns the machine-scoped data directory shared by every
// user, so all users present one device identity:
//
//	windows: %PROGRAMDATA%\obot\obocop
//	darwin:  /Library/Application Support/obot/obocop
//	linux:   /var/lib/obot/obocop
//
// The identity key stored here must be readable by all users (scans
// run per user), so the directory is created 0755. Creation can fail
// for unprivileged processes on darwin/linux — callers fall back to
// the per-user Dir (see identity.Load); on Windows, ProgramData is
// user-creatable by default and the MSI can pre-create the directory.
func MachineDir() (string, error) {
	var base string
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("ProgramData")
	case "darwin":
		base = "/Library/Application Support"
	default:
		base = "/var/lib"
	}
	if base == "" {
		return "", os.ErrNotExist
	}
	dir := filepath.Join(base, "obot", "obocop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// IdentityDir returns the directory the device identity key lives in:
// the machine-scoped dir when usable (the normal MDM case), otherwise
// the per-user dir — each user then appears as its own device, which
// keeps dev machines and unprovisioned macOS hosts working.
func IdentityDir() (string, error) {
	if dir, err := MachineDir(); err == nil {
		return dir, nil
	}
	return Dir()
}
