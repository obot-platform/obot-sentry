//go:build !windows

package hookinstall

import (
	"fmt"
	"os"
	"syscall"
)

// darwinRunDirParent is root-owned 0755 (postinstall creates it). The machine
// data directory beneath it, obot/obot-sentry, is mode 1777 and must not hold
// the run directory.
const darwinRunDirParent = "/Library/Application Support/obot"

func runDirLocation() (parent, dir string, err error) {
	return darwinRunDirParent, darwinRunDirParent + "/obot-sentry-hook-install", nil
}

func makeRunDir(dir string) error { return os.Mkdir(dir, 0o700) }

// secureRunDir changes ownership and mode through a descriptor opened without
// following symlinks, so nothing swapped in at the path can redirect it.
// Ownership checks apply only when running as root; tests run unprivileged.
func secureRunDir(parent, dir string) error {
	pinfo, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !pinfo.IsDir() {
		return fmt.Errorf("refusing run directory parent %q: not a directory", parent)
	}
	privileged := os.Geteuid() == 0
	if privileged {
		st, ok := pinfo.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || pinfo.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("refusing run directory parent %q: not a root-owned directory only root can write", parent)
		}
	}

	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		// O_NOFOLLOW|O_DIRECTORY reports a symlink as ENOTDIR (or ELOOP), so
		// name what is actually there rather than echo that error.
		if li, lerr := os.Lstat(dir); lerr == nil {
			if li.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing run directory %q: it is a symbolic link", dir)
			}
			if !li.IsDir() {
				return fmt.Errorf("refusing run directory %q: not a directory", dir)
			}
		}
		return fmt.Errorf("opening run directory %q: %w", dir, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("refusing run directory %q: not a directory", dir)
	}
	if privileged {
		if err := f.Chown(0, 0); err != nil {
			return err
		}
	}
	return f.Chmod(0o700)
}

// checkRunFileOwner has nothing to check in an unprivileged run (tests).
func checkRunFileOwner(path string, info os.FileInfo) error {
	if os.Geteuid() != 0 {
		return nil
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return fmt.Errorf("cannot read ownership of %q", path)
	}
	if st.Uid != 0 {
		return fmt.Errorf("refusing %q owned by uid %d, not root", path, st.Uid)
	}
	return nil
}

func claimRunFile(string) {}
