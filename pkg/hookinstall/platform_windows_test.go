//go:build windows

package hookinstall

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

const (
	systemSIDString         = "S-1-5-18"
	localServiceSIDString   = "S-1-5-19"
	networkServiceSIDString = "S-1-5-20"
	aliceSIDString          = "S-1-5-21-1004336348-1177238915-682003330-1001"
	bobSIDString            = "S-1-5-21-1004336348-1177238915-682003330-1002"
)

func mustSID(t *testing.T, s string) *windows.SID {
	t.Helper()
	sid, err := windows.StringToSid(s)
	if err != nil {
		t.Fatalf("parsing SID %s: %v", s, err)
	}
	return sid
}

func noCallingUser(t *testing.T) func() (sessionUser, error) {
	t.Helper()
	return func() (sessionUser, error) {
		t.Fatal("calling-process fallback should not run after a session resolved")
		return sessionUser{}, nil
	}
}

func TestFindWindowsConsoleUserUsesSessionToken(t *testing.T) {
	sessions := func() ([]sessionUser, error) {
		return []sessionUser{
			{sid: mustSID(t, aliceSIDString), name: `CORP\alice`, home: `D:\Profiles\alice`},
		}, nil
	}

	user, err := findWindowsConsoleUser(sessions, noCallingUser(t))
	if err != nil {
		t.Fatal(err)
	}
	if user.name != `CORP\alice` {
		t.Fatalf("name = %q, want CORP\\alice", user.name)
	}
	if user.home != `D:\Profiles\alice` {
		t.Fatalf("home = %q, want D:\\Profiles\\alice", user.home)
	}
}

func TestFindWindowsConsoleUserSkipsServiceAccountSessions(t *testing.T) {
	sessions := func() ([]sessionUser, error) {
		return []sessionUser{
			{sid: mustSID(t, systemSIDString), name: "SYSTEM", home: `C:\Windows\System32\config\systemprofile`},
			{sid: mustSID(t, localServiceSIDString), name: "LOCAL SERVICE", home: `C:\Windows\ServiceProfiles\LocalService`},
			{sid: mustSID(t, networkServiceSIDString), name: "NETWORK SERVICE", home: `C:\Windows\ServiceProfiles\NetworkService`},
			{sid: mustSID(t, aliceSIDString), name: `CORP\alice`, home: `D:\Profiles\alice`},
		}, nil
	}

	user, err := findWindowsConsoleUser(sessions, noCallingUser(t))
	if err != nil {
		t.Fatal(err)
	}
	if user.name != `CORP\alice` {
		t.Fatalf("name = %q, want CORP\\alice — a service session was picked as the console user", user.name)
	}
}

func TestFindWindowsConsoleUserIgnoresAccountName(t *testing.T) {
	sessions := func() ([]sessionUser, error) {
		return []sessionUser{
			// A service account under a name no name-based filter would list.
			{sid: mustSID(t, networkServiceSIDString), name: "a name we never listed", home: `C:\Windows\ServiceProfiles\NetworkService`},
			// A real user under a name a name-based filter would have rejected.
			{sid: mustSID(t, bobSIDString), name: "NETWORK SERVICE", home: `D:\Profiles\bob`},
		}, nil
	}

	user, err := findWindowsConsoleUser(sessions, noCallingUser(t))
	if err != nil {
		t.Fatal(err)
	}
	if user.home != `D:\Profiles\bob` {
		t.Fatalf("home = %q, want D:\\Profiles\\bob — the account name decided instead of the SID", user.home)
	}
}

// A session table holding nothing but service accounts resolves nobody, so the
// calling process's own token is the remaining candidate.
func TestFindWindowsConsoleUserFallsBackPastServiceSessions(t *testing.T) {
	sessions := func() ([]sessionUser, error) {
		return []sessionUser{
			{sid: mustSID(t, networkServiceSIDString), name: "NETWORK SERVICE", home: `C:\Windows\ServiceProfiles\NetworkService`},
		}, nil
	}
	callingUser := func() (sessionUser, error) {
		return sessionUser{sid: mustSID(t, bobSIDString), name: `CORP\bob`, home: `D:\Profiles\bob`}, nil
	}

	user, err := findWindowsConsoleUser(sessions, callingUser)
	if err != nil {
		t.Fatal(err)
	}
	if user.name != `CORP\bob` {
		t.Fatalf("name = %q, want CORP\\bob", user.name)
	}
}

// An elevated-Administrator install cannot open any session token (that needs
// SE_TCB_NAME), so it resolves itself.
func TestFindWindowsConsoleUserFallsBackForInteractiveCaller(t *testing.T) {
	sessions := func() ([]sessionUser, error) {
		return nil, nil
	}
	callingUser := func() (sessionUser, error) {
		return sessionUser{sid: mustSID(t, aliceSIDString), name: `CORP\alice`, home: `D:\Profiles\alice`}, nil
	}

	user, err := findWindowsConsoleUser(sessions, callingUser)
	if err != nil {
		t.Fatal(err)
	}
	if user.home != `D:\Profiles\alice` {
		t.Fatalf("home = %q, want D:\\Profiles\\alice", user.home)
	}
}

func TestFindWindowsConsoleUserRejectsServiceAccountFallback(t *testing.T) {
	sessions := func() ([]sessionUser, error) {
		return nil, nil
	}
	callingUser := func() (sessionUser, error) {
		return sessionUser{sid: mustSID(t, systemSIDString), name: "SYSTEM", home: `C:\Windows\System32\config\systemprofile`}, nil
	}

	_, err := findWindowsConsoleUser(sessions, callingUser)
	if err == nil {
		t.Fatal("a SYSTEM caller with no interactive session has no console user to fall back to")
	}
	if !strings.Contains(err.Error(), "no interactive session is owned by a real user") {
		t.Errorf("error %q does not report why no session resolved", err)
	}
	if !strings.Contains(err.Error(), `service account "SYSTEM"`) {
		t.Errorf("error %q does not report why the fallback was rejected", err)
	}
}

func TestFindWindowsConsoleUserPreservesEnumerationError(t *testing.T) {
	sessions := func() ([]sessionUser, error) {
		return nil, errors.New("enumerating WTS sessions: access denied")
	}
	callingUser := func() (sessionUser, error) {
		return sessionUser{}, errors.New("opening current process token: access denied")
	}

	_, err := findWindowsConsoleUser(sessions, callingUser)
	if err == nil {
		t.Fatal("both lookups failed, want an error")
	}
	for _, want := range []string{"enumerating WTS sessions", "opening current process token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not preserve %q", err, want)
		}
	}
}
