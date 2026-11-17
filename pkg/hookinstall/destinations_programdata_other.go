//go:build !windows

package hookinstall

import "os"

// Non-Windows tests model the Windows destination layout through ProgramData.
// Production Windows builds use the Known Folder API instead.
func windowsProgramData() string {
	if path := os.Getenv("ProgramData"); path != "" {
		return path
	}
	return `C:\ProgramData`
}

// windowsProgramFiles is the Program Files counterpart of windowsProgramData.
func windowsProgramFiles() string {
	if path := os.Getenv("ProgramFiles"); path != "" {
		return path
	}
	return `C:\Program Files`
}
