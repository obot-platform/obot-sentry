//go:build !windows

package hookinstall

// lockMachineDirs is a no-op outside Windows: the machine-wide destinations on
// macOS live under directories only root can change.
func lockMachineDirs([]Destination, bool) (map[string]error, func()) {
	return nil, func() {}
}
