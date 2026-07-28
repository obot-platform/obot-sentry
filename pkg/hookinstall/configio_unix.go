//go:build !windows

package hookinstall

import "os"

func chownToUser(root *os.Root, u *TargetUser, paths []string) error {
	for _, p := range paths {
		if err := root.Lchown(p, u.UID, u.GID); err != nil {
			return err
		}
	}
	return nil
}
