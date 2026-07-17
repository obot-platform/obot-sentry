package cmd

import (
	"github.com/obot-platform/obot-sentry/pkg/hookinstall"
	"github.com/spf13/cobra"
)

// HookInstall is the operator-facing `obot-sentry hook-install` command. It converges
// the native audit-hook configuration for the four supported local agents onto
// the hidden `obot-sentry audit submit` command. All platform, privilege, path, and
// executable resolution lives in pkg/hookinstall behind injectable seams so this
// command stays a thin orchestration layer.
type HookInstall struct{}

func (h *HookInstall) Customize(cmd *cobra.Command) {
	cmd.Use = "hook-install"
	cmd.Short = "Install managed local-agent audit hooks"
	cmd.Long = `Install managed local-agent audit hooks

Requires root on macOS or an elevated Administrator/SYSTEM token on Windows.
Installs machine policy for Codex and Cursor and user hooks for the active
console user's Claude Code and Visual Studio Code installations.`
	cmd.Args = cobra.NoArgs
}

func (h *HookInstall) Run(cmd *cobra.Command, _ []string) error {
	installer := hookinstall.New()
	installer.Out = cmd.OutOrStdout()
	return installer.Run(cmd.Context())
}
