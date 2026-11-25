package hookinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"runtime"
	"runtime/debug"
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
// injectable seam so platform discovery, command generation, and the filesystem
// commit can be exercised independently in tests without root, a specific OS, or
// a real obot-sentry binary on disk.
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
	// ResolveUsers lists every local user, signed in or not; defaults to the
	// platform resolver.
	ResolveUsers func() (UserSet, error)
	// ProvisionIdentity establishes the shared machine device identity read by
	// per-user hook executions and returns the resolved machine directory;
	// defaults to defaultProvisionIdentity (datadir.MachineDir + identity.Load).
	ProvisionIdentity func() (string, error)
	// ResolveDestinations returns the managed destinations for a GOOS; defaults
	// to Destinations. Tests inject a set rooted under a temporary directory so
	// the anchored commit can be exercised without writing to real system paths.
	ResolveDestinations func(goos string) []Destination
	// RunDir resolves the directory holding the run log and schedule state; nil
	// disables both.
	RunDir func() (string, error)
	// Enforce installs the pre-tool enforcement hooks in addition to the
	// post-tool audit hooks. It is resolved by the command layer from the
	// --enforce flag, the environment, and the MDM store, in that order.
	Enforce bool
	// Uninstall removes every marker-owned hook instead of installing desired
	// hooks. Supporting settings without ownership markers remain unchanged.
	Uninstall bool
	// IfDue skips the run unless decideDue says it is due.
	IfDue    bool
	Interval time.Duration
	Now      func() time.Time
	BootTime func() (time.Time, error)
	// Out receives operator-facing output; defaults to os.Stdout.
	Out io.Writer
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
		ProvisionIdentity:   defaultProvisionIdentity,
		ResolveDestinations: Destinations,
		RunDir:              DefaultRunDir,
		Now:                 time.Now,
		BootTime:            bootTime,
		Out:                 os.Stdout,
	}
}

// Plan is the resolved desired-state model for one invocation: its mode, the
// users whose files are converged, and the ordered destinations to converge.
// Install mode also carries the durable executable and shared machine identity
// directory.
type Plan struct {
	GOOS         string
	Executable   string
	Users        []*TargetUser
	Skipped      []SkippedUser
	IdentityDir  string
	Destinations []Destination
	Enforce      bool
	Uninstall    bool

	// read is copied from readConfig on the calling goroutine, so a unit
	// abandoned past its deadline never reads the package variable.
	read configReader
}

type configReader func(scope Scope, u *TargetUser, absPath string) ([]byte, bool, error)

func (p Plan) reader() configReader {
	if p.read != nil {
		return p.read
	}
	return readConfigFile
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

	out := i.Out
	if out == nil {
		out = os.Stdout
	}
	now := i.Now
	if now == nil {
		now = time.Now
	}
	startedAt := now()

	runDir, log, closeLog := i.openRunDir(out)
	defer closeLog()
	log = log.With("command", i.commandName())

	err := i.run(ctx, goos, out, log, runDir, startedAt)
	if err != nil {
		log.Error("hook convergence failed", "err", logErr(err))
		logPanicStacks(log, err, nil)
	}
	return err
}

// openRunDir resolves the run directory and opens the run log. Neither is
// required: on failure the run continues unlogged and without schedule state.
func (i *Installer) openRunDir(out io.Writer) (string, *slog.Logger, func()) {
	if i.RunDir == nil {
		return "", discardLogger(), func() {}
	}
	dir, err := i.RunDir()
	if err != nil {
		_, _ = fmt.Fprintf(out, "warning: hook-install run directory unavailable; not logging or recording this run: %v\n", err)
		return "", discardLogger(), func() {}
	}
	log, closer, err := openRunLog(dir)
	if err != nil {
		_, _ = fmt.Fprintf(out, "warning: not logging this run: %v\n", err)
		return dir, discardLogger(), func() {}
	}
	return dir, log, func() { _ = closer.Close() }
}

func (i *Installer) run(ctx context.Context, goos string, out io.Writer, log *slog.Logger, runDir string, startedAt time.Time) (err error) {
	resolveUsers := i.ResolveUsers
	if resolveUsers == nil {
		resolveUsers = resolveTargetUsers
	}
	// A failed listing must not hold back machine policy: converge it with no
	// users, then still fail the run so it is retried and not recorded.
	users, listErr := resolveUsers()
	// incomplete is any failure to work out which users to converge. The run
	// still converges what it can, then fails so it is retried.
	var incomplete error
	if listErr != nil {
		users = UserSet{}
		log.Error("listing users failed; converging machine-wide hooks only", "err", logErr(listErr))
		_, _ = fmt.Fprintf(out, "warning: listing users failed; converging machine-wide hooks only: %v\n", listErr)
		incomplete = fmt.Errorf("listing users: %w", listErr)
	}
	for _, s := range users.Skipped {
		// An ordinary skip (no home, home not owned) is expected; a panic
		// while resolving the user is a bug and must not count as success.
		var pe *panicError
		if errors.As(s.Err, &pe) {
			incomplete = errors.Join(incomplete, fmt.Errorf("resolving user %s: %w", s.Name, s.Err))
		}
	}
	if incomplete != nil {
		defer func() { err = errors.Join(incomplete, err) }()
	}

	if i.Uninstall && runDir != "" {
		// Otherwise a same-version reinstall would wait out the interval before
		// reinstalling the hooks removed here.
		if err := removeScheduleState(runDir); err != nil {
			log.Warn("clearing schedule state failed", "err", logErr(err))
		}
	}

	inputs := newScheduleInputs(i.Enforce, users.Users)
	inputs.HomesUnknown = listErr != nil
	prior := i.loadScheduleState(runDir, log)
	if i.IfDue {
		decision := decideDue(prior, inputs, i.Interval, startedAt, i.bootTime())
		if !decision.Due {
			_, _ = fmt.Fprintf(out, "obot-sentry %s: not due (%s); skipping\n", i.commandName(), decision.Reason)
			return nil
		}
		log.Info("hook convergence due", "reason", decision.Reason)
	}
	if runDir != "" && !i.Uninstall {
		// Every install run that gets this far, scheduled or manual, records
		// its outcome: any of them can rewrite files with its inputs, so the
		// next poll has to compare against them. A failed run is marked so
		// that the next poll retries it.
		//
		// The run is marked failed before it converges and the outcome
		// overwrites that, so a run that dies partway (killed by the
		// scheduler's time limit or Stop-ScheduledTask, or crashed) leaves a
		// failed record rather than the previous success.
		if markErr := saveFailedRunState(runDir, prior, inputs); markErr != nil {
			log.Warn("recording run start failed", "err", logErr(markErr))
		}
		defer func() {
			var saveErr error
			if err == nil && incomplete == nil {
				saveErr = saveScheduleState(runDir, inputs, startedAt)
			} else {
				saveErr = saveFailedRunState(runDir, prior, inputs)
			}
			if saveErr != nil {
				log.Warn("recording schedule state failed", "err", logErr(saveErr))
				_, _ = fmt.Fprintf(out, "warning: recording schedule state: %v\n", saveErr)
			}
		}()
	}

	log.Info("hook convergence started", "enforce", i.Enforce, "users", userNames(users.Users), "skipped_users", len(users.Skipped))
	for _, s := range users.Skipped {
		log.Warn("skipping user", "user", s.Name, "err", logErr(s.Err))
	}

	var exe string
	if !i.Uninstall {
		resolveExe := i.ResolveExe
		if resolveExe == nil {
			resolveExe = defaultResolveExe
		}
		exe, err = resolveExe()
		if err != nil {
			return err
		}
	}

	var identityDir string
	if !i.Uninstall {
		provision := i.ProvisionIdentity
		if provision == nil {
			provision = defaultProvisionIdentity
		}
		identityDir, err = provision()
		if err != nil {
			return err
		}
	}

	resolveDests := i.ResolveDestinations
	if resolveDests == nil {
		resolveDests = Destinations
	}
	plan := Plan{
		GOOS:         goos,
		Executable:   exe,
		Users:        users.Users,
		Skipped:      users.Skipped,
		IdentityDir:  identityDir,
		Destinations: resolveDests(goos),
		Enforce:      i.Enforce,
		Uninstall:    i.Uninstall,
	}

	results, err := converge(ctx, plan)
	if err != nil {
		return err
	}
	writeSummary(out, plan, results)
	logResults(log, results, startedAt)

	failed := 0
	for _, r := range results {
		if r.Status == StatusFailed {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%s could not converge %d of %d destinations; see summary", i.commandName(), failed, len(results))
	}
	return nil
}

// loadScheduleState returns the recorded state, or nil when there is no run
// directory or the state is absent or untrusted, which makes a poll due.
func (i *Installer) loadScheduleState(runDir string, log *slog.Logger) *scheduleState {
	if runDir == "" {
		return nil
	}
	state, err := loadScheduleState(runDir)
	if err != nil {
		log.Warn("ignoring schedule state", "err", logErr(err))
	}
	return state
}

// bootTime returns the system boot time, or zero when it is unknown.
func (i *Installer) bootTime() time.Time {
	if i.BootTime == nil {
		return time.Time{}
	}
	t, err := i.BootTime()
	if err != nil {
		return time.Time{}
	}
	return t
}

// logResults logs each destination that changed or failed; unchanged ones are
// only counted, to keep steady-state runs to a few lines.
func logResults(log *slog.Logger, results []Result, startedAt time.Time) {
	var changed, unchanged, failed int
	// A panic in a user's unit fails every row of that unit with the same
	// error; log its stack once.
	seen := map[*panicError]bool{}
	for _, r := range results {
		attrs := []any{"agent", r.Label, "path", r.Path}
		if r.User != "" {
			attrs = append(attrs, "user", r.User)
		}
		switch r.Status {
		case StatusUnchanged:
			unchanged++
		case StatusFailed:
			failed++
			log.Error("hook destination failed", append(attrs, "err", logErr(r.Err))...)
			logPanicStacks(log, r.Err, seen)
		default:
			changed++
			log.Info("hook destination changed", append(attrs,
				"status", string(r.Status),
				"duplicates_removed", r.DuplicatesRemoved,
				"hooks_removed", r.HooksRemoved)...)
		}
	}
	log.Info("hook convergence finished",
		"changed", changed, "unchanged", unchanged, "failed", failed,
		"duration", time.Since(startedAt).Round(time.Millisecond).String())
}

func userNames(users []*TargetUser) []string {
	names := make([]string, 0, len(users))
	for _, u := range users {
		names = append(names, u.Username)
	}
	return names
}

func (i *Installer) commandName() string {
	if i.Uninstall {
		return "hook-uninstall"
	}
	return "hook-install"
}

// plannedChange is one destination's converged bytes, decided during preflight
// and committed afterward. write is false when the file is already current. err
// is set when preflight failed for the destination's user, in which case
// nothing is written for it.
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

// readConfig is a seam tests replace to simulate a stalled read.
var readConfig configReader = readConfigFile

// unitTimeout bounds one unit (the machine scope, or one user) from first read
// to last write. Reads don't block on FIFOs and are size-capped, but an open
// can still stall, for example on a home whose network volume is unreachable.
var unitTimeout = 2 * time.Minute

// converge commits the machine-scoped destinations before reading any user's
// files, then each user independently, so no user's files can hold back
// machine policy or another user. A machine-scope preflight failure aborts the
// run before any write.
func converge(ctx context.Context, plan Plan) ([]Result, error) {
	if plan.read == nil {
		plan.read = readConfig
	}
	var machine, perUser []Destination
	for _, d := range plan.Destinations {
		if d.Scope == ScopeUser {
			perUser = append(perUser, d)
		} else {
			machine = append(machine, d)
		}
	}

	machinePlan := plan
	machinePlan.Destinations, machinePlan.Users = machine, nil
	results, machineFailed, err := runUnit(ctx, machinePlan, nil, map[liveKey]bool{})
	if err != nil {
		return nil, err
	}

	for _, u := range plan.Users {
		userPlan := plan
		userPlan.Destinations, userPlan.Users = perUser, []*TargetUser{u}
		userResults, _, err := runUnit(ctx, userPlan, u, machineFailed)
		if err != nil {
			// A cancelled run or a panic in the unit gets here.
			userResults = failedRows(perUser, u, err)
		}
		results = append(results, userResults...)
	}
	return results, nil
}

// runUnit plans and commits one unit (u is nil for the machine scope) under
// unitTimeout. A timeout is reported as failed rows rather than an error, and
// the unit's goroutine is abandoned with its context cancelled. priorFailed is
// copied, never written, so an abandoned unit shares no state with later ones.
func runUnit(ctx context.Context, unitPlan Plan, u *TargetUser, priorFailed map[liveKey]bool) ([]Result, map[liveKey]bool, error) {
	unitCtx, cancel := context.WithTimeout(ctx, unitTimeout)
	defer cancel()

	type outcome struct {
		results []Result
		failed  map[liveKey]bool
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		// A panic in a goroutine kills the process, and with it every unit
		// after this one; report it as this unit's error instead.
		defer func() {
			if r := recover(); r != nil {
				done <- outcome{err: recovered(r)}
			}
		}()
		changes, err := buildChanges(unitCtx, unitPlan)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		failed := maps.Clone(priorFailed)
		done <- outcome{results: commitChangesWith(unitCtx, unitPlan, changes, failed), failed: failed}
	}()

	var o outcome
	select {
	case o = <-done:
	case <-unitCtx.Done():
		select {
		case o = <-done: // finished just as the deadline passed
		default:
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			rows, failed := timedOutUnit(unitPlan.Destinations, u, priorFailed)
			return rows, failed, nil
		}
	}
	if o.err != nil {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if errors.Is(o.err, context.DeadlineExceeded) {
			// The unit saw its own deadline before the select above did.
			rows, failed := timedOutUnit(unitPlan.Destinations, u, priorFailed)
			return rows, failed, nil
		}
		return nil, nil, o.err
	}
	return o.results, o.failed, nil
}

// timedOutUnit marks the unit's destinations as live failures too, so retired
// destinations waiting on them are held back.
func timedOutUnit(dests []Destination, u *TargetUser, priorFailed map[liveKey]bool) ([]Result, map[liveKey]bool) {
	who := "machine-wide hooks"
	if u != nil {
		who = u.Username + "'s hooks"
	}
	timeout := fmt.Errorf("converging %s took longer than %s; abandoned", who, unitTimeout)
	failed := maps.Clone(priorFailed)
	for _, d := range dests {
		failed[liveKey{d.Agent, homeOf(u)}] = true
	}
	return failedRows(dests, u, timeout), failed
}

func failedRows(dests []Destination, u *TargetUser, err error) []Result {
	rows := make([]Result, 0, len(dests))
	for _, d := range dests {
		abs, _ := d.ResolvePath(u)
		rows = append(rows, failedResult(plannedChange{dest: d, user: u, absPath: abs}, err))
	}
	return rows
}

// buildChanges reads and merges every destination in memory. A machine
// destination that fails, or a cancelled ctx, returns an error; a user
// destination that fails gets a failed row of its own.
func buildChanges(ctx context.Context, plan Plan) ([]plannedChange, error) {
	var machine, perUser []Destination
	for _, d := range plan.Destinations {
		if d.Scope == ScopeUser {
			perUser = append(perUser, d)
		} else {
			machine = append(machine, d)
		}
	}

	changes, err := planDestinations(ctx, plan, machine, nil)
	if err != nil {
		return nil, err
	}
	for _, u := range plan.Users {
		// Each of a user's destinations is planned on its own: one file the
		// user can make unreadable or refused (a hard link, a malformed
		// settings.json) must not stop their other hooks, the Kiro
		// enforcement hook included, from being installed or repaired.
		for _, d := range perUser {
			c, err := planDestination(ctx, plan, d, u)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
				abs, _ := d.ResolvePath(u)
				c = plannedChange{dest: d, user: u, absPath: abs, err: err}
			}
			changes = append(changes, c)
		}
	}
	return changes, nil
}

// planDestinations preflights dests for one user (nil for machine scope),
// failing on the first destination that cannot be planned.
func planDestinations(ctx context.Context, plan Plan, dests []Destination, u *TargetUser) ([]plannedChange, error) {
	changes := make([]plannedChange, 0, len(dests))
	for _, d := range dests {
		c, err := planDestination(ctx, plan, d, u)
		if err != nil {
			return nil, err
		}
		changes = append(changes, c)
	}
	return changes, nil
}

func planDestination(ctx context.Context, plan Plan, d Destination, u *TargetUser) (plannedChange, error) {
	if err := ctx.Err(); err != nil {
		return plannedChange{}, err
	}
	abs, err := d.ResolvePath(u)
	if err != nil {
		return plannedChange{}, err
	}
	existing, existed, err := plan.reader()(d.Scope, u, abs)
	if err != nil {
		return plannedChange{}, fmt.Errorf("%s (%s): %w", d.Label, abs, err)
	}
	var outcome mergeOutcome
	if plan.Uninstall || d.Retired {
		outcome, err = removeConfig(d, existing)
	} else {
		outcome, err = mergeConfig(d, existing, plan.Executable, plan.GOOS, plan.Enforce)
	}
	if err != nil {
		return plannedChange{}, fmt.Errorf("%s (%s): %w", d.Label, abs, err)
	}
	return plannedChange{
		dest:     d,
		user:     u,
		absPath:  abs,
		data:     outcome.data,
		original: slices.Clone(existing),
		existed:  existed,
		status:   outcome.status,
		dupes:    outcome.dupes,
		removed:  outcome.removed,
		write:    outcome.write,
	}, nil
}

// liveKey identifies an agent's live destinations: machine-scoped (home "") or
// one user's.
type liveKey struct {
	agent Agent
	home  string
}

// commitChanges writes each planned change that needs writing and returns a
// result per destination, in plan order. A write failure marks that destination
// failed and leaves the others intact. If ctx is cancelled, the not-yet-committed
// destinations are marked failed with the context error so the summary still
// carries a row for every destination.
//
// On install, retired destinations are committed last, and only when the
// agent's live destinations — machine-wide and the same user's — all
// converged. Removing the old hooks after the new ones failed to land would
// leave an agent that had working hooks with none. Uninstall has no
// replacement to wait for, so it removes from each destination independently.
func commitChanges(ctx context.Context, plan Plan, changes []plannedChange) []Result {
	return commitChangesWith(ctx, plan, changes, map[liveKey]bool{})
}

// commitChangesWith is commitChanges seeded with an earlier unit's live
// failures; it adds this unit's to liveFailed.
func commitChangesWith(ctx context.Context, plan Plan, changes []plannedChange, liveFailed map[liveKey]bool) []Result {
	results := make([]Result, len(changes))
	for i, c := range changes {
		if c.dest.Retired {
			continue
		}
		results[i] = commitChange(ctx, plan.reader(), c)
		if results[i].Status == StatusFailed {
			liveFailed[liveKey{c.dest.Agent, homeOf(c.user)}] = true
		}
	}
	for i, c := range changes {
		if !c.dest.Retired {
			continue
		}
		held := liveFailed[liveKey{c.dest.Agent, ""}] || liveFailed[liveKey{c.dest.Agent, homeOf(c.user)}]
		if !plan.Uninstall && c.err == nil && c.write && held {
			results[i] = failedResult(c, errors.New("hooks left in place because their replacement was not installed"))
			continue
		}
		results[i] = commitChange(ctx, plan.reader(), c)
	}
	return results
}

// commitChange writes one planned change, if it needs writing, and reports the
// outcome.
func commitChange(ctx context.Context, read configReader, c plannedChange) Result {
	if c.err != nil {
		return failedResult(c, c.err)
	}
	if err := ctx.Err(); err != nil {
		return failedResult(c, err)
	}
	if c.write {
		current, exists, err := read(c.dest.Scope, c.user, c.absPath)
		if err != nil {
			return failedResult(c, fmt.Errorf("re-reading before commit: %w", err))
		}
		if exists != c.existed || !bytes.Equal(current, c.original) {
			return failedResult(c, errors.New("config changed after preflight; refusing to overwrite concurrent edits"))
		}
		// The re-read may have stalled past the deadline; an abandoned unit
		// must not write.
		if err := ctx.Err(); err != nil {
			return failedResult(c, err)
		}
		if err := commitConfigFile(ctx, c.dest.Scope, c.user, c.absPath, c.data); err != nil {
			return failedResult(c, err)
		}
	}
	return Result{
		Agent:             c.dest.Agent,
		Label:             c.dest.Label,
		Scope:             c.dest.Scope,
		User:              userName(c.user),
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
		Agent:  c.dest.Agent,
		Label:  c.dest.Label,
		Scope:  c.dest.Scope,
		User:   userName(c.user),
		Path:   c.absPath,
		Status: StatusFailed,
		Err:    err,
	}
}

func homeOf(u *TargetUser) string {
	if u == nil {
		return ""
	}
	return u.HomeDir
}

func userName(u *TargetUser) string {
	if u == nil {
		return ""
	}
	return u.Username
}

// writeSummary prints the run header (users and shared identity directory)
// followed by the per-destination result table. It emits only paths and
// statuses — never config contents or credentials.
func writeSummary(w io.Writer, plan Plan, results []Result) {
	command := "hook-install"
	if plan.Uninstall {
		command = "hook-uninstall"
	}
	_, _ = fmt.Fprintf(w, "obot-sentry %s (%s)\n", command, plan.GOOS)
	if len(plan.Users) == 0 {
		_, _ = fmt.Fprintln(w, "Users: none found; converging machine-wide hooks only")
	}
	for _, u := range plan.Users {
		_, _ = fmt.Fprintf(w, "User: %s (%s)\n", u.Username, u.HomeDir)
	}
	for _, s := range plan.Skipped {
		_, _ = fmt.Fprintf(w, "Skipped user: %s: %v\n", s.Name, s.Err)
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

// maxLoggedErrBytes caps logged error text. Errors can quote a user's own
// config (a JSON key, say), and a user must not be able to flood the log.
const maxLoggedErrBytes = 512

func logErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) <= maxLoggedErrBytes {
		return s
	}
	return strings.ToValidUTF8(s[:maxLoggedErrBytes], "") + fmt.Sprintf("… (%d bytes truncated)", len(s)-maxLoggedErrBytes)
}

// panicError is a panic recovered in a goroutine. Its message is shown with
// the result; the stack is logged separately (see logPanicStacks) so a bug that
// is caught still leaves enough to diagnose it.
type panicError struct {
	value any
	stack []byte
}

func (e *panicError) Error() string { return fmt.Sprintf("internal error: %v", e.value) }

func recovered(r any) error { return &panicError{value: r, stack: debug.Stack()} }

// logPanicStacks logs the stack of every recovered panic in err, including
// inside joined errors, skipping any already in seen (nil means none).
// Run logs those in its returned error, which covers a panic in the machine
// unit or while resolving users; logResults covers those in user units.
func logPanicStacks(log *slog.Logger, err error, seen map[*panicError]bool) {
	if err == nil {
		return
	}
	if pe, ok := err.(*panicError); ok {
		if seen == nil || !seen[pe] {
			log.Error("internal error stack", "stack", string(pe.stack))
			if seen != nil {
				seen[pe] = true
			}
		}
		return
	}
	switch u := err.(type) {
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			logPanicStacks(log, e, seen)
		}
	case interface{ Unwrap() error }:
		logPanicStacks(log, u.Unwrap(), seen)
	}
}
