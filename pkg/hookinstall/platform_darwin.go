//go:build darwin

package hookinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// minDarwinUserUID is the lowest uid macOS assigns to a real local account.
// Apple reserves everything below 500 for system use.
const minDarwinUserUID = 500

// checkPrivilege requires an effective UID of 0 on macOS. Machine policy and
// per-user files under other accounts' homes both require root; anything less
// is rejected before the first config write.
func checkPrivilege() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("obot-sentry hook commands must run as root on macOS; rerun with sudo")
	}
	return nil
}

// resolveTargetUsers lists every local account with a set-up home on macOS,
// plus the account at the console. The console account is added because a
// network account (Active Directory without a mobile account, or LDAP) is not in
// the local directory dscl lists, yet can be signed in and running agents. If
// the directory service cannot be read, the console account alone is returned
// with the error, so the run converges what it can and still reports the
// failure.
func resolveTargetUsers() (UserList, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/dscl", ".", "-list", "/Users", "UniqueID").Output()
	var list UserList
	var listErr error
	if err == nil {
		list = vetDarwinAccounts(parseDsclUIDs(out, minDarwinUserUID), user.LookupId)
	} else {
		listErr = fmt.Errorf("listing local accounts with dscl: %w", err)
	}
	console, consoleErr := targetUserFromConsole()
	switch {
	case consoleErr == nil:
		if reason := darwinHomeUnusable(console); reason != "" {
			if !hasHome(list, console.HomeDir) {
				list.Skipped = append(list.Skipped, SkippedUser{Username: console.Username, Reason: reason})
			}
		} else {
			list = withUser(list, console)
		}
	case listErr != nil:
		return UserList{}, errors.Join(listErr, consoleErr)
	}
	return list, listErr
}

// vetDarwinAccounts turns listed accounts into the users to converge, passing
// over any whose home is missing (a network account whose share is not mounted),
// not yet set up by a first sign-in, or owned by a different account.
func vetDarwinAccounts(accounts []localAccount, lookup func(uid string) (*user.User, error)) UserList {
	var list UserList
	seen := map[string]bool{}
	for _, a := range accounts {
		u, err := lookup(a.UID)
		if err != nil {
			list.Skipped = append(list.Skipped, SkippedUser{Username: a.Name, Reason: err.Error()})
			continue
		}
		tu, err := targetUserFromAccount(u)
		if err != nil {
			list.Skipped = append(list.Skipped, SkippedUser{Username: a.Name, Reason: err.Error()})
			continue
		}
		if seen[tu.HomeDir] {
			continue
		}
		if reason := darwinHomeUnusable(tu); reason != "" {
			list.Skipped = append(list.Skipped, SkippedUser{Username: tu.Username, Reason: reason})
			continue
		}
		seen[tu.HomeDir] = true
		list.Users = append(list.Users, tu)
	}
	return list
}

// darwinHomeUnusable says why hooks should not be written into u's home, or ""
// when they can be. macOS fills a new home from the user template at first
// sign-in; writing into it first could race that, so a home without its Library
// folder is left until it has one. A home owned by another account is left
// alone, since the files written there would be handed to an account that does
// not control the directory.
func darwinHomeUnusable(u *TargetUser) string {
	info, err := os.Lstat(u.HomeDir)
	if err != nil {
		return fmt.Sprintf("home %s is not accessible: %v", u.HomeDir, err)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != u.UID {
		return fmt.Sprintf("home %s is not owned by the account", u.HomeDir)
	}
	lib, err := os.Lstat(filepath.Join(u.HomeDir, "Library"))
	if err != nil || !lib.IsDir() {
		return fmt.Sprintf("home %s has not been set up by a first sign-in", u.HomeDir)
	}
	return ""
}

// targetUserFromConsole resolves the owner of /dev/console — the user logged in
// at the graphical session. A root-owned console means no user is logged in.
func targetUserFromConsole() (*TargetUser, error) {
	info, err := os.Stat("/dev/console")
	if err != nil {
		return nil, fmt.Errorf("no active console user: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("cannot read /dev/console ownership")
	}
	if st.Uid == 0 {
		return nil, fmt.Errorf("no active console user (no one is logged in); rerun after a user logs in")
	}
	u, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10))
	if err != nil {
		return nil, fmt.Errorf("looking up console user (uid %d): %w", st.Uid, err)
	}
	return targetUserFromAccount(u)
}

// targetUserFromAccount validates an account-database entry and builds a
// TargetUser. It rejects system accounts by uid range and requires a real home
// directory taken from the account database, never the environment.
func targetUserFromAccount(u *user.User) (*TargetUser, error) {
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return nil, fmt.Errorf("user %q has non-numeric uid %q", u.Username, u.Uid)
	}
	if uid < minDarwinUserUID {
		return nil, fmt.Errorf("resolved user %q (uid %d) is a system account, not a real console user", u.Username, uid)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nil, fmt.Errorf("user %q has non-numeric gid %q", u.Username, u.Gid)
	}
	if err := validateHomeDir(u.HomeDir); err != nil {
		return nil, err
	}
	return &TargetUser{Username: u.Username, HomeDir: u.HomeDir, UID: uid, GID: gid}, nil
}

func validateExecutableOwner(path string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return fmt.Errorf("cannot read obot-sentry executable %q ownership", path)
	}
	if st.Uid != 0 {
		return fmt.Errorf("obot-sentry executable %q is owned by uid %d, not root", path, st.Uid)
	}
	return nil
}

// hasHome reports whether list already has a user whose home is home.
func hasHome(list UserList, home string) bool {
	for _, u := range list.Users {
		if strings.EqualFold(u.HomeDir, home) {
			return true
		}
	}
	return false
}
