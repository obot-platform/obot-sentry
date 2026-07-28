//go:build darwin

package hookinstall

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// minDarwinUserUID is the lowest uid macOS assigns to a real local account.
// Apple reserves everything below 500 for system use.
const minDarwinUserUID = 500

// checkPrivilege requires an effective UID of 0 on macOS. Machine policy and
// per-user files under other accounts' homes both require root; anything less
// is rejected before the first config write.
func checkPrivilege() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("obot-sentry hook-install must run as root on macOS; rerun with sudo")
	}
	return nil
}

// resolveTargetUser resolves the active console user on macOS. It prefers the
// account named by SUDO_UID (the interactive `sudo obot-sentry hook-install`
// case), then falls back to the owner of /dev/console (the MDM/root case where
// a user is logged in at the GUI).
func resolveTargetUser() (*TargetUser, error) {
	if u := targetUserFromSudo(); u != nil {
		return u, nil
	}
	return targetUserFromConsole()
}

func targetUserFromSudo() *TargetUser {
	uidStr := os.Getenv("SUDO_UID")
	if uidStr == "" {
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
