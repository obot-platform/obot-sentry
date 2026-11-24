//go:build !windows

package hookinstall

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockUserHomes checks that every directory above each user's home is owned by
// root and writable by no one else. resolveTargetUsers has already refused homes
// not owned by their account and homes that are symlinks; this refuses a home
// whose parents someone other than root could change — a home under
// /Users/Shared, say — since such a parent could be renamed and replaced with a
// link, leading the writes elsewhere. Under /Users, which is root-owned, nothing
// needs holding: no one but root can rename a home there.
func lockUserHomes(list UserList) (UserList, func()) {
	kept := list.Users[:0:0]
	for _, u := range list.Users {
		if reason := unsafeHomeParent(u.HomeDir); reason != "" {
			list.Skipped = append(list.Skipped, SkippedUser{Username: u.Username, Reason: reason})
			continue
		}
		kept = append(kept, u)
	}
	list.Users = kept
	return list, func() {}
}

func unsafeHomeParent(home string) string {
	for dir := filepath.Dir(home); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil {
			return fmt.Sprintf("home %s: %v", home, err)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		switch {
		case info.Mode()&os.ModeSymlink != 0 && dir != filepath.Dir(dir):
			// macOS's own top-level links (/var, /tmp) are root's; anything
			// else is refused below if it is not.
			if !ok || st.Uid != 0 {
				return fmt.Sprintf("home %s: %s is a link not owned by root", home, dir)
			}
		case !ok || st.Uid != 0:
			return fmt.Sprintf("home %s: %s is not owned by root", home, dir)
		case info.Mode().Perm()&0o022 != 0:
			return fmt.Sprintf("home %s: %s is writable by accounts other than root", home, dir)
		}
		if dir == filepath.Dir(dir) {
			return ""
		}
	}
}
