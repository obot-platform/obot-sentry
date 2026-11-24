package hookinstall

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/obot-platform/obot-sentry/pkg/version"
)

// This file is the throttle behind `hook-install --if-due`. The OS scheduler
// (the hook-install LaunchDaemon and scheduled task) polls every few minutes,
// and each poll converges only when the run is due, so an administrator
// retunes the cadence by changing HookIntervalMinutes in the MDM alone.
//
// The throttle fails toward converging: a missing, unreadable, tampered-with,
// or future-dated state file makes the run due. The only thing the state can
// ever do is let a run be skipped when an equivalent run happened recently.

// scheduleStateName is the schedule state file inside the run directory.
const scheduleStateName = "schedule.json"

// dueSlack absorbs scheduler jitter. launchd and Task Scheduler fire a poll a
// little early or late; without slack, a poll landing a second short of the
// interval would defer convergence by a whole extra poll period.
const dueSlack = time.Minute

// scheduleState records the last convergence attempt and the inputs it ran
// with. A change to any of the inputs makes the next poll due immediately,
// rather than waiting out the interval.
type scheduleState struct {
	LastRunAt time.Time `json:"lastRunAt"`
	// Version is the obot-sentry build that ran, so an upgrade converges on
	// its first poll with whatever hooks the new build installs.
	Version string `json:"version"`
	Enforce bool   `json:"enforce"`
	// Homes are the home directories of the users converged, sorted. A new
	// profile (a first sign-in) or a removed one changes the set.
	Homes []string `json:"homes"`
}

// scheduleInputs is what a run would converge with, compared to the recorded
// state to decide whether the run is due.
type scheduleInputs struct {
	Version string
	Enforce bool
	Homes   []string
}

func newScheduleInputs(enforce bool, users []*TargetUser) scheduleInputs {
	homes := make([]string, 0, len(users))
	for _, u := range users {
		homes = append(homes, u.HomeDir)
	}
	slices.Sort(homes)
	return scheduleInputs{Version: version.Get().String(), Enforce: enforce, Homes: homes}
}

// dueDecision explains whether a run is due and why.
type dueDecision struct {
	Due    bool
	Reason string
}

// decideDue reports whether a run with inputs is due given the recorded state
// (nil when there is none or it could not be trusted), the configured interval,
// the current time, and the system boot time (zero when unknown).
func decideDue(state *scheduleState, inputs scheduleInputs, interval time.Duration, now, boot time.Time) dueDecision {
	switch {
	case state == nil:
		return dueDecision{true, "no previous run recorded"}
	case state.LastRunAt.After(now.Add(dueSlack)):
		return dueDecision{true, "last recorded run is in the future"}
	case !boot.IsZero() && state.LastRunAt.Before(boot):
		return dueDecision{true, "system started since the last run"}
	case state.Version != inputs.Version:
		return dueDecision{true, "obot-sentry version changed"}
	case state.Enforce != inputs.Enforce:
		return dueDecision{true, "enforcement setting changed"}
	case !slices.Equal(state.Homes, inputs.Homes):
		return dueDecision{true, "user profiles changed"}
	case now.Sub(state.LastRunAt)+dueSlack >= interval:
		return dueDecision{true, "interval elapsed"}
	default:
		next := state.LastRunAt.Add(interval).Format(time.RFC3339)
		return dueDecision{false, fmt.Sprintf("last run %s; next due %s", state.LastRunAt.Format(time.RFC3339), next)}
	}
}

// loadScheduleState reads the state from dir. Any problem — absent, not a
// regular file, owned by an untrusted account, or malformed — returns nil
// state so the run is due; the error is returned alongside for logging when it
// is anything other than absence.
func loadScheduleState(dir string) (*scheduleState, error) {
	data, err := readRunFile(dir, scheduleStateName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st scheduleState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filepath.Join(dir, scheduleStateName), err)
	}
	return &st, nil
}

// saveScheduleState atomically records a run that started at startedAt.
func saveScheduleState(dir string, inputs scheduleInputs, startedAt time.Time) error {
	data, err := json.MarshalIndent(scheduleState{
		LastRunAt: startedAt.UTC(),
		Version:   inputs.Version,
		Enforce:   inputs.Enforce,
		Homes:     inputs.Homes,
	}, "", "  ")
	if err != nil {
		return err
	}
	return writeRunFile(dir, scheduleStateName, append(data, '\n'))
}

// removeScheduleState forgets the last recorded run, so the next scheduled
// run is due. An absent state file is not an error.
func removeScheduleState(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.Remove(scheduleStateName); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// readRunFile reads name from the run directory through an os.Root, refusing a
// non-regular file or one an untrusted account created.
func readRunFile(dir, name string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if err := checkRunFile(root, dir, name); err != nil {
		return nil, err
	}
	return root.ReadFile(name)
}

// writeRunFile atomically replaces name in the run directory.
func writeRunFile(dir, name string, data []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	path := filepath.Join(dir, name)
	if err := writeAtomic(root, name, path, data, 0o600, nil); err != nil {
		return err
	}
	claimRunFile(path)
	return nil
}

// checkRunFile verifies an existing run-directory file is a regular file owned
// by a trusted account. It returns fs.ErrNotExist (wrapped) when absent.
func checkRunFile(root *os.Root, dir, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular file %q", path)
	}
	return checkRunFileOwner(path, info)
}
