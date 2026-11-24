//go:build windows

package hookinstall

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// openRunDirNow opens dir (%ProgramData%\obot\hook-install) for the run log
// and the schedule's record, creating it first when create is set.
//
// ProgramData lets any user create directories, so dir and its parent are
// opened as themselves and held without delete sharing for as long as the
// runDir is open (see holdDirChain). dir is created with an Administrators-owned
// protected ACL that only SYSTEM and Administrators can use. One a user made
// first is taken over in place and emptied, and a junction is moved aside, so a
// user cannot keep the log and the record away; obot above it, if a user made
// it, is taken over too, so its owner cannot move dir out between runs. Every
// file operation afterwards goes through the root handle.
func openRunDirNow(dir string, create bool) (*runDir, error) {
	if err := validateInstallMachinePath(dir); err != nil {
		return nil, err
	}
	held := &heldDirs{}
	opts := chainOptions{manage: true, create: create, leafSDDL: runDirSDDL, midSDDL: sharedParentSDDL}
	reclaimed, err := holdDirChain(windowsProgramData(), dir, opts, held)
	if err != nil {
		held.release()
		return nil, err
	}
	if reclaimed {
		if err := emptyDir(dir); err != nil {
			held.release()
			return nil, fmt.Errorf("emptying %s, taken over from a user: %w", dir, err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		held.release()
		return nil, err
	}
	return &runDir{path: dir, root: root, release: held.release}, nil
}

// removeRunDataNow removes the run directory through a handle to its held
// parent, never by a path a user could redirect, along with any junctions
// earlier runs moved aside beside it.
func removeRunDataNow(dir string) error {
	parent := filepath.Dir(dir)
	held := &heldDirs{}
	defer held.release()
	if _, err := holdDirChain(windowsProgramData(), parent, chainOptions{}, held); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	names, err := f.Readdirnames(-1)
	_ = f.Close()
	if err != nil {
		return err
	}
	base := filepath.Base(dir)
	var errs []error
	for _, n := range names {
		if strings.EqualFold(n, base) || strings.HasPrefix(strings.ToLower(n), strings.ToLower(base)+".untrusted-") {
			if err := root.RemoveAll(n); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// checkRunFile refuses an open run file with more than one link, or not owned
// by SYSTEM, Administrators, or the running account. It works on the handle, so
// the file checked is the file used.
func checkRunFile(f *os.File, path string) error {
	h := windows.Handle(f.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if info.NumberOfLinks != 1 {
		return fmt.Errorf("%s has %d links", path, info.NumberOfLinks)
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("reading the owner of %s: %w", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("reading the owner of %s: %w", path, err)
	}
	if trustedExecutableOwner(owner) {
		return nil
	}
	if self, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil && owner.Equals(self.User.Sid) {
		return nil
	}
	return fmt.Errorf("%s is not owned by SYSTEM or Administrators", filepath.Base(path))
}
