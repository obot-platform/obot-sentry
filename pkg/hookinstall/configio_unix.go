//go:build !windows

package hookinstall

import (
	"fmt"
	"os"
	"syscall"
)

// openNonblock keeps a FIFO swapped in for a hook file from hanging the open;
// the descriptor is then rejected as non-regular.
const openNonblock = syscall.O_NONBLOCK

func secureMachineConfig(string) error { return nil }

func secureMachineConfigDir(string) error { return nil }

// checkOpenedConfig refuses user files a user could use to make root read, and
// rewrite as theirs, a file they cannot read: a hard link to a root-only file,
// or a root-owned file (from a sudo'd tool, say) renamed into a hook path.
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

func fchownToUser(f *os.File, u *TargetUser) error {
	return f.Chown(u.UID, u.GID)
}

// chownCreatedDir opens with O_NOFOLLOW|O_DIRECTORY so that if the user swapped
// a link in for the new directory, the link's target is not handed to them.
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
