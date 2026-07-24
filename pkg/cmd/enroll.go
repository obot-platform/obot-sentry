package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/obot-platform/obot-sentry/pkg/agent"
	"github.com/obot-platform/obot-sentry/pkg/datadir"
)

type Enroll struct {
	ConfigFlags
}

func (e *Enroll) Customize(cmd *cobra.Command) {
	cmd.Use = "enroll"
	cmd.Short = "Enroll this device with the Obot server"
	cmd.Long = "Enroll registers this shared device identity's public key with the Obot server " +
		"using the configured enrollment credential. It is idempotent: re-running with the same " +
		"key is a no-op update. Scans enroll automatically, so this is mainly for verifying a " +
		"deployment's configuration."
	cmd.Args = cobra.NoArgs
}

func (e *Enroll) Run(cmd *cobra.Command, _ []string) error {
	cfg, err := e.resolve()
	if err != nil {
		return NewConfigError(err)
	}
	if cfg.ServerURL == "" {
		return NewConfigError(fmt.Errorf("no server URL configured (flag, env, or MDM)"))
	}
	if cfg.EnrollmentKey == "" {
		return NewConfigError(fmt.Errorf("no enrollment key configured (flag, env, or MDM)"))
	}

	dir, err := enrollStateDir()
	if err != nil {
		return err
	}
	idDir, err := datadir.IdentityDir()
	if err != nil {
		return err
	}
	id, st, err := agent.New(dir, idDir, cfg).EnsureEnrolled(cmd.Context())
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Enrolled as device %s (deployment %d, enrolled at %s)\n",
		id.DeviceID, st.MDMDeploymentID, st.EnrolledAt.Format(time.RFC3339))
	return nil
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
