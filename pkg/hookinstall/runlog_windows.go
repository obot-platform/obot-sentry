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

// runDirLocation is under the install directory in Program Files, where no
// unprivileged user can create entries.
func runDirLocation() (parent, dir string, err error) {
	parent = filepath.Dir(packagedWindowsExecutable)
	return parent, filepath.Join(parent, "hook-install"), nil
}

// makeRunDir creates the directory with its protected ACL already applied.
// os.Mkdir would briefly give it Program Files' inherited ACL, which lets
// Users open it, and a handle opened then keeps its access after the ACL is
// tightened.
func makeRunDir(dir string) error {
	// The owner is left unset, so Windows assigns the creator's default owner;
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

// secureRunDir works through handles opened without following reparse points,
// so a junction cannot redirect the ACL change, and requires a trusted owner:
// a user who created the directory could hold a handle whose access survives
// any later ACL change.
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

// trustedRunOwner also accepts the running account, because files an elevated
// administrator creates can be owned by that administrator, not the group.
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

// checkRunFileOwner is defense in depth: the run directory admits no one else.
// An unprivileged run (tests) has nothing to check.
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

// claimRunFile makes SYSTEM or Administrators the owner, so a later run under a
// different administrative account still trusts the file. Best-effort: an
// unprivileged run (tests) cannot change the owner.
func claimRunFile(path string) {
	acl, owner, err := runDirACL(0)
	if err != nil {
		return
	}
	_ = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, acl, nil)
}
