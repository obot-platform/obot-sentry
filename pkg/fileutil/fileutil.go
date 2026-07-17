// Package fileutil contains small filesystem helpers shared by obot-sentry packages.
package fileutil

import (
	"os"
	"path/filepath"
)

// MkdirAllPrivate creates path and restricts the final directory to the current
// user on platforms that support owner-only permissions.
func MkdirAllPrivate(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return applyPrivateDirPermissions(path)
}

// WriteFileAtomic writes data to path through a temporary file in the same
// directory, then renames it into place with perm.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return applyFilePermissions(path, perm)
}
