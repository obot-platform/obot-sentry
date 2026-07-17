//go:build darwin

package hookinstall

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// reservedDarwinUsers are accounts that are never the real interactive console
// user the installer targets: root itself, the login-window pseudo-user shown
// when no one is logged in, and the setup-assistant account.
var reservedDarwinUsers = map[string]bool{
	"root":         true,
	"loginwindow":  true,
	"_mbsetupuser": true,
}

// resolveTargetUser resolves the active console user on macOS. It prefers a
// verified non-root sudo invoker (the interactive `sudo obot-sentry hook-install`
// case), then falls back to the owner of /dev/console (the MDM/root case where
// a user is logged in at the GUI).
func resolveTargetUser() (*TargetUser, error) {
	if u := targetUserFromSudo(); u != nil {
		return u, nil
	}
	return targetUserFromConsole()
}

// targetUserFromSudo returns the account that invoked sudo, or nil when the
// process was not started through sudo by a real non-root user. A missing,
// malformed, root, or otherwise reserved SUDO_UID is ignored (nil) so
// resolution falls through to the console owner rather than failing outright.
func targetUserFromSudo() *TargetUser {
	uidStr := os.Getenv("SUDO_UID")
	if uidStr == "" {
		return nil
	}
	if uid, err := strconv.Atoi(uidStr); err != nil || uid == 0 {
		return nil
	}
	u, err := user.LookupId(uidStr)
	if err != nil {
		return nil
	}
	tu, err := targetUserFromAccount(u)
	if err != nil {
		return nil
	}
	return tu
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
// TargetUser. It rejects root and reserved pseudo-users and requires a real
// home directory taken from the account database, never the environment.
func targetUserFromAccount(u *user.User) (*TargetUser, error) {
	if reservedDarwinUsers[u.Username] {
		return nil, fmt.Errorf("resolved user %q is not a real console user", u.Username)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return nil, fmt.Errorf("user %q has non-numeric uid %q", u.Username, u.Uid)
	}
	if uid == 0 {
		return nil, fmt.Errorf("resolved user %q is root", u.Username)
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
