//go:build !windows

package fileutil

import "os"

func applyFilePermissions(path string, perm os.FileMode) error {
	return os.Chmod(path, perm)
}

func applyPrivateDirPermissions(path string) error {
	return os.Chmod(path, 0o700)
}
