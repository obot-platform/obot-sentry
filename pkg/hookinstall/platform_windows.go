//go:build windows

package hookinstall

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// profileListKey is where Windows records every user profile on the machine,
// one subkey per account SID, whether or not that account is signed in.
const profileListKey = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList`

// checkPrivilege accepts either an elevated Administrator token or the SYSTEM
// account. Under UAC, membership in the Administrators group without an elevated
// token is not sufficient to write machine policy, so a filtered (non-elevated)
// admin token is rejected.
func checkPrivilege() error {
	token := windows.GetCurrentProcessToken()
	if isSystemToken(token) {
		return nil
	}
	if token.IsElevated() {
		return nil
	}
	return fmt.Errorf("obot-sentry hook commands must run from an elevated Administrator or SYSTEM token on Windows")
}

// isSystemToken reports whether token's user is the local SYSTEM account.
func isSystemToken(token windows.Token) bool {
	user, err := token.GetTokenUser()
	if err != nil {
		return false
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false
	}
	return user.User.Sid.Equals(systemSID)
}

func validateExecutableOwner(path string, _ os.FileInfo) error {
	// Resolve symlinks and reparse points throughout the path before querying
	// security information, so ownership is checked on the final target.
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolving obot-sentry executable %q: %w", path, err)
	}
	owner, err := fileOwner(target)
	if err != nil {
		return fmt.Errorf("reading obot-sentry executable %q owner: %w", target, err)
	}
	if !trustedExecutableOwner(owner) {
		return fmt.Errorf("obot-sentry executable %q is not owned by SYSTEM or Administrators", target)
	}
	return nil
}

// fileOwner returns the owner SID of path.
func fileOwner(path string) (*windows.SID, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	if sd == nil {
		return nil, fmt.Errorf("%q has no security descriptor", path)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return nil, err
	}
	return owner, nil
}

func trustedExecutableOwner(owner *windows.SID) bool {
	return owner != nil && (owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid))
}

// resolveTargetUsers lists every real user profile on the machine from the
// registry ProfileList, signed in or not, so drift in a signed-out user's files
// is repaired too. Every decision is made on the profile's SID; the account
// name is only rendered for output, because Windows localizes built-in names.
// The profile directory comes from ProfileImagePath rather than an assumed
// C:\Users\<name>.
func resolveTargetUsers() (UserSet, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, profileListKey, registry.ENUMERATE_SUB_KEYS|registry.WOW64_64KEY)
	if err != nil {
		return UserSet{}, fmt.Errorf("opening %s: %w", profileListKey, err)
	}
	defer func() { _ = k.Close() }()
	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return UserSet{}, fmt.Errorf("listing %s: %w", profileListKey, err)
	}
	sort.Strings(names)

	var set UserSet
	for _, name := range names {
		if !isUserProfileSID(name) {
			continue
		}
		u, err := profileUser(k, name)
		if errors.Is(err, fs.ErrNotExist) {
			// A profile whose directory is gone (a deleted account) has nothing
			// to converge; it is not worth a warning on every run.
			continue
		}
		if err != nil {
			set.Skipped = append(set.Skipped, SkippedUser{Name: name, Err: err})
			continue
		}
		set.Users = append(set.Users, u)
	}
	return set, nil
}

// profileUser resolves one ProfileList entry to a validated TargetUser.
func profileUser(profileList registry.Key, sidString string) (*TargetUser, error) {
	sid, err := windows.StringToSid(sidString)
	if err != nil {
		return nil, fmt.Errorf("parsing profile SID: %w", err)
	}
	sub, err := registry.OpenKey(profileList, sidString, registry.QUERY_VALUE)
	if err != nil {
		return nil, fmt.Errorf("opening profile key: %w", err)
	}
	defer func() { _ = sub.Close() }()
	raw, _, err := sub.GetStringValue("ProfileImagePath")
	if err != nil {
		return nil, fmt.Errorf("reading ProfileImagePath: %w", err)
	}
	home, err := registry.ExpandString(raw)
	if err != nil {
		return nil, fmt.Errorf("expanding ProfileImagePath %q: %w", raw, err)
	}
	if err := validateHomeDir(home); err != nil {
		return nil, err
	}
	return &TargetUser{Username: accountName(sid), HomeDir: home}, nil
}

// accountName renders sid for operator-facing output. The name is never used to
// decide anything, so a lookup that cannot reach the account database — a
// domain account on a machine off the corporate network, or a deleted account
// whose profile remains — degrades to the SID string.
func accountName(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	switch {
	case err != nil:
		return sid.String()
	case domain == "":
		return account
	default:
		return domain + `\` + account
	}
}
