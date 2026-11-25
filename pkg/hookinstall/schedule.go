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

// The --if-due throttle fails toward converging: a missing, unreadable,
// untrusted, or future-dated state file makes the run due. State can only ever
// let a run be skipped when an equivalent one happened recently.

const scheduleStateName = "schedule.json"

// dueSlack absorbs scheduler jitter: launchd and Task Scheduler fire polls a
// little early or late, and a poll a second short of the interval would
// otherwise defer convergence by a whole poll period.
const dueSlack = time.Minute

// scheduleState records the last successful run and its inputs; a change to
// any input makes the next poll due.
type scheduleState struct {
	LastRunAt time.Time `json:"lastRunAt"`
	Version   string    `json:"version"`
	Enforce   bool      `json:"enforce"`
	Homes     []string  `json:"homes"` // sorted
}

type scheduleInputs struct {
	Version string
	Enforce bool
	Homes   []string
	// HomesUnknown is set when listing users failed: an empty Homes then
	// says nothing about whether profiles changed, so it is not compared.
	HomesUnknown bool
}

func newScheduleInputs(enforce bool, users []*TargetUser) scheduleInputs {
	homes := make([]string, 0, len(users))
	for _, u := range users {
		homes = append(homes, u.HomeDir)
	}
	slices.Sort(homes)
	return scheduleInputs{Version: version.Get().String(), Enforce: enforce, Homes: homes}
}

type dueDecision struct {
	Due    bool
	Reason string
}

// decideDue takes nil state when there is none or it is untrusted, and a zero
// boot time when unknown.
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
	case !inputs.HomesUnknown && !slices.Equal(state.Homes, inputs.Homes):
		return dueDecision{true, "user profiles changed"}
	case now.Sub(state.LastRunAt)+dueSlack >= interval:
		return dueDecision{true, "interval elapsed"}
	default:
		next := state.LastRunAt.Add(interval).Format(time.RFC3339)
		return dueDecision{false, fmt.Sprintf("last run %s; next due %s", state.LastRunAt.Format(time.RFC3339), next)}
	}
}

// loadScheduleState returns nil state (so the run is due) on any problem, with
// an error for logging unless the file is simply absent.
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

// checkRunFile refuses a non-regular file or one an untrusted account created.
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
