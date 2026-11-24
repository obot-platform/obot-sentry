//go:build !darwin && !windows

package hookinstall

import "os"

func checkPrivilege() error {
	return errUnsupportedPlatform
}

func resolveTargetUsers() (UserList, error) {
	return UserList{}, errUnsupportedPlatform
}

func validateExecutableOwner(string, os.FileInfo) error { return nil }
