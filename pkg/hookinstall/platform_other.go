//go:build !darwin && !windows

package hookinstall

import "os"

func checkPrivilege() error {
	return errUnsupportedPlatform
}

func resolveTargetUsers() (UserSet, error) {
	return UserSet{}, errUnsupportedPlatform
}

func validateExecutableOwner(string, os.FileInfo) error { return nil }
