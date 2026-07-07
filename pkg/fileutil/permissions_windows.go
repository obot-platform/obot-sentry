//go:build windows

package fileutil

import (
	"os"

	"golang.org/x/sys/windows"
)

func applyFilePermissions(path string, perm os.FileMode) error {
	// On Windows, os.Chmod does not implement Unix-style permission bits. It
	// only toggles the read-only attribute from the owner-write bit. Keep it for
	// that behavior, then apply a real Windows ACL for owner-only modes.
	if err := os.Chmod(path, perm); err != nil {
		return err
	}
	// Only translate private Unix-style modes, such as 0600, into a protected
	// ACL. Shared modes, such as the 0644 identity key, intentionally keep the
	// default inherited ACLs.
	if perm&0o077 != 0 {
		return nil
	}
	return applyPrivateACL(path, 0)
}

func applyPrivateDirPermissions(path string) error {
	// Directories need inheritable ACEs so child files and child directories do
	// not accidentally inherit broader permissions from a parent directory.
	return applyPrivateACL(path, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE)
}

func applyPrivateACL(path string, inheritance uint32) error {
	// A SID is Windows' stable identifier for a user, group, or service account.
	// File ACLs are expressed in terms of SIDs, not display names.
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	// SYSTEM and Administrators keep local management and MDM tooling from being
	// locked out of files created by an ordinary user process.
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}

	// A DACL is the part of a Windows security descriptor that grants or denies
	// access. Each EXPLICIT_ACCESS entry below becomes an allow ACE granting full
	// control to exactly one trustee: current user, SYSTEM, or Administrators.
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		allowSID(user.User.Sid, windows.TRUSTEE_IS_USER, inheritance),
		allowSID(systemSID, windows.TRUSTEE_IS_USER, inheritance),
		allowSID(adminSID, windows.TRUSTEE_IS_GROUP, inheritance),
	}, nil)
	if err != nil {
		return err
	}

	// PROTECTED_DACL_SECURITY_INFORMATION prevents broader inherited entries
	// such as Users or Everyone from being merged in from the parent directory.
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	)
}

func allowSID(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE, inheritance uint32) windows.EXPLICIT_ACCESS {
	// GENERIC_ALL is full control for the trustee. The trustee form says this
	// entry identifies the account by SID rather than by a localized name.
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}
