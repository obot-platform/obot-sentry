//go:build darwin

package hookinstall

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"sort"
	"strconv"
	"syscall"
	"time"
)

// checkPrivilege requires an effective UID of 0 on macOS. Machine policy and
// per-user files under other accounts' homes both require root; anything less
// is rejected before the first config write.
func checkPrivilege() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("obot-sentry hook commands must run as root on macOS; rerun with sudo")
	}
	return nil
}

// dsclTimeout keeps a wedged opendirectoryd from hanging the run; dscl
// normally answers from the local node instantly.
const dsclTimeout = 30 * time.Second

// resolveTargetUsers takes each home from the account database, never the
// environment.
func resolveTargetUsers() (UserSet, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dsclTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/dscl", ".", "-list", "/Users", "UniqueID").Output()
	if err != nil {
		return UserSet{}, fmt.Errorf("listing local accounts with dscl: %w", err)
	}
	return darwinUsers(parseDsclUserList(out), user.LookupId, homeOwnedBy), nil
}

// homeOwnedBy rejects homes the account does not own, such as /Users/Shared or
// /var/empty, which are not that user's to have root create directories in.
func homeOwnedBy(home string, uid int) error {
	info, err := os.Lstat(home)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return fmt.Errorf("cannot read ownership of home %q", home)
	}
	if int(st.Uid) != uid {
		return fmt.Errorf("home %q is owned by uid %d, not the account's uid %d", home, st.Uid, uid)
	}
	return nil
}

func darwinUsers(accounts []dsclAccount, lookup func(uid string) (*user.User, error), ownedBy func(home string, uid int) error) UserSet {
	var set UserSet
	for _, a := range accounts {
		u, err := lookup(a.UID)
		if err != nil {
			set.Skipped = append(set.Skipped, SkippedUser{Name: a.Name, Err: fmt.Errorf("looking up uid %s: %w", a.UID, err)})
			continue
		}
		tu, err := targetUserFromAccount(u)
		if errors.Is(err, fs.ErrNotExist) {
			// macOS creates a home directory at first login, so an account
			// that has never logged in has nothing to converge yet. Its first
			// login adds the home and makes the next scheduled poll due.
			continue
		}
		if err == nil {
			err = ownedBy(tu.HomeDir, tu.UID)
		}
		if err != nil {
			set.Skipped = append(set.Skipped, SkippedUser{Name: a.Name, Err: err})
			continue
		}
		set.Users = append(set.Users, tu)
	}
	sort.Slice(set.Users, func(i, j int) bool { return set.Users[i].UID < set.Users[j].UID })
	return set
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
		return nil, fmt.Errorf("resolved user %q (uid %d) is a system account, not a real user", u.Username, uid)
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
