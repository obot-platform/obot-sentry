package hookinstall

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

type TargetUser struct {
	Username string
	HomeDir  string
	UID      int
	GID      int
	// SID is the account's Windows security identifier; empty elsewhere.
	SID string
}

// withUser returns list with u added, unless a user with the same home is
// already in it. Homes are compared case-insensitively, since both platforms'
// default file systems are.
func withUser(list UserList, u *TargetUser) UserList {
	for _, have := range list.Users {
		if strings.EqualFold(have.HomeDir, u.HomeDir) {
			return list
		}
	}
	list.Users = append(list.Users, u)
	return list
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

// localAccount is one entry from the platform's account listing, before it is
// vetted as a user whose files are converged.
type localAccount struct {
	Name string
	UID  string
}

// parseDsclUIDs parses `dscl . -list /Users UniqueID`, one "name uid" pair per
// line, keeping the accounts at or above minUID. Everything below is reserved
// for system accounts (_www, daemon), which never run a coding agent, and so is
// nobody, whatever its uid.
func parseDsclUIDs(out []byte, minUID int) []localAccount {
	var accounts []localAccount
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		uid, err := strconv.ParseInt(fields[len(fields)-1], 10, 64)
		if err != nil {
			continue
		}
		// dscl prints a uid above 2^31 as negative: Active Directory mobile
		// accounts get such uids from their GUIDs. Read it back as the
		// unsigned 32-bit value it is. nobody (-2) becomes 4294967294 and is
		// excluded by name below.
		if uid < 0 && uid >= -(1<<31) {
			uid += 1 << 32
		}
		name := strings.Join(fields[:len(fields)-1], " ")
		if uid < int64(minUID) || uid > 1<<32-1 || name == "nobody" {
			continue
		}
		accounts = append(accounts, localAccount{Name: name, UID: strconv.FormatInt(uid, 10)})
	}
	return accounts
}

// isUserProfileSID reports whether a ProfileList subkey names a person's
// profile: a local or Active Directory account (S-1-5-21-…) or a Microsoft Entra
// ID account (S-1-12-1-…). The built-in service accounts, virtual service
// accounts, and the ".bak" copies Windows leaves behind after repairing a
// corrupt profile are not.
func isUserProfileSID(name string) bool {
	if strings.HasSuffix(strings.ToLower(name), ".bak") {
		return false
	}
	return strings.HasPrefix(name, "S-1-5-21-") || strings.HasPrefix(name, "S-1-12-1-")
}

// sortUsers orders users by name, then home, so summaries and the schedule's
// record are stable from run to run.
func sortUsers(users []*TargetUser) {
	slices.SortFunc(users, func(a, b *TargetUser) int {
		if c := strings.Compare(a.Username, b.Username); c != 0 {
			return c
		}
		return strings.Compare(a.HomeDir, b.HomeDir)
	})
}

// dedupeUsers drops every user whose home an earlier one already has.
func dedupeUsers(users []*TargetUser) []*TargetUser {
	var list UserList
	for _, u := range users {
		list = withUser(list, u)
	}
	return list.Users
}

// isTemporaryProfile reports whether home is a Windows temporary profile for
// account (DOMAIN\name, or a SID string when the name could not be looked up).
// Windows names a temporary profile TEMP, or TEMP.<DOMAIN>[.NNN] when TEMP is
// taken; a folder that merely looks like that but is named after its own
// account — an account called temp.lab, say — is that account's real profile.
func isTemporaryProfile(home, account string) bool {
	base := home
	if i := strings.LastIndexAny(home, `\/`); i >= 0 {
		base = home[i+1:]
	}
	upper := strings.ToUpper(base)
	if upper != "TEMP" && !strings.HasPrefix(upper, "TEMP.") {
		return false
	}
	name := account
	if i := strings.LastIndex(account, `\`); i >= 0 {
		name = account[i+1:]
	}
	return !strings.EqualFold(base, name) && !strings.HasPrefix(strings.ToUpper(base), strings.ToUpper(name)+".")
}
