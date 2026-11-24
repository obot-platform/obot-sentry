//go:build windows

package hookinstall

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// trustedInstallerSID owns the protected parts of Program Files.
const trustedInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

// runDirLocation is the run directory and its parent: the package's install
// directory under Program Files, which no unprivileged user can create
// entries in.
func runDirLocation() (parent, dir string, err error) {
	parent = filepath.Dir(packagedWindowsExecutable)
	return parent, filepath.Join(parent, "hook-install"), nil
}

// makeRunDir creates the run directory with its protected ACL in place from
// the start. os.Mkdir would create it with the ACL inherited from Program
// Files, which lets Users list it; a user who opened a handle to it in that
// window, before secureRunDir tightens the ACL, would keep that handle — and
// could hold the directory open to block the run log and state.
func makeRunDir(dir string) error {
	// The owner is left unset: Windows assigns the creator's default owner
	// (SYSTEM, or Administrators for an elevated administrator), and
	// secureRunDir then sets it explicitly.
	acl, _, err := runDirACL(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	if err != nil {
		return err
	}
	sd, err := windows.NewSecurityDescriptor()
	if err != nil {
		return err
	}
	if err := sd.SetDACL(acl, true, false); err != nil {
		return err
	}
	if err := sd.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	if err := windows.CreateDirectory(p, &sa); err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return fs.ErrExist
		}
		return err
	}
	return nil
}

// secureRunDir verifies the run directory and its parent and restricts the run
// directory to SYSTEM and Administrators. Everything is done through handles
// opened without following reparse points, so a junction cannot redirect the
// ACL change, and each directory must be owned by a trusted account — a
// directory a user created and still holds a handle to would keep whatever
// access that handle was opened with, whatever ACL is applied afterward.
func secureRunDir(parent, dir string) error {
	ph, err := openDirNoReparse(parent, windows.READ_CONTROL)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(ph) }()
	if err := checkTrustedOwner(ph, parent); err != nil {
		return err
	}

	h, err := openDirNoReparse(dir, windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := checkTrustedOwner(h, dir); err != nil {
		return err
	}
	acl, owner, err := runDirACL(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, acl, nil)
}

// openDirNoReparse opens path as a directory without following a reparse point
// in its final component, and refuses anything that is not a plain directory.
func openDirNoReparse(path string, access uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(p, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, fmt.Errorf("opening %q: %w", path, err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		_ = windows.CloseHandle(h)
		return 0, fmt.Errorf("reading %q: %w", path, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		_ = windows.CloseHandle(h)
		return 0, fmt.Errorf("refusing %q: not a plain directory", path)
	}
	return h, nil
}

// checkTrustedOwner requires the object behind h to be owned by SYSTEM,
// Administrators, TrustedInstaller, or the account this run executes as.
func checkTrustedOwner(h windows.Handle, path string) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("reading owner of %q: %w", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("reading owner of %q: %w", path, err)
	}
	if !trustedRunOwner(owner) {
		return fmt.Errorf("refusing %q: owned by %s, not SYSTEM, Administrators, or TrustedInstaller", path, accountName(owner))
	}
	return nil
}

func trustedRunOwner(owner *windows.SID) bool {
	if trustedExecutableOwner(owner) {
		return true
	}
	if ti, err := windows.StringToSid(trustedInstallerSID); err == nil && owner.Equals(ti) {
		return true
	}
	if self, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil && owner.Equals(self.User.Sid) {
		return true
	}
	return false
}

// runDirACL is full control for SYSTEM and Administrators and nothing for
// anyone else, with the owner set to SYSTEM (when running as SYSTEM) or
// Administrators.
func runDirACL(inheritance uint32) (*windows.ACL, *windows.SID, error) {
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, nil, err
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, nil, err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		machineConfigAccess(systemSID, windows.TRUSTEE_IS_USER, windows.GENERIC_ALL, inheritance),
		machineConfigAccess(adminSID, windows.TRUSTEE_IS_GROUP, windows.GENERIC_ALL, inheritance),
	}, nil)
	if err != nil {
		return nil, nil, err
	}
	owner := adminSID
	if isSystemToken(windows.GetCurrentProcessToken()) {
		owner = systemSID
	}
	return acl, owner, nil
}

// checkRunFileOwner refuses a run-directory file that neither SYSTEM, the
// Administrators group, nor the account this run executes as created. The run
// directory admits no one else, so this is defense in depth. An unprivileged
// run (tests) has no such guarantee to check.
func checkRunFileOwner(path string, _ os.FileInfo) error {
	token := windows.GetCurrentProcessToken()
	if !isSystemToken(token) && !token.IsElevated() {
		return nil
	}
	owner, err := fileOwner(path)
	if err != nil {
		return fmt.Errorf("reading owner of %q: %w", path, err)
	}
	if !trustedRunOwner(owner) {
		return fmt.Errorf("refusing %q: not owned by SYSTEM, Administrators, or the running account", path)
	}
	return nil
}

// claimRunFile gives a run-directory file the run directory's ACL with SYSTEM
// or Administrators as its owner, so a later run under a different
// administrative account still trusts it. It is best-effort: an unprivileged
// run (tests) cannot change the owner, and the file already inherits the
// directory's restrictive ACL.
func claimRunFile(path string) {
	acl, owner, err := runDirACL(0)
	if err != nil {
		return
	}
	_ = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, acl, nil)
}
