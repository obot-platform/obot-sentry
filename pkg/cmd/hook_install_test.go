package cmd

import (
	"bytes"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	obotcmd "github.com/obot-platform/cmd"
	"github.com/obot-platform/obot-sentry/pkg/hookinstall"
	"github.com/obot-platform/obot-sentry/pkg/mdmconfig"
)

func TestHookInstallVisibleInRootHelp(t *testing.T) {
	root := New()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--help"})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	cmds := listedCommands(stdout.String())
	if !slices.Contains(cmds, "hook-install") {
		t.Fatalf("expected hook-install command in root help, got %v", cmds)
	}
	if !slices.Contains(cmds, "hook-uninstall") {
		t.Fatalf("expected hook-uninstall command in root help, got %v", cmds)
	}
	// The audit plumbing stays hidden even though hook-install (whose
	// description mentions "audit") is public.
	if slices.Contains(cmds, "audit") {
		t.Fatalf("audit must remain hidden from root help, got %v", cmds)
	}
}

// listedCommands extracts the command names from the "Available Commands:"
// section of cobra help output, so tests can assert on the command list rather
// than loose substrings that also match command descriptions.
func listedCommands(help string) []string {
	var cmds []string
	inSection := false
	for line := range strings.SplitSeq(help, "\n") {
		if strings.HasPrefix(line, "Available Commands:") {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		if strings.TrimSpace(line) == "" {
			break
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			cmds = append(cmds, fields[0])
		}
	}
	return cmds
}

func TestHookInstallRejectsPositionalArgs(t *testing.T) {
	root := New()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"hook-install", "unexpected"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected positional argument to be rejected")
	}
}

// TestHookCommandsUnsupportedPlatformMakeNoChanges exercises the commands on
// the test host. On Linux (the CI/dev platform for this suite) each command
// must identify itself in the unsupported-platform error and write nothing to
// stdout.
// This test is skipped when not run on Linux
func TestHookCommandsUnsupportedPlatformMakeNoChanges(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("skipping test on non-Linux platform")
	}

	for _, command := range []string{"hook-install", "hook-uninstall"} {
		t.Run(command, func(t *testing.T) {
			root := New()
			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs([]string{command})

			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), "obot-sentry "+command) {
				t.Fatalf("err = %v, want unsupported-platform error for %s", err, command)
			}
			if stdout.Len() != 0 {
				t.Fatalf("expected no stdout when preflight fails, got %q", stdout.String())
			}
		})
	}
}

// TestHookInstallEnforcementPrecedence pins the resolution order: the --enforce
// flag first, then OBOT_SENTRY_ENFORCEMENT_ENABLED, then the MDM store.
func TestHookInstallEnforcementPrecedence(t *testing.T) {
	ptr := func(b bool) *bool { return &b }

	for _, tt := range []struct {
		name string
		flag *bool
		env  string
		mdm  *bool
		want bool
	}{
		{name: "nothing configured", want: false},
		{name: "MDM on", mdm: ptr(true), want: true},
		{name: "MDM off", mdm: ptr(false), want: false},
		{name: "env on beats MDM off", env: "1", mdm: ptr(false), want: true},
		{name: "env off beats MDM on", env: "off", mdm: ptr(true), want: false},
		{name: "env accepts the wider spellings", env: "yes", want: true},
		{name: "env junk falls through to MDM", env: "sometimes", mdm: ptr(true), want: true},
		{name: "flag on beats everything", flag: ptr(true), env: "off", mdm: ptr(false), want: true},
		{name: "flag off beats everything", flag: ptr(false), env: "1", mdm: ptr(true), want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envEnforcementEnabled, tt.env)
			h := &HookInstall{
				Enforce: tt.flag,
				loadMDMConfig: func() (mdmconfig.Config, error) {
					return mdmconfig.Config{EnforcementEnabled: tt.mdm}, nil
				},
			}
			got, err := h.enforcing(h.mdmLoader())
			if err != nil {
				t.Fatalf("enforcing: %v", err)
			}
			if got != tt.want {
				t.Errorf("enforcing() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHookInstallEnforcementStoreErrorSurfaces(t *testing.T) {
	t.Setenv(envEnforcementEnabled, "")
	h := &HookInstall{loadMDMConfig: func() (mdmconfig.Config, error) {
		return mdmconfig.Config{}, errors.New("registry unavailable")
	}}
	if _, err := h.enforcing(h.mdmLoader()); err == nil {
		t.Fatal("expected an error when the MDM store cannot be read")
	}
}

func TestHookInstallFlagReachesTheInstaller(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		mdm  bool
		want bool
	}{
		{name: "not given, MDM off", args: nil, want: false},
		{name: "not given, MDM on", args: nil, mdm: true, want: true},
		{name: "--enforce", args: []string{"--enforce"}, want: true},
		{name: "--enforce=false over MDM on", args: []string{"--enforce=false"}, mdm: true, want: false},
		{name: "--enforce=true", args: []string{"--enforce=true"}, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envEnforcementEnabled, "")

			var installer *hookinstall.Installer
			hook := &HookInstall{
				loadMDMConfig: func() (mdmconfig.Config, error) {
					enabled := tt.mdm
					return mdmconfig.Config{EnforcementEnabled: &enabled}, nil
				},
				// Every seam stubbed and no destinations, so the command resolves and
				// hands over the toggle without touching a real hook file.
				newInstaller: func() *hookinstall.Installer {
					installer = &hookinstall.Installer{
						GOOS:                "darwin",
						Privilege:           func() error { return nil },
						ResolveExe:          func() (string, error) { return "/usr/local/bin/obot-sentry", nil },
						ResolveUsers:        func() (hookinstall.UserList, error) { return hookinstall.UserList{}, nil },
						ProvisionIdentity:   func() (string, error) { return t.TempDir(), nil },
						ResolveDestinations: func(string) []hookinstall.Destination { return nil },
					}
					return installer
				},
			}

			cmd := obotcmd.Command(hook)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(tt.args)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if installer == nil {
				t.Fatal("the command never built an installer")
			}
			if installer.Enforce != tt.want {
				t.Errorf("installer.Enforce = %v, want %v", installer.Enforce, tt.want)
			}
		})
	}
}

func TestHookUninstallReachesInstaller(t *testing.T) {
	t.Setenv(envEnforcementEnabled, "true")
	var installer *hookinstall.Installer
	hook := &HookUninstall{
		newInstaller: func() *hookinstall.Installer {
			installer = &hookinstall.Installer{
				GOOS:                "darwin",
				Privilege:           func() error { return nil },
				ResolveUsers:        func() (hookinstall.UserList, error) { return hookinstall.UserList{}, nil },
				ResolveDestinations: func(string) []hookinstall.Destination { return nil },
			}
			return installer
		},
	}

	cmd := obotcmd.Command(hook)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if installer == nil || !installer.Uninstall {
		t.Fatalf("installer = %+v, want uninstall mode", installer)
	}
	if installer.Enforce {
		t.Fatal("uninstall unexpectedly enabled enforcement")
	}
}

func TestHookInstallIfDueResolvesInterval(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		env  string
		mdm  int
		want time.Duration
		due  bool
	}{
		{name: "no --if-due", args: nil, due: false},
		{name: "default", args: []string{"--if-due"}, want: 15 * time.Minute, due: true},
		{name: "MDM", args: []string{"--if-due"}, mdm: 60, want: time.Hour, due: true},
		{name: "env over MDM", args: []string{"--if-due"}, env: "30", mdm: 60, want: 30 * time.Minute, due: true},
		{name: "flag over MDM", args: []string{"--if-due", "--hook-check-interval-minutes=10"}, mdm: 60, want: 10 * time.Minute, due: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envEnforcementEnabled, "")
			t.Setenv("OBOT_SENTRY_HOOK_CHECK_INTERVAL_MINUTES", tt.env)

			var installer *hookinstall.Installer
			hook := &HookInstall{
				statePath: filepath.Join(t.TempDir(), "hook-install", "state.json"),
				loadMDMConfig: func() (mdmconfig.Config, error) {
					return mdmconfig.Config{HookCheckIntervalMinutes: tt.mdm}, nil
				},
				newInstaller: func() *hookinstall.Installer {
					installer = &hookinstall.Installer{
						GOOS:                "darwin",
						Privilege:           func() error { return nil },
						ResolveExe:          func() (string, error) { return "/usr/local/bin/obot-sentry", nil },
						ResolveUsers:        func() (hookinstall.UserList, error) { return hookinstall.UserList{}, nil },
						ProvisionIdentity:   func() (string, error) { return t.TempDir(), nil },
						ResolveDestinations: func(string) []hookinstall.Destination { return nil },
					}
					return installer
				},
			}
			cmd := obotcmd.Command(hook)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tt.args)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if installer == nil {
				t.Fatal("the command never built an installer")
			}
			if (installer.Schedule != nil) != tt.due {
				t.Fatalf("Schedule = %+v, want set=%v", installer.Schedule, tt.due)
			}
			if tt.due && installer.Schedule.Interval != tt.want {
				t.Errorf("Interval = %v, want %v", installer.Schedule.Interval, tt.want)
			}
		})
	}
}

// TestHookInstallIfDueFallsBackWhenMDMUnreadable runs as the scheduler does —
// no --enforce, no environment — against an unreadable MDM store: the check
// keeps the last recorded enforcement setting and the default interval.
func TestHookInstallIfDueFallsBackWhenMDMUnreadable(t *testing.T) {
	t.Setenv(envEnforcementEnabled, "")
	t.Setenv("OBOT_SENTRY_HOOK_CHECK_INTERVAL_MINUTES", "")
	var installer *hookinstall.Installer
	hook := &HookInstall{
		statePath:       filepath.Join(t.TempDir(), "state.json"),
		loadMDMConfig:   func() (mdmconfig.Config, error) { return mdmconfig.Config{}, errors.New("corrupt plist") },
		lastEnforcement: func(string) (bool, error) { return true, nil },
		newInstaller: func() *hookinstall.Installer {
			installer = &hookinstall.Installer{
				GOOS:                "darwin",
				Privilege:           func() error { return nil },
				ResolveExe:          func() (string, error) { return "/usr/local/bin/obot-sentry", nil },
				ResolveUsers:        func() (hookinstall.UserList, error) { return hookinstall.UserList{}, nil },
				ProvisionIdentity:   func() (string, error) { return t.TempDir(), nil },
				ResolveDestinations: func(string) []hookinstall.Destination { return nil },
			}
			return installer
		},
	}
	cmd := obotcmd.Command(hook)
	var stderr bytes.Buffer
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--if-due"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("an unreadable MDM store stopped the check: %v", err)
	}
	if installer.Schedule == nil || installer.Schedule.Interval != 15*time.Minute {
		t.Fatalf("Schedule = %+v, want the 15-minute default", installer.Schedule)
	}
	if !installer.Enforce {
		t.Fatal("the last recorded enforcement setting was not kept")
	}
	if !strings.Contains(stderr.String(), "corrupt plist") {
		t.Fatalf("no warning about the MDM store: %q", stderr.String())
	}
}

func TestHookInstallClearCheckRecord(t *testing.T) {
	cleared := false
	hook := &HookInstall{
		clearCheckRecord: func() error { cleared = true; return nil },
		newInstaller: func() *hookinstall.Installer {
			t.Fatal("--clear-check-record must not converge")
			return nil
		},
	}
	cmd := obotcmd.Command(hook)
	cmd.SetArgs([]string{"--clear-check-record"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !cleared {
		t.Fatal("the check record was not cleared")
	}
}

func TestHookInstallIfDueFailsClosedWithoutRecord(t *testing.T) {
	t.Setenv(envEnforcementEnabled, "")
	hook := &HookInstall{
		loadMDMConfig:   func() (mdmconfig.Config, error) { return mdmconfig.Config{}, errors.New("corrupt plist") },
		lastEnforcement: func(string) (bool, error) { return false, errors.New("no record") },
		newInstaller: func() *hookinstall.Installer {
			t.Fatal("converged without an enforcement setting")
			return nil
		},
	}
	cmd := obotcmd.Command(hook)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--if-due"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected the check to fail closed")
	}
}

func TestHookInstallRemoveRunData(t *testing.T) {
	removed := false
	hook := &HookInstall{
		removeRunData: func() error { removed = true; return nil },
		newInstaller: func() *hookinstall.Installer {
			t.Fatal("--remove-run-data must not converge")
			return nil
		},
	}
	cmd := obotcmd.Command(hook)
	cmd.SetArgs([]string{"--remove-run-data"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("run data was not removed")
	}
}
