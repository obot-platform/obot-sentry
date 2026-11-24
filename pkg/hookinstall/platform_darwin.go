//go:build darwin

package hookinstall

import (
	"context"
	"fmt"
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

// resolveTargetUsers lists the local node's accounts (`dscl .`), plus the
// signed-in console user and SUDO_UID when they are not among them: on a Mac
// bound to Active Directory without mobile accounts, or to LDAP/Open
// Directory, a network account has no local record but does have a local
// home. user.LookupId searches every directory node, so it resolves those
// too. Each home comes from the account database, never the environment.
func resolveTargetUsers() (UserSet, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dsclTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/dscl", ".", "-list", "/Users", "UniqueID").Output()
	if err != nil {
		return UserSet{}, fmt.Errorf("listing local accounts with dscl: %w", err)
	}
	cands := darwinCandidates(parseDsclUserList(out), signedInUIDs(), user.LookupId, homeOwnedBy)
	set := resolveCandidates(cands, userResolveTimeout)
	sort.Slice(set.Users, func(i, j int) bool { return set.Users[i].UID < set.Users[j].UID })
	return set, nil
}

// signedInUIDs is the owner of /dev/console (root when no one is signed in)
// and SUDO_UID for an interactive `sudo obot-sentry hook-install`.
func signedInUIDs() []string {
	var uids []string
	if info, err := os.Stat("/dev/console"); err == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != 0 {
			uids = append(uids, strconv.FormatUint(uint64(st.Uid), 10))
		}
	}
	if uid := os.Getenv("SUDO_UID"); uid != "" {
		uids = append(uids, uid)
	}
	return uids
}

func darwinCandidates(accounts []dsclAccount, extraUIDs []string, lookup func(uid string) (*user.User, error), ownedBy func(home string, uid int) error) []userCandidate {
	seen := map[string]bool{}
	var cands []userCandidate
	add := func(name, uid string) {
		if seen[uid] {
			return
		}
		seen[uid] = true
		cands = append(cands, userCandidate{Name: name, resolve: func() (*TargetUser, error) {
			return darwinUser(uid, lookup, ownedBy)
		}})
	}
	for _, a := range accounts {
		add(a.Name, a.UID)
	}
	for _, uid := range extraUIDs {
		add("uid "+uid, uid)
	}
	return cands
}

func darwinUser(uid string, lookup func(uid string) (*user.User, error), ownedBy func(home string, uid int) error) (*TargetUser, error) {
	u, err := lookup(uid)
	if err != nil {
		return nil, fmt.Errorf("looking up uid %s: %v", uid, err)
	}
	tu, err := targetUserFromAccount(u)
	if err != nil {
		return nil, err
	}
	if err := ownedBy(tu.HomeDir, tu.UID); err != nil {
		return nil, err
	}
	return tu, nil
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
