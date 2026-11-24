//go:build darwin || windows

package hookinstall

import (
	"errors"
	"fmt"
	"io/fs"
	"time"
)

// userCandidate is an account found by the cheap listing step. resolve does the
// account lookup and home checks, which can block indefinitely (a home on an
// unreachable network volume), so resolveCandidates bounds them.
type userCandidate struct {
	Name    string
	resolve func() (*TargetUser, error)
}

// userResolveTimeout bounds resolving all candidates. It runs before any
// per-unit deadline, so without it one stalled home would hang the whole run,
// machine policy included.
var userResolveTimeout = 30 * time.Second

// resolveCandidates resolves each candidate concurrently. One that has not
// finished by the deadline is skipped with a timeout error and abandoned; one
// whose home does not exist under an existing parent (macOS creates it at
// first login; a deleted account's profile can outlive it on Windows) is
// dropped silently. validateHomeDir reports a home whose parent is missing,
// such as one on an unmounted volume, so that one is skipped with a reason.
func resolveCandidates(cands []userCandidate, timeout time.Duration) UserSet {
	type resolved struct {
		user *TargetUser
		err  error
	}
	results := make([]chan resolved, len(cands))
	for i, c := range cands {
		ch := make(chan resolved, 1)
		results[i] = ch
		go func() {
			u, err := c.resolve()
			ch <- resolved{u, err}
		}()
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	expired := false
	var set UserSet
	for i, c := range cands {
		var r resolved
		ok := true
		if expired {
			select {
			case r = <-results[i]:
			default:
				ok = false
			}
		} else {
			select {
			case r = <-results[i]:
			case <-deadline.C:
				expired = true
				select {
				case r = <-results[i]:
				default:
					ok = false
				}
			}
		}
		switch {
		case !ok:
			set.Skipped = append(set.Skipped, SkippedUser{Name: c.Name, Err: fmt.Errorf("looking up the account and its home took longer than %s", timeout)})
		case errors.Is(r.err, fs.ErrNotExist):
		case r.err != nil:
			set.Skipped = append(set.Skipped, SkippedUser{Name: c.Name, Err: r.err})
		default:
			set.Users = append(set.Users, r.user)
		}
	}
	return set
}
