//go:build !windows

package hookinstall

import (
	"fmt"
	"os"
	"syscall"
)

// openRunDirNow opens dir for the run log or the schedule's record, creating it
// first when create is set. It must be a real directory owned by root (or by the
// account running, which is root outside tests) and writable by no one else.
// Its parents are root-controlled system directories (/Library/Logs and
// /Library/Application Support/obot), so only an administrator could replace it.
func openRunDirNow(dir string, create bool) (*runDir, error) {
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	if err := checkRootOwned(dir, info); err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s is writable by accounts other than its owner", dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	// Confirm the handle is the directory that was checked.
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, fmt.Errorf("%s changed while it was being opened", dir)
	}
	return &runDir{path: dir, root: root, release: func() {}}, nil
}

// checkRunFile refuses an open run file with more than one link, or not owned
// by root or the running account. It works on the descriptor, so the file
// checked is the file used.
func checkRunFile(f *os.File, path string) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read the identity of %s", path)
	}
	if st.Nlink != 1 {
		return fmt.Errorf("%s has %d links", path, st.Nlink)
	}
	return checkRootOwned(path, info)
}

// checkRootOwned refuses a file or directory not owned by root or by the
// running account.
func checkRootOwned(path string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read the owner of %s", path)
	}
	if st.Uid != 0 && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s is owned by uid %d, not root", path, st.Uid)
	}
	return nil
}

// removeRunDataNow removes the run directory. Its parents are root-controlled,
// so nothing in its path can be redirected.
func removeRunDataNow(dir string) error {
	return os.RemoveAll(dir)
}
