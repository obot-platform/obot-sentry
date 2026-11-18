package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/obot-platform/obot-sentry/pkg/agent"
	"github.com/obot-platform/obot-sentry/pkg/client"
	"github.com/obot-platform/obot-sentry/pkg/datadir"
	"github.com/obot-platform/obot-sentry/pkg/eventlog"
	"github.com/obot-platform/obot-sentry/pkg/identity"
	"github.com/obot-platform/obot-sentry/pkg/state"
)

type Enroll struct {
	ConfigFlags
	InstallCheck bool `usage:"Installer mode: exit 3 only when the server rejects the enrollment; report any other failure, such as an unreachable server, and exit 0 so scheduled scans enroll later"`
	Timeout      int  `usage:"Number of seconds to wait for the server" default:"60" env:"OBOT_SENTRY_ENROLL_TIMEOUT"`
}

func (e *Enroll) Customize(cmd *cobra.Command) {
	cmd.Use = "enroll"
	cmd.Short = "Enroll this device with the Obot server"
	cmd.Long = "Enroll registers this shared device identity's public key with the Obot server " +
		"using the configured enrollment credential. It is idempotent: re-running with the same " +
		"key is a no-op update. Scans enroll automatically, so this is mainly for verifying a " +
		"deployment's configuration. It exits 2 when the configuration is missing and 3 when the " +
		"server rejects the enrollment."
	cmd.Args = cobra.NoArgs
}

// Run enrolls the device and reports the outcome.
// By default any failure fails the command, and a server rejection exits 3.
// With --install-check only a rejection fails it, so an installer can stop on a key that will never work without
// stopping on a server it can't reach yet.
func (e *Enroll) Run(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if e.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(e.Timeout)*time.Second)
		defer cancel()
	}

	serverURL, id, st, err := e.enroll(ctx)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
		// Our timeout fired. main exits silently on context errors, so replace it with one that says what happened.
		err = fmt.Errorf("enrolling device: no response from %s within %d seconds", serverURL, e.Timeout)
	}

	if e.InstallCheck {
		// Nobody watches an installer's output, so the result goes to the event log and an exit code instead.
		return installCheckResult(cmd, serverURL, id, err)
	}
	if err != nil {
		return rejectedExitCode(err)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Enrolled as device %s (deployment %d, enrolled at %s)\n",
		id.DeviceID, st.MDMDeploymentID, st.EnrolledAt.Format(time.RFC3339))
	return nil
}

// enroll resolves the deployment configuration and enrolls the shared device identity with the configured server.
// It returns the server URL alongside the result, even on failure, so the caller can name the server in its report.
func (e *Enroll) enroll(ctx context.Context) (string, *identity.Identity, state.State, error) {
	cfg, err := e.resolve()
	if err != nil {
		return "", nil, state.State{}, NewConfigError(err)
	}
	if cfg.ServerURL == "" {
		return "", nil, state.State{}, NewConfigError(fmt.Errorf("no server URL configured (flag, env, or MDM)"))
	}
	if cfg.EnrollmentKey == "" {
		return cfg.ServerURL, nil, state.State{}, NewConfigError(fmt.Errorf("no enrollment key configured (flag, env, or MDM)"))
	}

	dir, err := enrollStateDir()
	if err != nil {
		return cfg.ServerURL, nil, state.State{}, err
	}
	idDir, err := datadir.IdentityDir()
	if err != nil {
		return cfg.ServerURL, nil, state.State{}, err
	}
	id, st, err := agent.New(dir, idDir, cfg).EnsureEnrolled(ctx)
	return cfg.ServerURL, id, st, err
}

// installCheckResult logs an installer's enrollment attempt to the event log and decides whether the install
// continues.
// Only a server rejection stops the install (exit 3). Rolling back then leaves nothing behind, instead of a device
// that can never enroll and, with enforcement on, blocks every tool call.
// Any other failure lets the install finish: an unreachable server or a server error is expected while a device is
// still being provisioned, and the scheduled scan enrolls the device once the server answers.
func installCheckResult(cmd *cobra.Command, serverURL string, id *identity.Identity, err error) error {
	switch {
	case err == nil:
		msg := fmt.Sprintf("Obot Sentry enrolled with %s as device %s.", serverURL, id.DeviceID)
		eventlog.Info(eventlog.EnrollSucceeded, msg)
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), msg)
		return nil
	case client.IsRejected(err):
		// The MSI shows only a generic error for a failed custom action, so this event carries the reason.
		eventlog.Error(eventlog.EnrollRejected,
			fmt.Sprintf("Obot Sentry installation stopped because %s rejected the device enrollment: %v", serverURL, err))
		return &ExitCodeError{Code: exitCodeRejected, Err: err}
	default:
		msg := fmt.Sprintf("Obot Sentry could not enroll with %s during installation; the scheduled scan will retry: %v", serverURL, err)
		eventlog.Warning(eventlog.EnrollDeferred, msg)
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+msg)
		return nil
	}
}

// enrollStateDir returns the directory this command's enrollment state is
// written to. A root run must use the machine-scoped dir: sudo preserves
// $HOME on macOS, so the per-user dir would be created root-owned in the
// invoking user's home and break that user's later scans. Per-user runs
// keep their own state and re-enroll the shared identity idempotently.
func enrollStateDir() (string, error) {
	if os.Geteuid() == 0 {
		return datadir.MachineDir()
	}
	return datadir.Dir()
}
