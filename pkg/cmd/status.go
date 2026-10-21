package cmd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/obot-platform/obot-sentry/pkg/agent"
	"github.com/obot-platform/obot-sentry/pkg/datadir"
	"github.com/obot-platform/obot-sentry/pkg/mdmconfig"
	"github.com/obot-platform/obot-sentry/pkg/state"
	"github.com/obot-platform/obot-sentry/pkg/version"
)

// Status reports what this device's agent sees: its configuration, its
// enrollment, and the current user's last scan.
type Status struct {
	ConfigFlags

	// resolveDirs locates the state status reads and writes. It is wired by
	// tests to keep them out of the host's real identity and state; when unset
	// it falls back to defaultStatusDirs.
	resolveDirs func() (statusDirs, error)
}

// statusDirs are the directories holding enrollment state, the device
// identity, and the per-user scan state.
type statusDirs struct {
	data     string
	identity string
	cache    string
}

func (s *Status) Customize(cmd *cobra.Command) {
	cmd.Use = "status"
	cmd.Short = "Check this device's configuration, enrollment, and last scan"
	cmd.Long = "Status prints the deployment configuration obot-sentry resolves, whether this device is " +
		"enrolled, and the outcome of the current user's last scan. A device that isn't enrolled yet is " +
		"enrolled now, exactly as the next scan would, so a wrong server URL or an enrollment key Obot " +
		"rejects shows up right away. Exits 2 for a configuration problem and 1 when enrollment fails " +
		"for any other reason."
	cmd.Args = cobra.NoArgs
}

func (s *Status) Run(cmd *cobra.Command, _ []string) error {
	out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	row := func(label, format string, args ...any) {
		_, _ = fmt.Fprintf(out, "%s:\t%s\n", label, fmt.Sprintf(format, args...))
	}

	problem := s.report(cmd.Context(), row)
	if err := out.Flush(); err != nil {
		return err
	}
	// Return a nil error, not a nil *ExitCodeError.
	if problem != nil {
		return problem
	}
	return nil
}

// report prints every row it can and returns the problem that sets the exit
// code. The problem is quiet because its row already printed it.
func (s *Status) report(ctx context.Context, row func(label, format string, args ...any)) *ExitCodeError {
	row("Version", "%s", version.Get())

	cfg, err := s.resolve()
	if err != nil {
		row("Configuration", "unreadable: %v", err)
		return &ExitCodeError{Code: 2, Err: err, Quiet: true}
	}
	row("Server URL", "%s", valueOr(cfg.ServerURL, "not set"))
	row("Enrollment key", "%s", describeEnrollmentKey(cfg.EnrollmentKey))
	row("Scan interval", "%s", cfg.ScanInterval())
	row("Enforcement", "%s", onOff(cfg.Enforcement()))

	resolveDirs := s.resolveDirs
	if resolveDirs == nil {
		resolveDirs = defaultStatusDirs
	}
	dirs, err := resolveDirs()
	if err != nil {
		row("Enrollment", "not checked: %v", err)
		return &ExitCodeError{Code: 1, Err: err, Quiet: true}
	}

	problem := checkEnrollment(ctx, cfg, dirs, row)
	reportLastScan(dirs.cache, row)
	return problem
}

// checkEnrollment enrolls the device if it isn't already, the same way the
// next scan would, so a bad server URL or key fails here instead of silently
// in a scheduled scan. An enrolled device isn't contacted.
func checkEnrollment(ctx context.Context, cfg mdmconfig.Config, dirs statusDirs, row func(label, format string, args ...any)) *ExitCodeError {
	if cfg.ServerURL == "" {
		err := errors.New("no server URL configured (flag, env, or MDM)")
		row("Enrollment", "not checked: %v", err)
		return &ExitCodeError{Code: 2, Err: err, Quiet: true}
	}

	id, st, err := agent.New(dirs.data, dirs.identity, cfg).EnsureEnrolled(ctx)
	if err != nil {
		row("Enrollment", "failed: %v", err)
		code := 1
		if _, ok := errors.AsType[*mdmconfig.EnrollmentKeyError](err); ok {
			code = 2
		}
		return &ExitCodeError{Code: code, Err: err, Quiet: true}
	}
	row("Enrollment", "enrolled as device %s since %s", id.DeviceID, formatTime(st.EnrolledAt))
	return nil
}

// reportLastScan prints the scan state the current user's scheduled scans
// record, including why the last one failed and where the file lives.
func reportLastScan(cacheDir string, row func(label, format string, args ...any)) {
	scanState, err := state.LoadScanState(cacheDir)
	switch {
	case err != nil:
		row("Last scan", "unreadable: %v", err)
	case scanState.LastStatus == "error":
		row("Last scan", "failed at %s: %s", formatTime(scanState.LastScanAt), scanState.LastError)
	case scanState.LastStatus == "ok":
		row("Last scan", "succeeded at %s", formatTime(scanState.LastScanAt))
	default:
		row("Last scan", "none recorded for this user")
	}
	row("Last submitted", "%s", formatTime(scanState.LastSubmitAt))
	row("Scan results", "%s", filepath.Join(cacheDir, "scan.json"))
}

// describeEnrollmentKey shows a valid key with its secret elided, and says
// what's wrong with any other key without printing it.
func describeEnrollmentKey(key string) string {
	if redacted := mdmconfig.RedactEnrollmentKey(key); redacted != "" {
		return redacted
	}
	switch strings.TrimSpace(key) {
	case "":
		return "not set"
	case mdmconfig.EnrollmentKeyPlaceholder:
		return mdmconfig.EnrollmentKeyPlaceholder + " (the placeholder, not a real key)"
	default:
		return "malformed (Obot enrollment keys look like ode1-<number>-<number>-<secret>)"
	}
}

// defaultStatusDirs resolves the directories enroll and scan use. Enrollment
// state follows enroll, so a root run doesn't leave root-owned files in the
// invoking user's home.
func defaultStatusDirs() (statusDirs, error) {
	data, err := enrollStateDir()
	if err != nil {
		return statusDirs{}, err
	}
	identity, err := datadir.IdentityDir()
	if err != nil {
		return statusDirs{}, err
	}
	cache, err := datadir.CacheDir()
	if err != nil {
		return statusDirs{}, err
	}
	return statusDirs{data: data, identity: identity, cache: cache}, nil
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func formatTime(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Format(time.RFC3339)
}
