//go:build darwin

package hookinstall

import (
	"os/user"
	"strings"
	"testing"
)

func TestTargetUserFromAccountAcceptsRealUser(t *testing.T) {
	home := t.TempDir()
	tu, err := targetUserFromAccount(&user.User{
		Username: "alice",
		Uid:      "501",
		Gid:      "20",
		HomeDir:  home,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tu.Username != "alice" || tu.HomeDir != home {
		t.Fatalf("got %q at %q, want alice at %q", tu.Username, tu.HomeDir, home)
	}
	if tu.UID != 501 || tu.GID != 20 {
		t.Fatalf("got uid/gid %d/%d, want 501/20", tu.UID, tu.GID)
	}
}

func TestTargetUserFromAccountRejectsSystemAccounts(t *testing.T) {
	for _, tc := range []struct {
		username string
		uid      string
	}{
		{"root", "0"},
		{"daemon", "1"},
		{"_mbsetupuser", "248"}, // Setup Assistant owns /dev/console mid-setup
		{"_windowserver", "88"},
		{"_oahd", "441"}, // highest system account Apple currently ships
		{"nobody", "-2"},
	} {
		t.Run(tc.username, func(t *testing.T) {
			_, err := targetUserFromAccount(&user.User{
				Username: tc.username,
				Uid:      tc.uid,
				Gid:      "20",
				HomeDir:  t.TempDir(),
			})
			if err == nil {
				t.Fatalf("uid %s (%s) should be rejected as a system account", tc.uid, tc.username)
			}
			if !strings.Contains(err.Error(), "system account") {
				t.Errorf("error %q does not explain the rejection", err)
			}
		})
	}
}

func TestTargetUserFromAccountRejectsMalformedIDs(t *testing.T) {
	home := t.TempDir()

	if _, err := targetUserFromAccount(&user.User{
		Username: "alice", Uid: "not-a-number", Gid: "20", HomeDir: home,
	}); err == nil {
		t.Error("a non-numeric uid should be rejected")
	}

	if _, err := targetUserFromAccount(&user.User{
		Username: "alice", Uid: "501", Gid: "not-a-number", HomeDir: home,
	}); err == nil {
		t.Error("a non-numeric gid should be rejected")
	}
}

func TestTargetUserFromAccountRequiresRealHome(t *testing.T) {
	// _mbsetupuser's /var/setup exists, so the uid gate — not the home check —
	// has to be what stops a system account. Conversely a real uid with no home
	// must still fail.
	if _, err := targetUserFromAccount(&user.User{
		Username: "alice", Uid: "501", Gid: "20", HomeDir: "/nonexistent/alice",
	}); err == nil {
		t.Error("a missing home directory should be rejected")
	}
}
