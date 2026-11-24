//go:build windows

package hookinstall

import (
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

// runDirWindows is the root-only directory holding the run log and the
// schedule's record. It is a sibling of %ProgramData%\obot\obot-sentry rather
// than inside it, because that directory keeps ProgramData's default ACL, which
// lets any user create files; prepareRunDir gives this one a protected ACL.
func runDirWindows() string {
	return filepath.Join(windowsProgramData(), "obot", "hook-install")
}

func runLogPath() (string, error)   { return filepath.Join(runDirWindows(), "hook-install.log"), nil }
func runStatePath() (string, error) { return filepath.Join(runDirWindows(), "state.json"), nil }

var procGetTickCount64 = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount64")

// systemBootTime derives the boot time from GetTickCount64, the milliseconds
// since the system started (time asleep included).
func systemBootTime() (time.Time, error) {
	if err := procGetTickCount64.Find(); err != nil {
		return time.Time{}, err
	}
	ms, _, _ := procGetTickCount64.Call()
	return time.Now().Add(-time.Duration(ms) * time.Millisecond), nil
}
