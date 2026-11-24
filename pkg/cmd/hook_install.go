package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/obot-platform/obot-sentry/pkg/hookinstall"
	"github.com/obot-platform/obot-sentry/pkg/mdmconfig"
	"github.com/obot-platform/obot-sentry/pkg/version"
	"github.com/spf13/cobra"
)

const envEnforcementEnabled = "OBOT_SENTRY_ENFORCEMENT_ENABLED"

// HookInstall is the operator-facing `obot-sentry hook-install` command. It converges
// the native audit-hook configuration for the supported local agents onto
// the hidden `obot-sentry audit submit` command, and — with enforcement enabled —
// the pre-tool hooks for the three supported by `obot-sentry enforce`. All platform,
// privilege, path, and executable resolution lives in pkg/hookinstall behind
// injectable seams so this command stays a thin orchestration layer.
type HookInstall struct {
	// Enforce is a tri-state: nil means the flag was not given, so the
	// environment and then the MDM store decide. It cannot be a bare bool because
	// false is a meaningful value — it is how an operator overrides an
	// MDM-configured true.
	Enforce *bool `usage:"also install the pre-tool enforcement hooks (defaults to OBOT_SENTRY_ENFORCEMENT_ENABLED, then the MDM-configured EnforcementEnabled)"`

	// IfDue makes this run a periodic check: it converges only when one is due.
	// The OS scheduler passes it; an operator's run without it always converges.
	IfDue bool `usage:"converge only when a periodic hook check is due (passed by the OS scheduler)"`
	// ClearCheckRecord forgets the last periodic check and exits; the
	// installers run it so a reinstall checks at once.
	ClearCheckRecord bool `usage:"forget the last periodic hook check, so the next one runs at once, and exit (used by the installers)"`
	// RemoveRunData removes the run log and the check record and exits; the
	// uninstallers run it.
	RemoveRunData bool `usage:"remove the hook-install run log and periodic-check record, and exit (used by the uninstallers)"`
	// HookCheckIntervalMinutes overrides the MDM-configured interval between
	// periodic checks; 0 leaves it to the MDM store and then the default.
	HookCheckIntervalMinutes int `usage:"Minutes between periodic hook checks with --if-due (overrides the MDM-configured value)" env:"OBOT_SENTRY_HOOK_CHECK_INTERVAL_MINUTES"`

	// loadMDMConfig reads the platform MDM store. It is wired by New so tests can
	// substitute a stub, keeping them independent of any real MDM configuration on
	// the developer's machine. When unset it falls back to the real loader.
	loadMDMConfig func() (mdmconfig.Config, error)

	// clearCheckRecord is the --clear-check-record action; tests substitute a
	// stub. When unset it falls back to hookinstall.ClearCheckRecord.
	clearCheckRecord func() error

	// removeRunData and lastEnforcement are the --remove-run-data action and
	// the enforcement fallback; tests substitute stubs. When unset they fall
	// back to the hookinstall functions.
	removeRunData   func() error
	lastEnforcement func(statePath string) (bool, error)

	// statePath overrides where --if-due keeps its record. Tests set it so they
	// never touch the platform location; empty means the platform default.
	statePath string

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
the Visual Studio Code and Kiro installations of every local user whose
profile has been set up by a first sign-in, signed in or not.

With enforcement enabled, also installs the pre-tool hooks that check each tool
call against Obot's allowlist, for Claude Code, Codex, and Cursor. A run without
enforcement removes obot-sentry-managed enforcement hooks.

Missing hooks are restored and changed ones repaired; third-party hooks and
unrelated settings are left alone. The OS scheduler runs this command with
--if-due every few minutes, and it converges when a check is due: once the
interval (HookCheckIntervalMinutes, default 15) has passed, after a restart, when
a user profile appears, or when the version or enforcement setting changes.
Every run's changes and failures are appended to the run log
(/Library/Logs/obot/hook-install.log on macOS,
%ProgramData%\obot\hook-install\hook-install.log on Windows).`
	cmd.Args = cobra.NoArgs
}

func (h *HookInstall) Run(cmd *cobra.Command, _ []string) error {
	if h.ClearCheckRecord {
		clear := h.clearCheckRecord
		if clear == nil {
			clear = hookinstall.ClearCheckRecord
		}
		return clear()
	}
	if h.RemoveRunData {
		remove := h.removeRunData
		if remove == nil {
			remove = hookinstall.RemoveRunData
		}
		return remove()
	}
	mdm := h.mdmLoader()
	enforcing, err := h.enforcing(mdm)
	if err != nil {
		if !h.IfDue {
			return NewConfigError(err)
		}
		// A periodic check keeps the enforcement setting it last converged
		// with rather than guessing: guessing off would strip enforcement
		// hooks, guessing on would add ones nobody configured. With no record
		// to fall back on it fails closed, changing nothing.
		last := h.lastEnforcement
		if last == nil {
			last = hookinstall.LastRecordedEnforcement
		}
		prev, lerr := last(h.statePath)
		if lerr != nil {
			return NewConfigError(fmt.Errorf("%w; no earlier check recorded an enforcement setting to keep", err))
		}
		enforcing = prev
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v; keeping the last checked enforcement setting (%v)\n", err, prev)
	}

	newInstaller := h.newInstaller
	if newInstaller == nil {
		newInstaller = hookinstall.New
	}
	installer := newInstaller()
	installer.Out = cmd.OutOrStdout()
	installer.ErrOut = cmd.ErrOrStderr()
	installer.Enforce = enforcing
	if h.IfDue {
		interval, err := h.checkInterval(mdm)
		if err != nil {
			// An unreadable MDM store must not stop hooks being repaired: the
			// interval only paces the checks.
			interval = mdmconfig.Config{}.HookCheckInterval()
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v; checking hooks every %s\n", err, interval)
		}
		installer.Schedule = &hookinstall.Schedule{Interval: interval, Version: version.Get().String(), StatePath: h.statePath}
	}
	return installer.Run(cmd.Context())
}

// mdmLoader returns a loader that reads the MDM store at most once, and only
// if a setting is not resolved from a flag or the environment first.
func (h *HookInstall) mdmLoader() func() (mdmconfig.Config, error) {
	load := h.loadMDMConfig
	if load == nil {
		load = mdmconfig.Load
	}
	var (
		loaded bool
		cfg    mdmconfig.Config
		err    error
	)
	return func() (mdmconfig.Config, error) {
		if !loaded {
			cfg, err = load()
			loaded = true
		}
		if err != nil {
			return mdmconfig.Config{}, fmt.Errorf("reading MDM configuration: %w", err)
		}
		return cfg, nil
	}
}

// enforcing resolves the enforcement toggle: the --enforce flag first, then
// OBOT_SENTRY_ENFORCEMENT_ENABLED, then the MDM store's EnforcementEnabled.
func (h *HookInstall) enforcing(mdm func() (mdmconfig.Config, error)) (bool, error) {
	if h.Enforce != nil {
		return *h.Enforce, nil
	}
	if enabled, ok := mdmconfig.ParseBool(os.Getenv(envEnforcementEnabled)); ok {
		return enabled, nil
	}
	cfg, err := mdm()
	if err != nil {
		return false, err
	}
	return cfg.Enforcement(), nil
}

// checkInterval resolves the time between periodic checks: the flag or
// OBOT_SENTRY_HOOK_CHECK_INTERVAL_MINUTES first, then the MDM store's
// HookCheckIntervalMinutes, clamped to its bounds.
func (h *HookInstall) checkInterval(mdm func() (mdmconfig.Config, error)) (time.Duration, error) {
	cfg := mdmconfig.Config{HookCheckIntervalMinutes: max(0, h.HookCheckIntervalMinutes)}
	if cfg.HookCheckIntervalMinutes == 0 {
		fallback, err := mdm()
		if err != nil {
			return 0, err
		}
		cfg = cfg.Merge(fallback)
	}
	return cfg.HookCheckInterval(), nil
}
