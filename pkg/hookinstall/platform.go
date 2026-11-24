package hookinstall

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type TargetUser struct {
	Username string
	HomeDir  string
	UID      int
	GID      int
}

// UserSet is every local account whose per-user files a run converges, plus
// the accounts that look like real users but were passed over, with the
// reason. A skipped account is reported, not fatal: one profile on an
// unmounted volume must not hold back every other user on the machine.
type UserSet struct {
	Users   []*TargetUser
	Skipped []SkippedUser
}

// SkippedUser is an account that was enumerated but not converged.
type SkippedUser struct {
	Name string
	Err  error
}

func validateHomeDir(home string) error {
	if home == "" {
		return fmt.Errorf("resolved user has no home directory")
	}
	if err := validateInstallHomePath(home); err != nil {
		return err
	}
	info, err := os.Lstat(home)
	if err != nil {
		return fmt.Errorf("resolved home %q is not accessible: %w", home, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("resolved home %q is not a directory", home)
	}
	return nil
}

// minDarwinUserUID is the lowest uid macOS assigns to a real local account.
// Apple reserves everything below 500 for system use.
const minDarwinUserUID = 500

// dsclAccount is one row of `dscl . -list /Users UniqueID`.
type dsclAccount struct {
	Name string
	UID  string
}

// parseDsclUserList extracts the real-user rows from `dscl . -list /Users
// UniqueID` output. System accounts are dropped here: by uid range, and by the
// leading underscore macOS gives every daemon account. It lives in a portable
// file so the parse is tested on every platform.
func parseDsclUserList(out []byte) []dsclAccount {
	var accounts []dsclAccount
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		name, uidStr := fields[0], fields[1]
		if strings.HasPrefix(name, "_") {
			continue
		}
		uid, err := strconv.Atoi(uidStr)
		if err != nil || uid < minDarwinUserUID {
			continue
		}
		accounts = append(accounts, dsclAccount{Name: name, UID: uidStr})
	}
	return accounts
}

// isUserProfileSID reports whether a Windows ProfileList subkey names a real
// user's profile: a local or Active Directory account (S-1-5-21-…) or a
// Microsoft Entra ID account (S-1-12-1-…). The built-in service accounts
// (S-1-5-18/19/20) and the ".bak" keys Windows leaves behind when it falls back
// to a temporary profile are excluded.
func isUserProfileSID(name string) bool {
	if strings.HasSuffix(strings.ToLower(name), ".bak") {
		return false
	}
	return strings.HasPrefix(name, "S-1-5-21-") || strings.HasPrefix(name, "S-1-12-1-")
}
