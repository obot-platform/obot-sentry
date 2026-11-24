//go:build darwin

package hookinstall

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateExecutableOwnerRequiresRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "obot-sentry")
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	err = validateExecutableOwner(path, info)
	if os.Geteuid() == 0 {
		if err != nil {
			t.Fatalf("root-owned test executable was rejected: %v", err)
		}
	} else if err == nil || !strings.Contains(err.Error(), "not root") {
		t.Fatalf("user-owned test executable was accepted: %v", err)
	}
}

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

func TestDarwinUsersSkipsInvalidAccountsAndSortsByUID(t *testing.T) {
	aliceHome, bobHome, sharedHome := t.TempDir(), t.TempDir(), t.TempDir()
	accounts := map[string]*user.User{
		"502": {Uid: "502", Gid: "20", Username: "bob", HomeDir: bobHome},
		"501": {Uid: "501", Gid: "20", Username: "alice", HomeDir: aliceHome},
		"503": {Uid: "503", Gid: "20", Username: "gone", HomeDir: filepath.Join(bobHome, "missing")},
		"505": {Uid: "505", Gid: "20", Username: "shared", HomeDir: sharedHome},
	}
	lookup := func(uid string) (*user.User, error) {
		if u, ok := accounts[uid]; ok {
			return u, nil
		}
		return nil, user.UnknownUserIdError(0)
	}
	set := darwinUsers([]dsclAccount{
		{Name: "bob", UID: "502"},
		{Name: "alice", UID: "501"},
		{Name: "gone", UID: "503"},
		{Name: "ghost", UID: "504"},
		{Name: "shared", UID: "505"},
	}, lookup, func(home string, uid int) error {
		if home == sharedHome {
			return errors.New("home owned by another account")
		}
		return nil
	})

	if len(set.Users) != 2 || set.Users[0].Username != "alice" || set.Users[1].Username != "bob" {
		t.Fatalf("users = %+v, want alice then bob", set.Users)
	}
	// "gone" has never logged in, so has no home and is dropped silently.
	if len(set.Skipped) != 2 || set.Skipped[0].Name != "ghost" || set.Skipped[1].Name != "shared" {
		t.Fatalf("skipped = %+v, want ghost and shared", set.Skipped)
	}
}

func TestHomeOwnedBy(t *testing.T) {
	home := t.TempDir()
	if err := homeOwnedBy(home, os.Getuid()); err != nil {
		t.Fatalf("a home owned by the account was rejected: %v", err)
	}
	if err := homeOwnedBy(home, os.Getuid()+1); err == nil {
		t.Fatal("a home owned by another uid was accepted")
	}
}
