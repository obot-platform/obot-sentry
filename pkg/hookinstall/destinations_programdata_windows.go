//go:build windows

package hookinstall

import "golang.org/x/sys/windows"

// windowsProgramData asks Windows for the machine-wide known folder rather than
// trusting an environment variable inherited by the elevated installer.
func windowsProgramData() string {
	path, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil || path == "" {
		// The conventional path remains subject to the local-fixed-drive check in
		// openConfigRoot before the installer touches it.
		return `C:\ProgramData`
	}
	return path
}
