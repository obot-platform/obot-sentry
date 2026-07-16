//go:build windows

package hookinstall

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This file implements Windows console-user discovery. Under an MDM/SYSTEM
// install the active console user is found by enumerating WTS sessions and
// picking the first Active interactive session's user — a locale-independent
// numeric-state check, unlike parsing `query session` text. The home is then
// C:\Users\<username>. Newly created per-user files inherit their DACL from the
// profile, so no account SID is resolved here.
//
// x/sys/windows binds WTSEnumerateSessions/WTSFreeMemory but not
// WTSQuerySessionInformationW, so that one call is declared here against
// wtsapi32.dll. Reading the enumerated C array and the queried string buffer
// requires unsafe pointer access.

const (
	// wtsCurrentServerHandle targets the local session server.
	wtsCurrentServerHandle = windows.Handle(0)
	// wtsUserName is the WTS_INFO_CLASS selecting the session's user name.
	wtsUserName = 5
	// wtsServicesSession is session 0, the non-interactive services session.
	wtsServicesSession = 0
)

var (
	wtsapi32                        = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSQuerySessionInformationW = wtsapi32.NewProc("WTSQuerySessionInformationW")
)

// wtsServiceAccounts are the built-in accounts that are never a real
// interactive console user, matched case-insensitively on the WTS user name.
var wtsServiceAccounts = map[string]bool{
	"system":          true,
	"local service":   true,
	"network service": true,
}

// resolveTargetUser resolves the active console user on Windows: the active WTS
// interactive session's user, else the current USERPROFILE for an interactive
// (non-SYSTEM) invocation.
func resolveTargetUser() (*TargetUser, error) {
	username, home, err := windowsConsoleUser()
	if err != nil {
		return nil, err
	}
	if err := validateHomeDir(home); err != nil {
		return nil, err
	}
	return &TargetUser{Username: username, HomeDir: home}, nil
}

// windowsConsoleUser returns the console user's account name and home directory.
func windowsConsoleUser() (username, home string, err error) {
	if name, wtsErr := wtsActiveConsoleUser(); wtsErr == nil {
		return name, filepath.Join(`C:\Users`, name), nil
	} else if profile := os.Getenv("USERPROFILE"); profile != "" && !strings.Contains(strings.ToLower(profile), "systemprofile") {
		// The USERPROFILE fallback is only meaningful for a non-SYSTEM
		// (interactive/dev) caller; under SYSTEM it points at systemprofile,
		// which is not a real console user.
		return filepath.Base(profile), profile, nil
	} else {
		return "", "", fmt.Errorf("no active console user: %w", wtsErr)
	}
}

// wtsActiveConsoleUser returns the user name of the first Active interactive WTS
// session, skipping the services session (0) and service accounts. On a normal
// workstation there is at most one such session.
func wtsActiveConsoleUser() (string, error) {
	var sessions *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(wtsCurrentServerHandle, 0, 1, &sessions, &count); err != nil {
		return "", fmt.Errorf("enumerating WTS sessions: %w", err)
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(sessions)))

	// sessions points at a C array of count WTS_SESSION_INFO structs.
	for _, s := range unsafe.Slice(sessions, count) {
		if s.State != windows.WTSActive || s.SessionID == wtsServicesSession {
			continue
		}
		name, err := wtsSessionUserName(s.SessionID)
		if err != nil || name == "" {
			continue
		}
		if wtsServiceAccounts[strings.ToLower(name)] {
			continue
		}
		return name, nil
	}
	return "", fmt.Errorf("no active interactive session with a real user")
}

// wtsSessionUserName queries the user name of a WTS session. The API allocates
// the returned UTF-16 buffer, which must be released with WTSFreeMemory.
func wtsSessionUserName(sessionID uint32) (string, error) {
	var buf *uint16
	var bytesReturned uint32
	r1, _, callErr := procWTSQuerySessionInformationW.Call(
		uintptr(wtsCurrentServerHandle),
		uintptr(sessionID),
		uintptr(wtsUserName),
		uintptr(unsafe.Pointer(&buf)),
		uintptr(unsafe.Pointer(&bytesReturned)),
	)
	if r1 == 0 {
		return "", callErr
	}
	if buf == nil {
		return "", nil
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(buf)))
	return windows.UTF16PtrToString(buf), nil
}
