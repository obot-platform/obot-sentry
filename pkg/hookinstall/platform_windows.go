//go:build windows

package hookinstall

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// profileListKey has one subkey per user profile SID, signed in or not.
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

// resolveTargetUsers decides on SIDs only, since Windows localizes built-in
// account names, and takes each home from ProfileImagePath rather than
// assuming C:\Users\<name>.
func resolveTargetUsers() (UserSet, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, profileListKey, registry.ENUMERATE_SUB_KEYS|registry.WOW64_64KEY)
	if err != nil {
		return UserSet{}, fmt.Errorf("opening %s: %w", profileListKey, err)
	}
	names, err := k.ReadSubKeyNames(-1)
	_ = k.Close()
	if err != nil {
		return UserSet{}, fmt.Errorf("listing %s: %w", profileListKey, err)
	}
	sort.Strings(names)

	var cands []userCandidate
	for _, name := range names {
		if !isUserProfileSID(name) {
			continue
		}
		cands = append(cands, userCandidate{Name: name, resolve: func() (*TargetUser, error) {
			return profileUser(name)
		}})
	}
	set := resolveCandidates(cands, userResolveTimeout)
	nameAccounts(set.Users, accountNameTimeout)
	return set, nil
}

// accountNameTimeout bounds the display-name lookups. LookupAccountSid can
// block on the network for a domain account when no domain controller is
// reachable; the name is cosmetic, so it must not cost the user their hooks.
const accountNameTimeout = 5 * time.Second

// nameAccounts replaces each user's SID-string Username with the account name,
// keeping the SID for any lookup that has not finished by the deadline.
func nameAccounts(users []*TargetUser, timeout time.Duration) {
	type named struct {
		i    int
		name string
	}
	done := make(chan named, len(users))
	pending := 0
	for i, u := range users {
		sid, err := windows.StringToSid(u.Username)
		if err != nil {
			continue
		}
		pending++
		go func() {
			defer func() { _ = recover() }()
			done <- named{i, accountName(sid)}
		}()
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for range pending {
		select {
		case n := <-done:
			users[n.i].Username = n.name
		case <-deadline.C:
			return
		}
	}
}

// profileUser opens its own registry key, so a lookup abandoned past the
// deadline never uses a handle the caller has closed.
func profileUser(sidString string) (*TargetUser, error) {
	sid, err := windows.StringToSid(sidString)
	if err != nil {
		return nil, fmt.Errorf("parsing profile SID: %w", err)
	}
	sub, err := registry.OpenKey(registry.LOCAL_MACHINE, profileListKey+`\`+sidString, registry.QUERY_VALUE|registry.WOW64_64KEY)
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
	// Username is the SID string until nameAccounts resolves it, outside the
	// deadline that decides whether the user is converged.
	return &TargetUser{Username: sid.String(), HomeDir: home}, nil
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
