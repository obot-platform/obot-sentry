//go:build !windows

package hookinstall

import "os"

// applyOwnership re-owns newly created per-user files and directories to the
// target console user. Machine files stay root-owned (their world-readable
// modes were set at creation), so ownership is only changed for user-scoped
// paths, and only for the components this run created — never a preexisting
// parent. Lchown operates on the anchored descriptor without following a
// symlink.
func applyOwnership(root *os.Root, rel string, createdDirs []string, scope Scope, u *TargetUser) error {
	if scope != ScopeUser || u == nil {
		return nil
	}
	for _, dir := range createdDirs {
		if err := root.Lchown(dir, u.UID, u.GID); err != nil {
			return err
		}
	}
	return root.Lchown(rel, u.UID, u.GID)
}
