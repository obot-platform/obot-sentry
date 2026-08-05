package cmd

import (
	"github.com/obot-platform/obot-sentry/pkg/hookinstall"
	"github.com/spf13/cobra"
)

// HookUninstall is the operator-facing `obot-sentry hook-uninstall` command.
// It removes hook entries owned by obot-sentry while leaving third-party hooks
// and unmarked supporting settings unchanged.
type HookUninstall struct {
	// newInstaller builds the converger. Tests substitute one whose seams are
	// stubbed, so they can assert uninstall mode without writing real hook files.
	// When unset it falls back to the real installer.
	newInstaller func() *hookinstall.Installer
}

func (h *HookUninstall) Customize(cmd *cobra.Command) {
	cmd.Use = "hook-uninstall"
	cmd.Short = "Uninstall managed local-agent hooks"
	cmd.Long = `Uninstall managed local-agent hooks

Requires root on macOS or an elevated Administrator/SYSTEM token on Windows.
Removes every hook entry marked --managed-by obot-sentry from machine policy
and the active console user's files while leaving third-party hooks and
unmarked supporting settings unchanged.`
	cmd.Args = cobra.NoArgs
}

func (h *HookUninstall) Run(cmd *cobra.Command, _ []string) error {
	newInstaller := h.newInstaller
	if newInstaller == nil {
		newInstaller = hookinstall.New
	}
	installer := newInstaller()
	installer.Out = cmd.OutOrStdout()
	installer.Uninstall = true
	return installer.Run(cmd.Context())
}
