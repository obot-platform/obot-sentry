//go:build !windows

package hookinstall

import (
	"fmt"
	"os"
	"syscall"
)

// openNonblock keeps opening a FIFO from blocking: a user who swaps a named
// pipe in for a hook file between the path check and the open must not be able
// to hang the privileged run. The descriptor is then rejected as non-regular.
const openNonblock = syscall.O_NONBLOCK

func secureMachineConfig(string) error { return nil }

func secureMachineConfigDir(string) error { return nil }

// checkOpenedConfig refuses a user-scoped file that a user could have used to
// make root read (and then rewrite, owned by that user) a file they cannot
// read: one with another hard link — a link to a root-only file — or one owned
// by anyone other than the user. Root-owned files are refused too: a user can
// rename a root-owned, root-only file left in their home (by a sudo'd tool,
// say) into a hook path. Machine-scoped files live in root-owned directories
// and need no such check.
func checkOpenedConfig(scope Scope, u *TargetUser, info os.FileInfo, absPath string, _ *os.File) error {
	if scope != ScopeUser || u == nil {
		return nil
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return fmt.Errorf("cannot read ownership of %q", absPath)
	}
	if st.Nlink != 1 {
		return fmt.Errorf("refusing user config %q with %d hard links", absPath, st.Nlink)
	}
	if int(st.Uid) != u.UID {
		return fmt.Errorf("refusing user config %q owned by uid %d, not %s", absPath, st.Uid, u.Username)
	}
	return nil
}

// fchownToUser gives a file being written for u to u, through its descriptor.
func fchownToUser(f *os.File, u *TargetUser) error {
	return f.Chown(u.UID, u.GID)
}

// chownCreatedDir gives a directory just created under the user's home to the
// user through a descriptor. The open refuses a symlink (O_NOFOLLOW) and
// anything that is not a directory (O_DIRECTORY), so if the user renamed the
// new directory away and planted a link to some other file in its place, that
// file is not touched.
func chownCreatedDir(root *os.Root, d string, u *TargetUser) error {
	f, err := root.OpenFile(d, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("refusing %q: not a directory", d)
	}
	return f.Chown(u.UID, u.GID)
}
