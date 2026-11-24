//go:build !windows

package hookinstall

import (
	"fmt"
	"os"
	"syscall"
)

// darwinRunDirParent holds the run directory. It is root-owned and 0755: the
// package creates it, and /Library/Application Support admits only root. The
// machine data directory beneath it (obot/obot-sentry) is deliberately NOT
// used: the package makes it mode 1777 so any user can create the shared
// device identity, so any user could also plant or race the run directory
// there.
const darwinRunDirParent = "/Library/Application Support/obot"

// runDirLocation is the run directory and its parent.
func runDirLocation() (parent, dir string, err error) {
	return darwinRunDirParent, darwinRunDirParent + "/obot-sentry-hook-install", nil
}

// makeRunDir creates the run directory private to its creator (root).
func makeRunDir(dir string) error { return os.Mkdir(dir, 0o700) }

// secureRunDir verifies the parent is a root-owned directory no one else can
// write, then makes the run directory root-owned and private to root. The run
// directory is opened without following a symlink and its ownership and mode
// are changed through that descriptor, so nothing swapped in at the path can
// redirect the change. Ownership checks apply only when running as root, which
// every real hook-install run is; tests run unprivileged.
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

// checkRunFileOwner refuses a run-directory file that a privileged run did not
// create. An unprivileged run (tests) has no such guarantee to check.
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

// claimRunFile is a no-op outside Windows: files root creates are root's.
func claimRunFile(string) {}
