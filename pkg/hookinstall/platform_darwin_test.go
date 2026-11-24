//go:build darwin

package hookinstall

import (
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

func TestVetDarwinAccounts(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	ready := t.TempDir()
	if err := os.Mkdir(filepath.Join(ready, "Library"), 0o700); err != nil {
		t.Fatal(err)
	}
	fresh := t.TempDir()
	homes := map[string]string{"501": ready, "502": fresh, "503": filepath.Join(t.TempDir(), "unmounted")}
	lookup := func(uid string) (*user.User, error) {
		home, ok := homes[uid]
		if !ok {
			return nil, user.UnknownUserIdError(0)
		}
		return &user.User{Username: "u" + uid, Uid: me.Uid, Gid: me.Gid, HomeDir: home}, nil
	}
	list := vetDarwinAccounts([]localAccount{{"u501", "501"}, {"u502", "502"}, {"u503", "503"}, {"u504", "504"}}, lookup)
	if len(list.Users) != 1 || list.Users[0].HomeDir != ready {
		t.Fatalf("Users = %+v, want only the set-up home", list.Users)
	}
	if len(list.Skipped) != 3 {
		t.Fatalf("Skipped = %+v, want the fresh, unmounted, and unknown accounts", list.Skipped)
	}
	if !strings.Contains(list.Skipped[0].Reason, "first sign-in") {
		t.Errorf("fresh home skipped for %q", list.Skipped[0].Reason)
	}
}
