//go:build !windows

package hookinstall

import (
	"fmt"
	"os"
	"syscall"
)

// configReadFlags opens a config without blocking, so a FIFO cannot stall the
// reader; the descriptor is then refused for not being a regular file.
const configReadFlags = os.O_RDONLY | syscall.O_NONBLOCK

func secureMachineConfig(string) error { return nil }

func secureMachineConfigDir(string) error { return nil }

// chownToUser re-owns paths by name. Only directories are passed: see
// commitConfigFile.
func chownToUser(root *os.Root, u *TargetUser, paths []string) error {
	for _, p := range paths {
		if err := root.Lchown(p, u.UID, u.GID); err != nil {
			return err
		}
	}
	return nil
}

// chownFile gives an open file to u through its descriptor.
func chownFile(f *os.File, u *TargetUser) error {
	return f.Chown(u.UID, u.GID)
}

// checkConfigFileIdentity refuses a config that has more than one link, or a
// user-scoped one owned by anyone but the user or root. Root is allowed because
// an agent run under sudo leaves root-owned files in the home, and a user cannot
// put a root-owned single-link file there themselves.
func checkConfigFileIdentity(_ *os.File, info os.FileInfo, scope Scope, u *TargetUser, absPath string) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read the identity of config %q", absPath)
	}
	if st.Nlink != 1 {
		return fmt.Errorf("refusing config %q with %d hard links", absPath, st.Nlink)
	}
	if scope == ScopeUser && u != nil && st.Uid != 0 && int(st.Uid) != u.UID {
		return fmt.Errorf("refusing config %q owned by uid %d, not %s", absPath, st.Uid, u.Username)
	}
	return nil
}
