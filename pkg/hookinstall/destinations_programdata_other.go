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
