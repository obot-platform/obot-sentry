//go:build !windows

package hookinstall

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestRunChownsEachUsersFiles proves files are handed to the user they belong
// to. Giving a file to another account needs root.
func TestRunChownsEachUsersFiles(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	machineRoot := realTempDir(t)
	var users []*TargetUser
	for i, name := range []string{"alice", "bob"} {
		home := t.TempDir()
		uid := 60001 + i
		if err := os.Chown(home, uid, uid); err != nil {
			t.Fatal(err)
		}
		users = append(users, &TargetUser{Username: name, HomeDir: home, UID: uid, GID: uid})
	}
	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveExe:          func() (string, error) { return macExe, nil },
		ResolveUsers:        func() (UserList, error) { return UserList{Users: users}, nil },
		ProvisionIdentity:   func() (string, error) { return "/id", nil },
		ResolveDestinations: tempDestinations(machineRoot),
		Out:                 &bytes.Buffer{},
	}
	if err := inst.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		for _, rel := range []string{".copilot", ".copilot/hooks", ".copilot/hooks/obot-sentry.json"} {
			info, err := os.Lstat(filepath.Join(u.HomeDir, rel))
			if err != nil {
				t.Fatal(err)
			}
			if st := info.Sys().(*syscall.Stat_t); int(st.Uid) != u.UID {
				t.Fatalf("%s/%s owned by uid %d, want %d", u.Username, rel, st.Uid, u.UID)
			}
		}
	}
}

func TestUnsafeHomeParent(t *testing.T) {
	if got := unsafeHomeParent("/usr/share/x"); got != "" {
		t.Errorf("a home under root-owned directories was refused: %s", got)
	}
	// TempDir lives under a world-writable directory.
	if got := unsafeHomeParent(filepath.Join(t.TempDir(), "home")); got == "" {
		t.Error("a home under a directory others can write was accepted")
	}
}
