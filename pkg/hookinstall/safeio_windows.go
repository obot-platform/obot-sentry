//go:build windows

package hookinstall

import "os"

// applyOwnership is a no-op on Windows. Newly created files and directories
// inherit their DACL from the parent: per-user files under the console user's
// profile inherit an ACL that already grants that user (plus SYSTEM and
// Administrators, which created them) full control, and machine files under
// %ProgramData% inherit the ProgramData ACL — SYSTEM and Administrators full
// control, authenticated users read/execute, no normal-user write. Relying on
// inheritance avoids resolving the account SID and stamping an explicit DACL on
// every file. The anchored, symlink-safe creation in safeio.go still applies.
func applyOwnership(_ *os.Root, _ string, _ []string, _ Scope, _ *TargetUser) error {
	return nil
}
