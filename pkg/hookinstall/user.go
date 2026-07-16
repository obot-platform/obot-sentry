package hookinstall

import (
	"fmt"
	"os"
)

// TargetUser is the single active console user whose per-user hook files the
// installer converges. A privileged run (root on macOS, an elevated/SYSTEM
// token on Windows) writes machine policy for all users but per-user files for
// exactly this one account; there is no active user means preflight fails
// before any config write (MDM should retry at logon).
type TargetUser struct {
	// Username is the account name, used only in operator-facing output.
	Username string
	// HomeDir is the account's real home/profile directory, resolved from the
	// account database (macOS) or the session token (Windows) — never from an
	// attacker-influenced environment variable. It is the trusted anchor for the
	// symlink-safe per-user writes in safeio.go.
	HomeDir string
	// UID and GID are the POSIX numeric owner ids used to chown newly created
	// per-user files back to the console user after a root write. They are unused
	// (zero) on Windows, where newly created files inherit the profile's DACL.
	UID int
	GID int
}

// validateHomeDir rejects a resolved home that cannot anchor a safe per-user
// write: an empty path, a missing directory, or a non-directory (a symlink or
// other file planted where the home should be).
func validateHomeDir(home string) error {
	if home == "" {
		return fmt.Errorf("resolved console user has no home directory")
	}
	info, err := os.Lstat(home)
	if err != nil {
		return fmt.Errorf("resolved home %q is not accessible: %w", home, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("resolved home %q is not a directory", home)
	}
	return nil
}
