//go:build windows

package hookinstall

import (
	"errors"
	"strings"
	"testing"
)

func TestFindWindowsConsoleUserUsesSessionProfile(t *testing.T) {
	activeUser := func() (uint32, string, error) {
		return 42, "alice", nil
	}
	sessionProfileDir := func(sessionID uint32) (string, error) {
		if sessionID != 42 {
			t.Fatalf("profile lookup got session %d, want 42", sessionID)
		}
		return `D:\Profiles\alice`, nil
	}
	getenv := func(string) string {
		t.Fatal("USERPROFILE fallback should not be read after session profile resolution succeeds")
		return ""
	}

	username, home, err := findWindowsConsoleUser(activeUser, sessionProfileDir, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if username != "alice" {
		t.Fatalf("username = %q, want alice", username)
	}
	if home != `D:\Profiles\alice` {
		t.Fatalf("home = %q, want D:\\Profiles\\alice", home)
	}
}

func TestFindWindowsConsoleUserFallsBackForInteractiveCaller(t *testing.T) {
	activeUser := func() (uint32, string, error) {
		return 42, "alice", nil
	}
	sessionProfileDir := func(uint32) (string, error) {
		return "", errors.New("WTSQueryUserToken requires SYSTEM")
	}
	getenv := func(name string) string {
		if name != "USERPROFILE" {
			t.Fatalf("environment lookup = %q, want USERPROFILE", name)
		}
		return `D:\Profiles\alice`
	}

	username, home, err := findWindowsConsoleUser(activeUser, sessionProfileDir, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if username != "alice" {
		t.Fatalf("username = %q, want alice", username)
	}
	if home != `D:\Profiles\alice` {
		t.Fatalf("home = %q, want D:\\Profiles\\alice", home)
	}
}

func TestFindWindowsConsoleUserRejectsSystemProfileFallback(t *testing.T) {
	activeUser := func() (uint32, string, error) {
		return 42, "alice", nil
	}
	sessionProfileDir := func(uint32) (string, error) {
		return "", errors.New("profile lookup failed")
	}
	getenv := func(string) string {
		return `C:\Windows\System32\config\systemprofile`
	}

	_, _, err := findWindowsConsoleUser(activeUser, sessionProfileDir, getenv)
	if err == nil {
		t.Fatal("SYSTEM profile fallback should be rejected")
	}
	if !strings.Contains(err.Error(), `resolving profile for active console user "alice"`) {
		t.Fatalf("error %q does not preserve the session profile failure", err)
	}
}
