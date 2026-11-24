package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/obot-platform/obot-sentry/pkg/hookinstall"
	"github.com/obot-platform/obot-sentry/pkg/mdmconfig"
	"github.com/spf13/cobra"
)

const envEnforcementEnabled = "OBOT_SENTRY_ENFORCEMENT_ENABLED"

// HookInstall is the operator-facing `obot-sentry hook-install` command. It converges
// the native audit-hook configuration for the five supported local agents onto
// the hidden `obot-sentry audit submit` command, and — with enforcement enabled —
// the pre-tool hooks for the agents supported by `obot-sentry enforce`. All platform,
// privilege, path, and executable resolution lives in pkg/hookinstall behind
// injectable seams so this command stays a thin orchestration layer.
type HookInstall struct {
	// Enforce is a tri-state: nil means the flag was not given, so the
	// environment and then the MDM store decide. It cannot be a bare bool because
	// false is a meaningful value — it is how an operator overrides an
	// MDM-configured true.
	Enforce *bool `usage:"also install the pre-tool enforcement hooks (defaults to OBOT_SENTRY_ENFORCEMENT_ENABLED, then the MDM-configured EnforcementEnabled)"`

	IfDue bool `usage:"converge only if the hook interval has elapsed since the last scheduled run"`

	HookIntervalMinutes int `usage:"Minutes between scheduled hook convergences with --if-due (overrides the MDM-configured value; default 15)" env:"OBOT_SENTRY_HOOK_INTERVAL_MINUTES"`

	// loadMDMConfig reads the platform MDM store. It is wired by New so tests can
	// substitute a stub, keeping them independent of any real MDM configuration on
	// the developer's machine. When unset it falls back to the real loader.
	loadMDMConfig func() (mdmconfig.Config, error)

	// newInstaller builds the converger. Tests substitute one whose seams are
	// stubbed, so they can assert what the command resolved without writing to
	// real hook files. When unset it falls back to the real installer.
	newInstaller func() *hookinstall.Installer
}

func (h *HookInstall) Customize(cmd *cobra.Command) {
	cmd.Use = "hook-install"
	cmd.Short = "Install managed local-agent hooks"
	cmd.Long = `Install managed local-agent hooks

Requires root on macOS or an elevated Administrator/SYSTEM token on Windows.
Installs machine policy for Claude Code, Codex, and Cursor and user hooks for
the Visual Studio Code and Kiro installations of every local user, whether or
not they are signed in, and on macOS of a signed-in network account too.

With enforcement enabled, also installs the pre-tool hooks that check each tool
call against Obot's allowlist, for Claude Code, Codex, Cursor, and Kiro. A run
without enforcement removes obot-sentry-managed enforcement hooks.

Each run appends the hooks it changed and any failures to hook-install.log in
/Library/Application Support/obot/obot-sentry-hook-install on macOS, or
C:\Program Files\Obot\obot-sentry\hook-install on Windows. Both are readable
only by root, or by SYSTEM and Administrators.`
	cmd.Args = cobra.NoArgs
}

func (h *HookInstall) Run(cmd *cobra.Command, _ []string) error {
	enforcing, err := h.enforcing()
	if err != nil {
		return NewConfigError(err)
	}
	var interval time.Duration
	if h.IfDue {
		if interval, err = h.interval(); err != nil {
			return NewConfigError(err)
		}
	}

	newInstaller := h.newInstaller
	if newInstaller == nil {
		newInstaller = hookinstall.New
	}
	installer := newInstaller()
	installer.Out = cmd.OutOrStdout()
	installer.Enforce = enforcing
	installer.IfDue = h.IfDue
	installer.Interval = interval
	return installer.Run(cmd.Context())
}

func (h *HookInstall) loadMDM() (mdmconfig.Config, error) {
	load := h.loadMDMConfig
	if load == nil {
		load = mdmconfig.Load
	}
	cfg, err := load()
	if err != nil {
		return mdmconfig.Config{}, fmt.Errorf("reading MDM configuration: %w", err)
	}
	return cfg, nil
}

// enforcing resolves the enforcement toggle: the --enforce flag first, then
// OBOT_SENTRY_ENFORCEMENT_ENABLED, then the MDM store's EnforcementEnabled.
func (h *HookInstall) enforcing() (bool, error) {
	if h.Enforce != nil {
		return *h.Enforce, nil
	}
	if enabled, ok := mdmconfig.ParseBool(os.Getenv(envEnforcementEnabled)); ok {
		return enabled, nil
	}
	cfg, err := h.loadMDM()
	if err != nil {
		return false, err
	}
	return cfg.Enforcement(), nil
}

func (h *HookInstall) interval() (time.Duration, error) {
	cfg, err := h.loadMDM()
	if err != nil {
		return 0, err
	}
	return mdmconfig.Config{HookIntervalMinutes: h.HookIntervalMinutes}.Merge(cfg).HookInterval(), nil
}
