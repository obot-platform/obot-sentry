//go:build windows

package hookinstall

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// openNonblock is unneeded: NTFS has no FIFOs.
const openNonblock = 0

// checkOpenedConfig refuses hard links, so a user cannot get SYSTEM to read a
// file they cannot and write its contents back into one they own.
func checkOpenedConfig(scope Scope, u *TargetUser, _ os.FileInfo, absPath string, f *os.File) error {
	if scope != ScopeUser || u == nil {
		return nil
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return fmt.Errorf("reading file information for %q: %w", absPath, err)
	}
	if info.NumberOfLinks != 1 {
		return fmt.Errorf("refusing user config %q with %d hard links", absPath, info.NumberOfLinks)
	}
	return nil
}

// fchownToUser and chownCreatedDir are no-ops: new files and directories
// inherit the profile's DACL, which grants the user full control.
func fchownToUser(*os.File, *TargetUser) error { return nil }

func chownCreatedDir(*os.Root, string, *TargetUser) error { return nil }

// secureMachineConfigDir protects the directory that controls deletion and
// replacement of a machine hook. The inheritable entries also ensure that the
// temporary file begins life with the restricted ACL before it is renamed.
func secureMachineConfigDir(absPath string) error {
	dir := filepath.Dir(absPath)
	return applyMachineConfigACL(dir, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE)
}

// secureMachineConfig gives normal users read/execute access to the installed
// configuration but reserves mutation and ACL control for SYSTEM and the local
// Administrators group. The protected DACL deliberately discards broader
// entries inherited from ProgramData.
func secureMachineConfig(absPath string) error {
	return applyMachineConfigACL(absPath, 0)
}

func applyMachineConfigACL(path string, inheritance uint32) error {
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("resolving SYSTEM SID: %w", err)
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("resolving Administrators SID: %w", err)
	}
	usersSID, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		return fmt.Errorf("resolving Users SID: %w", err)
	}

	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		machineConfigAccess(systemSID, windows.TRUSTEE_IS_USER, windows.GENERIC_ALL, inheritance),
		machineConfigAccess(adminSID, windows.TRUSTEE_IS_GROUP, windows.GENERIC_ALL, inheritance),
		machineConfigAccess(usersSID, windows.TRUSTEE_IS_GROUP, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_EXECUTE, inheritance),
	}, nil)
	if err != nil {
		return fmt.Errorf("building protected ACL for %q: %w", path, err)
	}

	// An elevated administrator can assign the Administrators group as owner;
	// SYSTEM retains itself as owner. Keeping ownership within these two
	// principals prevents an unprivileged account from rewriting the DACL via
	// the owner's implicit WRITE_DAC right.
	ownerSID := adminSID
	if isSystemToken(windows.GetCurrentProcessToken()) {
		ownerSID = systemSID
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		ownerSID,
		nil,
		acl,
		nil,
	); err != nil {
		return fmt.Errorf("securing machine config %q: %w", path, err)
	}
	return nil
}

func machineConfigAccess(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE, permissions windows.ACCESS_MASK, inheritance uint32) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: permissions,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}
