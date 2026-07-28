//go:build !darwin && !windows

package hookinstall

func checkPrivilege() error {
	return errUnsupportedPlatform
}

func resolveTargetUser() (*TargetUser, error) {
	return nil, errUnsupportedPlatform
}
