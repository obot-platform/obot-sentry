//go:build windows

package hookinstall

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestTrustedExecutableOwner(t *testing.T) {
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	if !trustedExecutableOwner(system) || !trustedExecutableOwner(admins) {
		t.Fatal("SYSTEM or Administrators ownership was rejected")
	}
	if trustedExecutableOwner(users) || trustedExecutableOwner(nil) {
		t.Fatal("an untrusted executable owner was accepted")
	}
}

// TestResolveTargetUsersReadsProfileList reads the real ProfileList, which on
// any machine running the tests holds at least the running account's profile.
func TestResolveTargetUsersReadsProfileList(t *testing.T) {
	set, err := resolveTargetUsers()
	if err != nil {
		t.Fatalf("resolveTargetUsers: %v", err)
	}
	if len(set.Users) == 0 {
		t.Fatalf("no user profiles resolved; skipped: %+v", set.Skipped)
	}
	named := false
	for _, u := range set.Users {
		if u.Username == "" || !filepath.IsAbs(u.HomeDir) {
			t.Errorf("resolved user %+v lacks a name or an absolute home", u)
		}
		if !strings.HasPrefix(u.Username, "S-1-") {
			named = true
		}
	}
	if !named {
		t.Errorf("no profile resolved to an account name; got %+v", set.Users)
	}
}

func TestBootTimeIsInThePast(t *testing.T) {
	boot, err := bootTime()
	if err != nil {
		t.Fatal(err)
	}
	if boot.IsZero() || !boot.Before(time.Now()) {
		t.Fatalf("boot time %v is not in the past", boot)
	}
}

func TestOpenDirNoReparseRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	h, err := openDirNoReparse(dir, windows.READ_CONTROL)
	if err != nil {
		t.Fatalf("a plain directory was refused: %v", err)
	}
	_ = windows.CloseHandle(h)

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("cannot create a directory symlink here: %v", err)
	}
	if h, err := openDirNoReparse(link, windows.READ_CONTROL); err == nil {
		_ = windows.CloseHandle(h)
		t.Fatal("a directory symlink was opened as the run directory")
	}
}

func TestMakeRunDirIsBornProtected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hook-install")
	if err := makeRunDir(dir); err != nil {
		t.Fatalf("makeRunDir: %v", err)
	}
	t.Cleanup(func() {
		// Re-open the DACL to inheritance so an unprivileged test run can remove
		// the temp dir; as owner, the creator may change it.
		_ = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, nil, nil)
	})
	if err := makeRunDir(dir); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second makeRunDir err = %v, want fs.ErrExist", err)
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("run directory DACL is not protected from inheritance")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsWellKnown(windows.WinLocalSystemSid) && !sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
			t.Fatalf("run directory grants access to %s; want only SYSTEM and Administrators", sid)
		}
	}
}
