package hookinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"runtime"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/obot-platform/obot-sentry/pkg/datadir"
	"github.com/obot-platform/obot-sentry/pkg/identity"
)

// errUnsupportedPlatform is returned for any GOOS other than darwin or windows.
// It is a plain error so the CLI maps it to the normal runtime exit code: hook
// management is local configuration, not a deployment-config failure.
var errUnsupportedPlatform = errors.New("hook management is only supported on macOS and Windows")

// supportedPlatform reports whether goos has a defined destination layout and
// privilege model.
func supportedPlatform(goos string) bool {
	return goos == "darwin" || goos == "windows"
}

// Installer converges or removes the managed hooks. Every external dependency is an
// injectable seam so platform discovery, command generation, and (later) the
// filesystem commit can be exercised independently in tests without root, a
// specific OS, or a real obot-sentry binary on disk.
type Installer struct {
	// GOOS selects the destination layout and command quoting; defaults to
	// runtime.GOOS.
	GOOS string
	// Privilege verifies the process holds the elevation required to write
	// machine policy and per-user files; defaults to the platform check.
	Privilege func() error
	// ResolveExe returns the validated, durable obot-sentry path embedded in hook
	// commands; defaults to the platform's MDM package path +
	// validateExecutable.
	ResolveExe func() (string, error)
	// ResolveUsers lists the local accounts whose per-user files are
	// converged — every initialized user profile on the machine, not only the
	// one at the console; defaults to the platform resolver.
	ResolveUsers func() (UserList, error)
	// LockHomes verifies each resolved user's home and holds it so it cannot be
	// swapped for a link while the run writes in it, moving any that fail to
	// Skipped; the returned function releases them. Defaults (in New) to the
	// platform implementation; nil skips it, as tests do.
	LockHomes func(UserList) (UserList, func())
	// ProvisionIdentity establishes the shared machine device identity read by
	// per-user hook executions and returns the resolved machine directory;
	// defaults to defaultProvisionIdentity (datadir.MachineDir + identity.Load).
	ProvisionIdentity func() (string, error)
	// ResolveDestinations returns the managed destinations for a GOOS; defaults
	// to Destinations. Tests inject a set rooted under a temporary directory so
	// the anchored commit can be exercised without writing to real system paths.
	ResolveDestinations func(goos string) []Destination
	// Enforce installs the pre-tool enforcement hooks in addition to the
	// post-tool audit hooks. It is resolved by the command layer from the
	// --enforce flag, the environment, and the MDM store, in that order.
	Enforce bool
	// Uninstall removes every marker-owned hook instead of installing desired
	// hooks. Supporting settings without ownership markers remain unchanged.
	Uninstall bool
	// Out receives operator-facing output; defaults to os.Stdout.
	Out io.Writer
	// ErrOut receives warnings that are not part of the summary; defaults to
	// os.Stderr.
	ErrOut io.Writer
	// Log receives one structured record per change, failure, and run. It is
	// the durable record of repairs, kept apart from Out's human-readable
	// table. When nil, OpenLog supplies one; when both are nil, records are
	// discarded.
	Log *slog.Logger
	// OpenLog opens the run log Log records are written to. It is called only
	// once the privilege check has passed, since the log lives where only an
	// administrator can write. A failure to open it is a warning, not a reason
	// to leave the hooks unrepaired.
	OpenLog func() (io.WriteCloser, error)
	// LockMachineDirs verifies and holds the directories of the machine-wide
	// destinations for the duration of their convergence, creating missing
	// ones when create is set, so none can be swapped for a link while they are
	// written. It returns an error per destination (keyed by Abs) whose
	// directories could not be secured, and a release function. Defaults (in
	// New) to the platform implementation; nil skips it, as tests do.
	LockMachineDirs func(dests []Destination, create bool) (map[string]error, func())
	// GroupTimeout bounds the convergence of the machine-wide destinations and
	// of each user's; a group that takes longer is abandoned and reported
	// failed, so a file that blocks its reader (a FIFO, a held oplock, a hung
	// network share) cannot stall the rest. Defaults to defaultGroupTimeout.
	GroupTimeout time.Duration
	// ClearSchedule forgets the last periodic check, so the next one is due at
	// once. hook-uninstall calls it, so hooks reinstalled afterwards — by a
	// reinstall of the same version, say — are not held back by the record of a
	// check made before they were removed. Defaults (in New) to the platform
	// record; nil skips it, as tests do.
	ClearSchedule func() error
	// Schedule, when set, makes Run a periodic check: it converges only when
	// one is due (see Schedule.due) and records each run it performs. Leave it
	// nil for an operator's run, which always converges.
	Schedule *Schedule
}

// defaultResolveExe resolves and validates the durable obot-sentry executable.
func defaultResolveExe() (string, error) {
	exe, err := DefaultExecutable()
	if err != nil {
		return "", err
	}
	if err := validateExecutable(exe); err != nil {
		return "", err
	}
	return exe, nil
}

// defaultProvisionIdentity establishes the shared machine device identity read
// by per-user hook executions and returns its directory. It resolves the
// machine-scoped directory directly — never datadir.IdentityDir's per-user
// fallback — so a privileged install establishes the one shared identity rather
// than a root-owned per-user one, then loads (generating on first run) the
// shared key. The loaded identity itself is not needed here; only that it exists
// (created world-readable by datadir/keystore) for the agent users that later
// submit as this device.
func defaultProvisionIdentity() (string, error) {
	dir, err := datadir.MachineDir()
	if err != nil {
		return "", fmt.Errorf("resolving machine identity directory: %w", err)
	}
	if _, err := identity.Load(dir); err != nil {
		return "", fmt.Errorf("provisioning machine identity in %s: %w", dir, err)
	}
	return dir, nil
}

// New builds an Installer wired to the real platform seams.
func New() *Installer {
	return &Installer{
		GOOS:                runtime.GOOS,
		Privilege:           checkPrivilege,
		ResolveExe:          defaultResolveExe,
		ResolveUsers:        resolveTargetUsers,
		LockHomes:           lockUserHomes,
		LockMachineDirs:     lockMachineDirs,
		ClearSchedule:       clearRunRecord,
		ProvisionIdentity:   defaultProvisionIdentity,
		ResolveDestinations: Destinations,
		OpenLog:             openRunLog,
		Out:                 os.Stdout,
	}
}

// UserList is the result of resolving the machine's users: the accounts whose
// per-user files are converged, and the ones passed over with the reason.
type UserList struct {
	Users   []*TargetUser
	Skipped []SkippedUser
}

// SkippedUser is a local account whose files are not converged, such as one
// whose home has not been set up by a first sign-in yet.
type SkippedUser struct {
	Username string
	Reason   string
}

// Plan is the resolved desired-state model for one invocation: its mode, the
// users whose files are converged, and the ordered destinations to converge.
// Install mode also carries the durable executable and shared machine identity
// directory.
type Plan struct {
	GOOS        string
	Executable  string
	Users       []*TargetUser
	IdentityDir string
	// UsersErr records a failure to list all of the users. Machine-wide
	// destinations, and any users that were found, still converge; the run as a
	// whole reports the failure.
	UsersErr     error
	Destinations []Destination
	Enforce      bool
	Uninstall    bool
}

func (i *Installer) Run(ctx context.Context) error {
	goos := i.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if !supportedPlatform(goos) {
		return fmt.Errorf("obot-sentry %s: %w", i.commandName(), errUnsupportedPlatform)
	}

	privilege := i.Privilege
	if privilege == nil {
		privilege = checkPrivilege
	}
	if err := privilege(); err != nil {
		return err
	}

	log, closeLog := i.openLog()
	defer closeLog()
	err := i.converge(ctx, goos, log)
	if err != nil && !errors.As(err, new(loggedError)) {
		log.Error(i.commandName()+" failed", "error", err)
	}
	return err
}

// loggedError is an error whose details logRun has already recorded, so Run
// does not log it a second time.
type loggedError struct{ err error }

func (e loggedError) Error() string { return e.err.Error() }
func (e loggedError) Unwrap() error { return e.err }

// defaultGroupTimeout is GroupTimeout's default: far longer than converging a
// handful of small files takes, short enough that a stalled group cannot hold
// up the next check.
const defaultGroupTimeout = 2 * time.Minute

// openLog returns the logger for this run and a function that closes it. The
// run log is opened on the first record written, so a periodic tick with
// nothing due never touches it.
func (i *Installer) openLog() (*slog.Logger, func()) {
	if i.Log != nil {
		return i.Log, func() {}
	}
	if i.OpenLog == nil {
		return slog.New(slog.DiscardHandler), func() {}
	}
	errOut := i.ErrOut
	if errOut == nil {
		errOut = os.Stderr
	}
	w := &lazyLog{open: i.OpenLog, errOut: errOut, command: i.commandName()}
	return slog.New(slog.NewTextHandler(w, nil)), w.close
}

// lazyLog opens the run log on its first write. A failure to open it is
// reported once and the records are dropped.
type lazyLog struct {
	open    func() (io.WriteCloser, error)
	errOut  io.Writer
	command string
	w       io.WriteCloser
	failed  bool
}

func (l *lazyLog) Write(p []byte) (int, error) {
	if l.w == nil && !l.failed {
		w, err := l.open()
		if err != nil {
			l.failed = true
			_, _ = fmt.Fprintf(l.errOut, "warning: %s run log unavailable: %v\n", l.command, err)
		} else {
			l.w = w
		}
	}
	if l.w == nil {
		return len(p), nil
	}
	return l.w.Write(p)
}

func (l *lazyLog) close() {
	if l.w != nil {
		_ = l.w.Close()
	}
}

// converge resolves the plan for goos and brings every destination to it.
func (i *Installer) converge(ctx context.Context, goos string, log *slog.Logger) error {
	var exe string
	if !i.Uninstall {
		resolveExe := i.ResolveExe
		if resolveExe == nil {
			resolveExe = defaultResolveExe
		}
		var err error
		exe, err = resolveExe()
		if err != nil {
			return err
		}
	}

	resolveUsers := i.ResolveUsers
	if resolveUsers == nil {
		resolveUsers = resolveTargetUsers
	}
	// A failure to resolve users does not stop the run: the machine-wide hooks
	// do not depend on any user, and a periodic check that gave up on them
	// whenever user discovery failed would leave every agent unprotected. A
	// resolver that could list only some users returns them with the error;
	// those are converged and the failure is still reported.
	users, usersErr := resolveUsers()
	users.Users = slices.Clone(users.Users)
	if i.LockHomes != nil {
		var release func()
		users, release = i.LockHomes(users)
		defer release()
	}
	// Two profile entries can name one directory. Duplicates are dropped only
	// now, after the homes were verified, so a stale entry that failed the
	// check cannot knock out the live one that shares its directory.
	users.Users = dedupeUsers(users.Users)
	sortUsers(users.Users)

	resolveDests := i.ResolveDestinations
	if resolveDests == nil {
		resolveDests = Destinations
	}
	plan := Plan{
		GOOS:         goos,
		Executable:   exe,
		Users:        users.Users,
		UsersErr:     usersErr,
		Destinations: resolveDests(goos),
		Enforce:      i.Enforce,
		Uninstall:    i.Uninstall,
	}

	var record runRecord
	if i.Schedule != nil && !i.Uninstall {
		var due bool
		record, due = i.Schedule.check(plan, log)
		if !due {
			return nil
		}
	}

	if !i.Uninstall {
		provision := i.ProvisionIdentity
		if provision == nil {
			provision = defaultProvisionIdentity
		}
		identityDir, err := provision()
		if err != nil {
			i.recordCheck(record, true, log)
			return err
		}
		plan.IdentityDir = identityDir
	}

	out := i.Out
	if out == nil {
		out = os.Stdout
	}

	results, err := i.convergeGroups(ctx, plan)
	if err != nil {
		return err
	}
	writeSummary(out, plan, users.Skipped, results)
	logRun(ctx, log, i.commandName(), record.Reason, plan, users.Skipped, results)

	failed, liveFailed := 0, 0
	for _, r := range results {
		if r.Status == StatusFailed {
			failed++
			if !r.Retired {
				liveFailed++
			}
		}
	}

	if i.Uninstall && i.ClearSchedule != nil {
		if err := i.ClearSchedule(); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Warn("could not clear the hook check record", "error", err)
		}
	}
	// A failure in a retired destination does not make the next check due
	// sooner: nothing there needs repairing, only removing, and a user's own
	// broken file there would otherwise keep every tick busy.
	i.recordCheck(record, liveFailed > 0 || usersErr != nil, log)

	// A retired destination that could not be cleaned up is reported and
	// logged, but does not fail the run: no hook is missing because of it, and
	// a user's own broken file there would otherwise fail every run forever.
	var errs []error
	if usersErr != nil {
		errs = append(errs, fmt.Errorf("%s could not list all of the machine's users; per-user hooks were converged only for %d found: %w", i.commandName(), len(users.Users), usersErr))
	}
	if liveFailed > 0 || (i.Uninstall && failed > 0) {
		errs = append(errs, fmt.Errorf("%s could not converge %d of %d destinations; see summary", i.commandName(), failed, len(results)))
	}
	if len(errs) == 0 {
		return nil
	}
	return loggedError{errors.Join(errs...)}
}

// recordCheck saves the schedule's record of this check, if this run is a
// periodic one.
func (i *Installer) recordCheck(record runRecord, failed bool, log *slog.Logger) {
	if i.Schedule == nil || i.Uninstall {
		return
	}
	if failed {
		record.Failures++
	} else {
		record.Failures = 0
	}
	record.Failed = record.Failures > 0
	if err := i.Schedule.record(record); err != nil {
		log.Warn("could not record hook check", "error", err)
	}
}

// convergeGroups converges the machine-wide destinations first, one group
// each, and then each user's in turn. Machine-wide hooks protect every user, so they land before any
// one user's files are touched; and each group runs under its own time limit,
// so a file that blocks its reader stalls only its own group.
func (i *Installer) convergeGroups(ctx context.Context, plan Plan) ([]Result, error) {
	var machine, perUser []Destination
	for _, d := range plan.Destinations {
		if d.Scope == ScopeUser {
			perUser = append(perUser, d)
		} else {
			machine = append(machine, d)
		}
	}
	// Each machine-wide destination is its own group, so a folder that blocks
	// (a user holding it open, say) fails only its own agent.
	var groups []Plan
	for _, d := range machine {
		groups = append(groups, withDestinations(plan, []Destination{d}, nil))
	}
	for _, u := range plan.Users {
		groups = append(groups, withDestinations(plan, perUser, []*TargetUser{u}))
	}

	liveFailed := map[liveKey]bool{}
	var results []Result
	for _, g := range groups {
		if len(g.Destinations) == 0 {
			continue
		}
		rs, err := i.convergeGroup(ctx, g, liveFailed)
		if err != nil {
			return nil, err
		}
		results = append(results, rs...)
	}
	return results, nil
}

func withDestinations(plan Plan, dests []Destination, users []*TargetUser) Plan {
	plan.Destinations = dests
	plan.Users = users
	return plan
}

// convergeGroup plans and commits one group under the group time limit,
// recording its failed live destinations in liveFailed for the retired ones of
// later groups. A group that runs out of time is abandoned: every destination in
// it is reported failed, and its context is cancelled so it writes nothing more
// if it ever comes unstuck.
func (i *Installer) convergeGroup(ctx context.Context, g Plan, liveFailed map[liveKey]bool) ([]Result, error) {
	timeout := i.GroupTimeout
	if timeout <= 0 {
		timeout = defaultGroupTimeout
	}
	gctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type outcome struct {
		results []Result
		failed  map[liveKey]bool
		err     error
	}
	done := make(chan outcome, 1)
	local := maps.Clone(liveFailed)
	go func() {
		var lockErrs map[string]error
		if len(g.Users) == 0 && i.LockMachineDirs != nil {
			var release func()
			lockErrs, release = i.LockMachineDirs(g.Destinations, !g.Uninstall)
			defer release()
		}
		changes, err := buildChanges(gctx, g)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		for k := range changes {
			if err := lockErrs[changes[k].dest.Abs]; err != nil && changes[k].dest.Scope == ScopeMachine {
				changes[k].err = err
			}
		}
		results := commitChangesWith(gctx, g, changes, local)
		done <- outcome{results: results, failed: local}
	}()

	select {
	case o := <-done:
		if o.err == nil {
			maps.Copy(liveFailed, o.failed)
			return o.results, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	case <-gctx.Done():
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// A group that finished just as its time ran out still counts.
		select {
		case o := <-done:
			if o.err == nil {
				maps.Copy(liveFailed, o.failed)
				return o.results, nil
			}
		default:
		}
	}
	return timedOutResults(g, timeout, liveFailed), nil
}

// timedOutResults reports every destination of an abandoned group as failed.
func timedOutResults(g Plan, timeout time.Duration, liveFailed map[liveKey]bool) []Result {
	err := fmt.Errorf("timed out after %s; a file in this group may be blocking its reader", timeout)
	targets := []*TargetUser{nil}
	if len(g.Users) > 0 {
		targets = g.Users
	}
	var results []Result
	for _, d := range g.Destinations {
		for _, u := range targets {
			c := plannedChange{dest: d, user: u}
			if abs, perr := d.ResolvePath(u); perr == nil {
				c.absPath = abs
			}
			results = append(results, failedResult(c, err))
			if !d.Retired {
				liveFailed[liveKey{d.Agent, homeOf(u)}] = true
			}
		}
	}
	return results
}

func (i *Installer) commandName() string {
	if i.Uninstall {
		return "hook-uninstall"
	}
	return "hook-install"
}

// plannedChange is one destination's converged bytes for one user (nil for a
// machine-wide destination), decided during preflight and committed afterward.
// write is false when the file is already current; err is set when the
// destination could not be read or merged, and nothing is written for it.
type plannedChange struct {
	dest     Destination
	user     *TargetUser
	absPath  string
	data     []byte
	original []byte
	existed  bool
	status   Status
	dupes    int
	removed  int
	write    bool
	err      error
}

// buildChanges resolves, reads, and merges every destination in memory: each
// machine-wide destination once, and each user-scoped one once per user. A
// destination whose path cannot be resolved, whose file cannot be read safely,
// or whose document is malformed or has an incompatible shape carries that
// error into its change, and is reported failed without stopping the others: one
// user's broken file must not leave every other agent unrepaired. Only a
// cancelled ctx aborts the whole run before any write.
func buildChanges(ctx context.Context, plan Plan) ([]plannedChange, error) {
	changes := make([]plannedChange, 0, len(plan.Destinations)*max(1, len(plan.Users)))
	for _, d := range plan.Destinations {
		targets := []*TargetUser{nil}
		if d.Scope == ScopeUser {
			targets = plan.Users
		}
		for _, u := range targets {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			changes = append(changes, planChange(plan, d, u))
		}
	}
	return changes, nil
}

// planChange reads and merges one destination for one user.
func planChange(plan Plan, d Destination, u *TargetUser) plannedChange {
	c := plannedChange{dest: d, user: u}
	abs, err := d.ResolvePath(u)
	if err != nil {
		c.err = err
		return c
	}
	c.absPath = abs
	existing, existed, err := readConfigFile(d.Scope, u, abs)
	if err != nil {
		c.err = err
		return c
	}
	var outcome mergeOutcome
	if plan.Uninstall || d.Retired {
		outcome, err = removeConfig(d, existing)
	} else {
		outcome, err = mergeConfig(d, existing, plan.Executable, plan.GOOS, plan.Enforce)
	}
	if err != nil {
		c.err = err
		return c
	}
	c.data = outcome.data
	c.original = slices.Clone(existing)
	c.existed = existed
	c.status = outcome.status
	c.dupes = outcome.dupes
	c.removed = outcome.removed
	c.write = outcome.write
	return c
}

// commitChanges writes each planned change that needs writing and returns a
// result per destination, in plan order. A write failure marks that destination
// failed and leaves the others intact. If ctx is cancelled, the not-yet-committed
// destinations are marked failed with the context error so the summary still
// carries a row for every destination.
//
// On install, retired destinations are committed last, and only for an agent
// whose live destinations all converged — the machine-wide ones, and the user's
// own for a retired user file. Removing the old hooks after the new ones failed
// to land would leave an agent that had working hooks with none. Uninstall has
// no replacement to wait for, so it removes from each destination
// independently.
func commitChanges(ctx context.Context, plan Plan, changes []plannedChange) []Result {
	return commitChangesWith(ctx, plan, changes, map[liveKey]bool{})
}

// liveKey names an agent's live destination: the machine-wide one (user "") or
// one user's (their home).
type liveKey struct {
	agent Agent
	user  string
}

// commitChangesWith is commitChanges with the live failures of earlier groups
// in liveFailed, which it extends with this group's.
func commitChangesWith(ctx context.Context, plan Plan, changes []plannedChange, liveFailed map[liveKey]bool) []Result {
	results := make([]Result, len(changes))
	for i, c := range changes {
		if c.dest.Retired {
			continue
		}
		results[i] = commitChange(ctx, c)
		if results[i].Status == StatusFailed {
			liveFailed[liveKey{c.dest.Agent, homeOf(c.user)}] = true
		}
	}
	for i, c := range changes {
		if !c.dest.Retired {
			continue
		}
		replacementFailed := liveFailed[liveKey{c.dest.Agent, ""}] || liveFailed[liveKey{c.dest.Agent, homeOf(c.user)}]
		if !plan.Uninstall && c.err == nil && c.write && replacementFailed {
			results[i] = failedResult(c, errors.New("hooks left in place because their replacement was not installed"))
			continue
		}
		results[i] = commitChange(ctx, c)
	}
	return results
}

// commitChange writes one planned change, if it needs writing, and reports the
// outcome.
func commitChange(ctx context.Context, c plannedChange) Result {
	if c.err != nil {
		return failedResult(c, c.err)
	}
	if err := ctx.Err(); err != nil {
		return failedResult(c, err)
	}
	if c.write {
		current, exists, err := readConfigFile(c.dest.Scope, c.user, c.absPath)
		if err != nil {
			return failedResult(c, fmt.Errorf("re-reading before commit: %w", err))
		}
		if exists != c.existed || !bytes.Equal(current, c.original) {
			return failedResult(c, errors.New("config changed after preflight; refusing to overwrite concurrent edits"))
		}
		// The re-read may have blocked past the group's deadline; an abandoned
		// group must not write.
		if err := ctx.Err(); err != nil {
			return failedResult(c, err)
		}
		if err := commitConfigFile(c.dest.Scope, c.user, c.absPath, c.data); err != nil {
			return failedResult(c, err)
		}
	}
	return Result{
		Agent:             c.dest.Agent,
		Label:             c.dest.Label,
		Scope:             c.dest.Scope,
		Retired:           c.dest.Retired,
		User:              usernameOf(c.user),
		Path:              c.absPath,
		Status:            c.status,
		DuplicatesRemoved: c.dupes,
		HooksRemoved:      c.removed,
	}
}

// failedResult reports a change that was not committed. It carries no counts:
// nothing was removed or deduplicated on disk.
func failedResult(c plannedChange, err error) Result {
	return Result{
		Agent:   c.dest.Agent,
		Label:   c.dest.Label,
		Scope:   c.dest.Scope,
		Retired: c.dest.Retired,
		User:    usernameOf(c.user),
		Path:    c.absPath,
		Status:  StatusFailed,
		Err:     err,
	}
}

func homeOf(u *TargetUser) string {
	if u == nil {
		return ""
	}
	return u.HomeDir
}

func usernameOf(u *TargetUser) string {
	if u == nil {
		return ""
	}
	return u.Username
}

// writeSummary prints the run header (the users converged and the shared identity directory)
// followed by the per-destination result table. It emits only paths and
// statuses — never config contents or credentials.
func writeSummary(w io.Writer, plan Plan, skipped []SkippedUser, results []Result) {
	command := "hook-install"
	if plan.Uninstall {
		command = "hook-uninstall"
	}
	_, _ = fmt.Fprintf(w, "obot-sentry %s (%s)\n", command, plan.GOOS)
	switch {
	case plan.UsersErr != nil && len(plan.Users) == 0:
		_, _ = fmt.Fprintf(w, "Users: could not be resolved: %v\n", plan.UsersErr)
	case len(plan.Users) == 0:
		_, _ = fmt.Fprintln(w, "Users: none (machine-wide hooks only)")
	default:
		names := make([]string, 0, len(plan.Users))
		for _, u := range plan.Users {
			names = append(names, fmt.Sprintf("%s (%s)", u.Username, u.HomeDir))
		}
		_, _ = fmt.Fprintf(w, "Users: %s\n", strings.Join(names, ", "))
		if plan.UsersErr != nil {
			_, _ = fmt.Fprintf(w, "Users: not all could be listed: %v\n", plan.UsersErr)
		}
	}
	for _, sk := range skipped {
		_, _ = fmt.Fprintf(w, "Skipped user %s: %s\n", sk.Username, sk.Reason)
	}
	if plan.IdentityDir != "" {
		_, _ = fmt.Fprintf(w, "Machine identity: %s\n", plan.IdentityDir)
	}
	_, _ = fmt.Fprintln(w)
	formatSummary(w, plan.Executable, results, plan.Uninstall)
}

// restartReminder is appended to successful output so the operator reloads every
// agent and the hook changes take effect.
const restartReminder = "Restart or reload Claude Code, Codex, Visual Studio Code, Cursor, and Kiro to apply the hook changes."

// FormatSummary renders the deterministic per-destination result summary. It
// reports the resolved executable and one line per destination with its status
// and change counts, but never config contents or credentials.
func FormatSummary(w io.Writer, exe string, results []Result) {
	formatSummary(w, exe, results, false)
}

func formatSummary(w io.Writer, exe string, results []Result, uninstall bool) {
	if !uninstall {
		_, _ = fmt.Fprintf(w, "Executable: %s\n\n", exe)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "AGENT\tUSER\tSTATUS\tPATH\tDETAIL")
	for _, r := range results {
		label := r.Label
		if label == "" {
			label = r.Agent.DisplayName()
		}
		user := r.User
		if user == "" {
			user = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", label, user, r.Status, r.Path, resultDetail(r))
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, restartReminder)
}

// resultDetail renders the non-secret detail column for a result: an error
// summary for failures, a deduplication count when entries were collapsed, or a
// dash.
func resultDetail(r Result) string {
	if r.Status == StatusFailed && r.Err != nil {
		return r.Err.Error()
	}
	var details []string
	if r.HooksRemoved == 1 {
		details = append(details, "1 hook removed")
	} else if r.HooksRemoved > 1 {
		details = append(details, fmt.Sprintf("%d hooks removed", r.HooksRemoved))
	}
	if r.DuplicatesRemoved == 1 {
		details = append(details, "1 duplicate removed")
	} else if r.DuplicatesRemoved > 1 {
		details = append(details, fmt.Sprintf("%d duplicates removed", r.DuplicatesRemoved))
	}
	if len(details) == 0 {
		return "-"
	}
	return strings.Join(details, ", ")
}
