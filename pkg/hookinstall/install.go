package hookinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/obot-platform/obot-sentry/pkg/datadir"
	"github.com/obot-platform/obot-sentry/pkg/identity"
)

// errUnsupportedPlatform is returned for any GOOS other than darwin or windows.
// It is a plain error so the CLI maps it to the normal runtime exit code:
// installing hooks is local configuration, not a deployment-config failure.
var errUnsupportedPlatform = errors.New("obot-sentry hook-install is only supported on macOS and Windows")

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
	// ResolveUser resolves the active console user whose per-user files are
	// converged; defaults to the platform resolver.
	ResolveUser func() (*TargetUser, error)
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
		ResolveUser:         resolveTargetUser,
		ProvisionIdentity:   defaultProvisionIdentity,
		ResolveDestinations: Destinations,
		Out:                 os.Stdout,
	}
}

// Plan is the resolved desired-state model for one invocation: its mode, the
// active console user, and the ordered destinations to converge. Install mode
// also carries the durable executable and shared machine identity directory.
type Plan struct {
	GOOS         string
	Executable   string
	User         *TargetUser
	IdentityDir  string
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
		return errUnsupportedPlatform
	}

	privilege := i.Privilege
	if privilege == nil {
		privilege = checkPrivilege
	}
	if err := privilege(); err != nil {
		return err
	}

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

	resolveUser := i.ResolveUser
	if resolveUser == nil {
		resolveUser = resolveTargetUser
	}
	user, err := resolveUser()
	if err != nil {
		return err
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

	out := i.Out
	if out == nil {
		out = os.Stdout
	}

	resolveDests := i.ResolveDestinations
	if resolveDests == nil {
		resolveDests = Destinations
	}
	plan := Plan{
		GOOS:         goos,
		Executable:   exe,
		User:         user,
		IdentityDir:  identityDir,
		Destinations: resolveDests(goos),
		Enforce:      i.Enforce,
		Uninstall:    i.Uninstall,
	}

	// Preflight: read and merge every destination in memory. Any read or parse
	// failure aborts before a single file is written.
	changes, err := buildChanges(ctx, plan)
	if err != nil {
		return err
	}

	// Commit: write the changed files. A per-destination failure is captured in
	// its result rather than aborting the rest.
	results := commitChanges(ctx, plan, changes)
	writeSummary(out, plan, results)

	failed := 0
	for _, r := range results {
		if r.Status == StatusFailed {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("hook-install could not converge %d of %d destinations; see summary", failed, len(results))
	}
	return nil
}

// plannedChange is one destination's converged bytes, decided during preflight
// and committed afterward. write is false when the file is already current.
type plannedChange struct {
	dest     Destination
	absPath  string
	data     []byte
	original []byte
	existed  bool
	status   Status
	dupes    int
	removed  int
	write    bool
}

// buildChanges resolves, reads, and merges every destination in memory. It
// returns an error (aborting the whole run before any write) when a destination
// path cannot be resolved, a present file cannot be read safely, an existing
// document is malformed or has an incompatible shape, or ctx is cancelled.
func buildChanges(ctx context.Context, plan Plan) ([]plannedChange, error) {
	home := ""
	if plan.User != nil {
		home = plan.User.HomeDir
	}
	changes := make([]plannedChange, 0, len(plan.Destinations))
	for _, d := range plan.Destinations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		abs, err := d.ResolvePath(plan.User)
		if err != nil {
			return nil, err
		}
		existing, existed, err := readConfigFile(d.Scope, home, abs)
		if err != nil {
			return nil, fmt.Errorf("%s (%s): %w", d.Label, abs, err)
		}
		var outcome mergeOutcome
		if plan.Uninstall {
			outcome, err = removeConfig(d, existing)
		} else {
			outcome, err = mergeConfig(d, existing, plan.Executable, plan.GOOS, plan.Enforce)
		}
		if err != nil {
			return nil, fmt.Errorf("%s (%s): %w", d.Label, abs, err)
		}
		changes = append(changes, plannedChange{
			dest:     d,
			absPath:  abs,
			data:     outcome.data,
			original: slices.Clone(existing),
			existed:  existed,
			status:   outcome.status,
			dupes:    outcome.dupes,
			removed:  outcome.removed,
			write:    outcome.write,
		})
	}
	return changes, nil
}

// commitChanges writes each planned change that needs writing and returns a
// result per destination. A write failure marks that destination failed and
// leaves the others intact. If ctx is cancelled, the not-yet-committed
// destinations are marked failed with the context error so the summary still
// carries a row for every destination.
func commitChanges(ctx context.Context, plan Plan, changes []plannedChange) []Result {
	results := make([]Result, 0, len(changes))
	for _, c := range changes {
		r := Result{
			Agent:             c.dest.Agent,
			Label:             c.dest.Label,
			Scope:             c.dest.Scope,
			Path:              c.absPath,
			Status:            c.status,
			DuplicatesRemoved: c.dupes,
			HooksRemoved:      c.removed,
		}
		if err := ctx.Err(); err != nil {
			r.Status = StatusFailed
			r.Err = err
			r.DuplicatesRemoved = 0
			r.HooksRemoved = 0
			results = append(results, r)
			continue
		}
		if c.write {
			current, exists, err := readConfigFile(c.dest.Scope, homeOf(plan.User), c.absPath)
			if err != nil {
				r.Status = StatusFailed
				r.Err = fmt.Errorf("re-reading before commit: %w", err)
				r.DuplicatesRemoved = 0
				r.HooksRemoved = 0
				results = append(results, r)
				continue
			}
			if exists != c.existed || !bytes.Equal(current, c.original) {
				r.Status = StatusFailed
				r.Err = errors.New("config changed after preflight; refusing to overwrite concurrent edits")
				r.DuplicatesRemoved = 0
				r.HooksRemoved = 0
				results = append(results, r)
				continue
			}
			if err := commitConfigFile(c.dest.Scope, plan.User, c.absPath, c.data); err != nil {
				r.Status = StatusFailed
				r.Err = err
				r.DuplicatesRemoved = 0
				r.HooksRemoved = 0
			}
		}
		results = append(results, r)
	}
	return results
}

func homeOf(u *TargetUser) string {
	if u == nil {
		return ""
	}
	return u.HomeDir
}

// writeSummary prints the run header (active user and shared identity directory)
// followed by the per-destination result table. It emits only paths and
// statuses — never config contents or credentials.
func writeSummary(w io.Writer, plan Plan, results []Result) {
	command := "hook-install"
	if plan.Uninstall {
		command += " --uninstall"
	}
	_, _ = fmt.Fprintf(w, "obot-sentry %s (%s)\n", command, plan.GOOS)
	if plan.User != nil {
		_, _ = fmt.Fprintf(w, "Active user: %s (%s)\n", plan.User.Username, plan.User.HomeDir)
	}
	if plan.IdentityDir != "" {
		_, _ = fmt.Fprintf(w, "Machine identity: %s\n", plan.IdentityDir)
	}
	_, _ = fmt.Fprintln(w)
	formatSummary(w, plan.Executable, results, plan.Uninstall)
}

// restartReminder is appended to successful output so the operator reloads every
// agent and the hook changes take effect.
const restartReminder = "Restart or reload Claude Code, Codex, Visual Studio Code, and Cursor to apply the hook changes."

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
	_, _ = fmt.Fprintln(tw, "AGENT\tSTATUS\tPATH\tDETAIL")
	for _, r := range results {
		label := r.Label
		if label == "" {
			label = r.Agent.DisplayName()
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", label, r.Status, r.Path, resultDetail(r))
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
