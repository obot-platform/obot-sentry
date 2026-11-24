package hookinstall

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"time"
)

// Schedule turns hook-install into a periodic check. The OS scheduler starts it
// every few minutes; Schedule decides whether a check is due and records each one
// it runs, so an administrator changes the cadence through MDM configuration
// alone, without re-registering a launchd job or a scheduled task.
//
// A check is due when the interval has passed since the last one, and also,
// without waiting for the interval, when the last one failed (after a delay that
// grows with each consecutive failure), when the machine
// has restarted since, when a user profile has appeared or gone, or when what a
// check installs has changed — a new obot-sentry version, a moved executable, or
// enforcement toggled.
type Schedule struct {
	// Interval is the time between checks.
	Interval time.Duration
	// StatePath is where the last check is recorded; defaults to the
	// platform's root-only location (runStatePath).
	StatePath string
	// Version is the running obot-sentry version, recorded so an upgrade checks
	// at once.
	Version string
	// Now defaults to time.Now.
	Now func() time.Time
	// BootTime reports when the machine last started; defaults to the platform
	// query. An error is treated as no restart.
	BootTime func() (time.Time, error)
}

// runRecord is the state Schedule keeps between checks. It holds no
// configuration contents or credentials.
type runRecord struct {
	LastRunAt  time.Time `json:"lastRunAt"`
	Version    string    `json:"version"`
	Executable string    `json:"executable"`
	Enforce    bool      `json:"enforce"`
	// Users are the home directories of the users the check converged.
	Users []string `json:"users"`
	// Reason says why the check ran, for the record's reader.
	Reason string `json:"reason"`
	// Failed records that some live destination could not be converged, or
	// the users could not all be listed, so a retry comes sooner than the
	// interval. Failures counts consecutive failed checks, and spaces the
	// retries out (see retryDelay).
	Failed   bool `json:"failed,omitempty"`
	Failures int  `json:"failures,omitempty"`
}

// retryBase is the delay before retrying a failed check, doubled for each
// further consecutive failure up to the interval: 5, 10, 20 minutes, and so on.
// A failure a user can cause in their own home, such as an unparsable file,
// then costs a check a few times an hour at most rather than every tick.
const retryBase = 5 * time.Minute

func (s *Schedule) retryDelay(failures int) time.Duration {
	d := retryBase
	for n := 1; n < failures && d < s.Interval; n++ {
		d *= 2
	}
	return min(d, s.Interval)
}

func (r runRecord) failures() int {
	if r.Failures == 0 && r.Failed {
		return 1
	}
	return r.Failures
}

// dueSlack is how much earlier than the interval a check counts as due. The OS
// scheduler starts hook-install on a fixed tick, and each start lands a little
// earlier or later relative to it; without slack, a check started a moment
// short of the interval would wait a whole extra tick.
const dueSlack = time.Minute

// check reports whether a check is due for plan, returning the record to save
// once it has run.
func (s *Schedule) check(plan Plan, log *slog.Logger) (runRecord, bool) {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	next := runRecord{
		LastRunAt:  now.UTC(),
		Version:    s.Version,
		Executable: plan.Executable,
		Enforce:    plan.Enforce,
		Users:      userHomes(plan.Users),
	}

	path, err := s.statePath()
	if err != nil {
		next.Reason = "no state location: " + err.Error()
		return next, true
	}
	prev, err := loadRunRecord(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Warn("ignoring hook check record", "path", path, "error", err)
		}
		next.Reason = "no previous check"
		return next, true
	}
	next.Failures = prev.failures()
	next.Reason = s.dueReason(prev, next, now)
	return next, next.Reason != ""
}

// dueReason returns why a check is due, or "" when it is not.
func (s *Schedule) dueReason(prev, next runRecord, now time.Time) string {
	switch {
	case prev.LastRunAt.After(now):
		// A record from the future means the clock moved back or the record was
		// tampered with; either way it cannot be trusted to defer a check.
		return "previous check is in the future"
	case prev.failures() > 0 && now.Sub(prev.LastRunAt) >= s.retryDelay(prev.failures())-dueSlack:
		return "previous check failed"
	case now.Sub(prev.LastRunAt) >= s.Interval-dueSlack:
		return "interval elapsed"
	case prev.Version != next.Version:
		return "obot-sentry version changed"
	case prev.Executable != next.Executable:
		return "executable changed"
	case prev.Enforce != next.Enforce:
		return "enforcement setting changed"
	case !slices.Equal(prev.Users, next.Users):
		return "users changed"
	}
	bootTime := s.BootTime
	if bootTime == nil {
		bootTime = systemBootTime
	}
	if booted, err := bootTime(); err == nil && prev.LastRunAt.Before(booted) {
		return "system restarted"
	}
	return ""
}

// record saves next as the last check.
func (s *Schedule) record(next runRecord) error {
	path, err := s.statePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	d, err := openRunDir(filepath.Dir(path), true)
	if err != nil {
		return err
	}
	defer d.Close()
	return writeAtomic(d.root, filepath.Base(path), path, append(data, '\n'), 0o600, nil)
}

func (s *Schedule) statePath() (string, error) {
	if s.StatePath != "" {
		return s.StatePath, nil
	}
	return runStatePath()
}

// loadRunRecord reads the last check's record. A record that an unprivileged
// account could have written is refused: trusting one would let a user defer
// the next repair of hooks they had just removed.
func loadRunRecord(path string) (runRecord, error) {
	d, err := openRunDir(filepath.Dir(path), false)
	if err != nil {
		return runRecord{}, err
	}
	defer d.Close()
	f, err := d.root.Open(filepath.Base(path))
	if err != nil {
		return runRecord{}, err
	}
	defer func() { _ = f.Close() }()
	if err := checkRunFile(f, path); err != nil {
		return runRecord{}, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return runRecord{}, err
	}
	var r runRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return runRecord{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return r, nil
}

func userHomes(users []*TargetUser) []string {
	homes := make([]string, 0, len(users))
	for _, u := range users {
		homes = append(homes, u.HomeDir)
	}
	slices.Sort(homes)
	return homes
}

// clearRunRecord removes the platform's record of the last check.
func clearRunRecord() error {
	path, err := runStatePath()
	if err != nil {
		return err
	}
	d, err := openRunDir(filepath.Dir(path), false)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.root.Remove(filepath.Base(path))
}

// ClearCheckRecord forgets the last periodic check, so the next one runs at
// once. The installers call it (through hook-install --clear-check-record) so a
// reinstall of the same version repairs hooks without waiting out the interval.
// It needs the same privilege as hook-install.
func ClearCheckRecord() error {
	if err := checkPrivilege(); err != nil {
		return err
	}
	if err := clearRunRecord(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// RemoveRunData removes the run log and the schedule's record. The uninstallers
// call it (through hook-install --remove-run-data) so the removal goes through
// the same link-safe code that wrote them.
func RemoveRunData() error {
	if err := checkPrivilege(); err != nil {
		return err
	}
	var errs []error
	seen := map[string]bool{}
	for _, path := range []func() (string, error){runStatePath, runLogPath} {
		p, err := path()
		if err != nil {
			return err
		}
		dir := filepath.Dir(p)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if err := removeRunDataNow(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// LastRecordedEnforcement returns the enforcement setting the last periodic
// check converged with, read from the record at statePath (the platform
// location when empty). hook-install --if-due falls back on it when the MDM
// store cannot be read, rather than guessing and perhaps stripping the
// enforcement hooks.
func LastRecordedEnforcement(statePath string) (bool, error) {
	if statePath == "" {
		p, err := runStatePath()
		if err != nil {
			return false, err
		}
		statePath = p
	}
	r, err := loadRunRecord(statePath)
	if err != nil {
		return false, err
	}
	return r.Enforce, nil
}
