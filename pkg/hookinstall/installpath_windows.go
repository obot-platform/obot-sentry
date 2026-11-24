//go:build windows

package hookinstall

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func validateInstallHomePath(path string) error {
	if err := validateLocalFixedPath(path); err != nil {
		return fmt.Errorf("resolved user home %q is not on a local fixed drive: %w", path, err)
	}
	return nil
}

func validateInstallMachinePath(path string) error {
	if err := validateLocalFixedPath(path); err != nil {
		return fmt.Errorf("machine-scoped hook path %q is not on a local fixed drive: %w", path, err)
	}
	return nil
}

func validateLocalFixedPath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("path is not absolute")
	}
	clean := filepath.Clean(path)
	if strings.HasPrefix(clean, `\\`) {
		return fmt.Errorf("UNC and device paths are not permitted")
	}
	volume := filepath.VolumeName(clean)
	if len(volume) != 2 || volume[1] != ':' {
		return fmt.Errorf("path has no local drive-letter volume")
	}
	root, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return fmt.Errorf("encoding volume root: %w", err)
	}
	if driveType := windows.GetDriveType(root); driveType != windows.DRIVE_FIXED {
		return fmt.Errorf("drive type is %d, want fixed drive type %d", driveType, windows.DRIVE_FIXED)
	}
	return nil
}
