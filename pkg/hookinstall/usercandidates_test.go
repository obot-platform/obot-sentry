//go:build darwin || windows

package hookinstall

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"
)

func TestResolveCandidatesBoundsAStalledHome(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	ok := func(name string) userCandidate {
		return userCandidate{Name: name, resolve: func() (*TargetUser, error) {
			return &TargetUser{Username: name, HomeDir: "/home/" + name}, nil
		}}
	}
	cands := []userCandidate{
		ok("alice"),
		{Name: "stalled", resolve: func() (*TargetUser, error) { <-block; return nil, nil }},
		{Name: "never-logged-in", resolve: func() (*TargetUser, error) { return nil, fs.ErrNotExist }},
		{Name: "broken", resolve: func() (*TargetUser, error) { return nil, errors.New("bad home") }},
		ok("bob"),
	}

	start := time.Now()
	set := resolveCandidates(cands, 200*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("resolveCandidates took %s; the stalled home was not bounded", elapsed)
	}
	if len(set.Users) != 2 || set.Users[0].Username != "alice" || set.Users[1].Username != "bob" {
		t.Fatalf("users = %+v, want alice and bob", set.Users)
	}
	if len(set.Skipped) != 2 || set.Skipped[0].Name != "stalled" || !strings.Contains(set.Skipped[0].Err.Error(), "took longer") ||
		set.Skipped[1].Name != "broken" {
		t.Fatalf("skipped = %+v, want the stalled (timeout) and broken accounts only", set.Skipped)
	}
}

func TestResolveCandidatesRecoversAPanic(t *testing.T) {
	set := resolveCandidates([]userCandidate{
		{Name: "carol", resolve: func() (*TargetUser, error) { panic("boom") }},
		{Name: "dave", resolve: func() (*TargetUser, error) { return &TargetUser{Username: "dave"}, nil }},
	}, time.Minute)
	if len(set.Users) != 1 || set.Users[0].Username != "dave" {
		t.Fatalf("users = %+v, want dave", set.Users)
	}
	var pe *panicError
	if len(set.Skipped) != 1 || !errors.As(set.Skipped[0].Err, &pe) || len(pe.stack) == 0 {
		t.Fatalf("skipped = %+v, want carol with a recovered panic and its stack", set.Skipped)
	}
}
