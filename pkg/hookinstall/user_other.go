//go:build !darwin && !windows

package hookinstall

// resolveTargetUser is never reached on unsupported platforms: the platform
// check in install.go rejects them before any user resolution. It exists so the
// package builds and links on Linux and other GOOS values.
func resolveTargetUser() (*TargetUser, error) {
	return nil, errUnsupportedPlatform
}
