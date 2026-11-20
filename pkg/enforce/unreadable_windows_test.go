//go:build windows

package enforce

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// makeUnreadable denies every access to dir until the test ends. On Windows a
// mode of 0 only sets the read-only attribute, which leaves a directory
// listable, so this replaces the directory's access list with an empty,
// protected one instead: it grants nobody anything and inherits nothing. The
// owner keeps the right to change the access list, which is what puts the
// original back for cleanup.
func makeUnreadable(t *testing.T, dir string) {
	t.Helper()
	original, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("reading the access list of %s: %v", dir, err)
	}
	originalDACL, _, err := original.DACL()
	if err != nil {
		t.Fatalf("reading the access list of %s: %v", dir, err)
	}
	// "D:P" is a protected access list with no entries. An empty list is not the
	// same as no list: a missing (NULL) access list grants everyone everything.
	denyAll, err := windows.SecurityDescriptorFromString("D:P")
	if err != nil {
		t.Fatal(err)
	}
	empty, _, err := denyAll.DACL()
	if err != nil || empty == nil {
		t.Fatalf("building an empty access list: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, empty, nil); err != nil {
		t.Fatalf("denying access to %s: %v", dir, err)
	}
	t.Cleanup(func() {
		info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
		if control, _, err := original.Control(); err == nil && control&windows.SE_DACL_PROTECTED != 0 {
			info = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
		}
		if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, info, nil, nil, originalDACL, nil); err != nil {
			t.Errorf("restoring the access list of %s: %v", dir, err)
		}
	})

	// A process holding an enabled SeBackupPrivilege lists any directory
	// whatever its access list says, the Windows counterpart of running as root.
	// An elevated shell can hand one down: MSYS2 and Cygwin (Git Bash included)
	// enable it for administrators, and child processes inherit it.
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("this process reads directories regardless of their access list (SeBackupPrivilege enabled)")
	}
}
