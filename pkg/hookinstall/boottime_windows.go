//go:build windows

package hookinstall

import (
	"time"

	"golang.org/x/sys/windows"
)

var procGetTickCount64 = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount64")

// bootTime returns when the system last started: now minus the milliseconds
// elapsed since boot. A Fast Startup power-on resumes a hibernated kernel and
// does not reset the count; the interval still covers that case.
func bootTime() (time.Time, error) {
	if err := procGetTickCount64.Find(); err != nil {
		return time.Time{}, err
	}
	ms, _, _ := procGetTickCount64.Call()
	return time.Now().Add(-time.Duration(ms) * time.Millisecond), nil
}
