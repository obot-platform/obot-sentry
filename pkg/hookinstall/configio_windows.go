//go:build windows

package hookinstall

import "os"

// chownToUser is a no-op on Windows: new files inherit their DACL from the parent.
func chownToUser(_ *os.Root, _ *TargetUser, _ []string) error {
	return nil
}
