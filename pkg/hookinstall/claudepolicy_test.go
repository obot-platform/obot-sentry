package hookinstall

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

func TestClaudePolicyFromSettings(t *testing.T) {
	for _, tc := range []struct {
		name       string
		settings   map[string]any
		supersedes bool
	}{
		{
			name:       "no settings",
			settings:   map[string]any{},
			supersedes: false,
		},
		{
			name:       "a setting with a value",
			settings:   map[string]any{"allowManagedHooksOnly": true},
			supersedes: true,
		},
		{
			name:       "a false value is still a value",
			settings:   map[string]any{"allowManagedHooksOnly": false},
			supersedes: true,
		},
		{
			name:       "merge turned on",
			settings:   map[string]any{"allowManagedHooksOnly": true, "managedSourcesBehavior": "merge"},
			supersedes: false,
		},
		{
			name:       "first-wins stated outright",
			settings:   map[string]any{"allowManagedHooksOnly": true, "managedSourcesBehavior": "first-wins"},
			supersedes: true,
		},
		{
			name:       "only settings that do not count as a policy",
			settings:   map[string]any{"managedSourcesBehavior": "first-wins", "wslInheritsWindowsSettings": true},
			supersedes: false,
		},
		{
			name:       "only empty objects",
			settings:   map[string]any{"env": map[string]any{}, "sandbox": map[string]any{"network": map[string]any{}}},
			supersedes: false,
		},
		{
			name:       "a nested value",
			settings:   map[string]any{"env": map[string]any{"FOO": "bar"}},
			supersedes: true,
		},
		{
			name:       "an empty list is a value",
			settings:   map[string]any{"deniedMcpServers": []any{}},
			supersedes: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := claudePolicyFromSettings("policy", tc.settings)
			if got.SupersedesFile != tc.supersedes {
				t.Fatalf("SupersedesFile = %v, want %v", got.SupersedesFile, tc.supersedes)
			}
			if wantSource := map[bool]string{true: "policy"}[tc.supersedes]; got.Source != wantSource {
				t.Fatalf("Source = %q, want %q", got.Source, wantSource)
			}
		})
	}
}

func TestClaudePolicyFromJSON(t *testing.T) {
	if got := claudePolicyFromJSON("reg", `{"allowManagedHooksOnly":true}`); !got.SupersedesFile || got.Source != "reg" {
		t.Fatalf("policy with a setting = %+v, want it to supersede the file", got)
	}
	if got := claudePolicyFromJSON("reg", `{"allowManagedHooksOnly":true,"managedSourcesBehavior":"merge"}`); got.SupersedesFile {
		t.Fatalf("merging policy = %+v, want it not to supersede the file", got)
	}
	// A value that cannot be read is assumed to be a policy.
	if got := claudePolicyFromJSON("reg", `{"allowManagedHooksOnly":`); !got.SupersedesFile {
		t.Fatalf("malformed policy = %+v, want it to supersede the file", got)
	}
}

func TestClaudePlistPaths(t *testing.T) {
	device := "/Library/Managed Preferences/com.anthropic.claudecode.plist"
	got := claudePlistPaths(&TargetUser{Username: "alice"})
	want := []string{"/Library/Managed Preferences/alice/com.anthropic.claudecode.plist", device}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("paths = %q, want %q", got, want)
	}
	// A name that is not a single path element cannot name a preferences folder.
	for _, u := range []*TargetUser{nil, {}, {Username: "../etc"}} {
		if got := claudePlistPaths(u); len(got) != 1 || got[0] != device {
			t.Fatalf("paths for %+v = %q, want only the device file", u, got)
		}
	}
}

func TestClaudePolicyFromPlists(t *testing.T) {
	const (
		header    = `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>`
		footer    = `</dict></plist>`
		setting   = `<key>allowManagedHooksOnly</key><true/>`
		merge     = `<key>managedSourcesBehavior</key><string>merge</string>`
		emptyOnly = `<key>env</key><dict/>`
	)
	write := func(t *testing.T, name, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(header+body+footer), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	missing := filepath.Join(t.TempDir(), "missing.plist")

	t.Run("no files", func(t *testing.T) {
		if got := claudePolicyFromPlists([]string{missing}); got != (ClaudePolicy{}) {
			t.Fatalf("policy = %+v, want none", got)
		}
	})
	t.Run("device policy", func(t *testing.T) {
		device := write(t, "device.plist", setting)
		got := claudePolicyFromPlists([]string{missing, device})
		if !got.SupersedesFile || got.Source != device {
			t.Fatalf("policy = %+v, want %s to supersede the file", got, device)
		}
	})
	t.Run("merging policy", func(t *testing.T) {
		if got := claudePolicyFromPlists([]string{write(t, "device.plist", setting+merge)}); got.SupersedesFile {
			t.Fatalf("policy = %+v, want none", got)
		}
	})
	t.Run("user file without a policy falls through to the device file", func(t *testing.T) {
		device := write(t, "device.plist", setting)
		got := claudePolicyFromPlists([]string{write(t, "user.plist", emptyOnly), device})
		if !got.SupersedesFile || got.Source != device {
			t.Fatalf("policy = %+v, want %s to supersede the file", got, device)
		}
	})
	t.Run("unreadable file is assumed to be a policy", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad.plist")
		if err := os.WriteFile(path, []byte("not a plist"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := claudePolicyFromPlists([]string{path}); !got.SupersedesFile || got.Source != path {
			t.Fatalf("policy = %+v, want %s to supersede the file", got, path)
		}
	})
}

// TestRunFailsClaudeCodeUnderMDMPolicy covers a machine whose Claude Code MDM
// policy makes Claude Code ignore managed-settings.json. The hooks are written
// there and nowhere else, the run fails and names the policy, the other agents
// still converge, and the run succeeds once the policy allows the file.
func TestRunFailsClaudeCodeUnderMDMPolicy(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}
	const policySource = "/Library/Managed Preferences/com.anthropic.claudecode.plist"

	supersedes := true
	run := func(t *testing.T) (string, error) {
		t.Helper()
		var out bytes.Buffer
		inst := &Installer{
			GOOS:                "darwin",
			Privilege:           func() error { return nil },
			ResolveExe:          func() (string, error) { return macExe, nil },
			ResolveUser:         func() (*TargetUser, error) { return user, nil },
			ProvisionIdentity:   func() (string, error) { return "/id", nil },
			ResolveDestinations: tempDestinations(machineRoot),
			ClaudePolicy: func(u *TargetUser) ClaudePolicy {
				if u != user {
					t.Fatalf("policy checked for %+v, want the console user", u)
				}
				if !supersedes {
					return ClaudePolicy{}
				}
				return ClaudePolicy{Source: policySource, SupersedesFile: true}
			},
			Enforce: true,
			Out:     &out,
		}
		err := inst.Run(t.Context())
		return out.String(), err
	}
	var (
		userFile    = filepath.Join(home, ".claude", "settings.json")
		managedFile = filepath.Join(machineRoot, "ClaudeCode/managed-settings.json")
		codexFile   = filepath.Join(machineRoot, "etc/codex/requirements.toml")
	)
	hasHooks := func(path string) bool {
		data, err := os.ReadFile(path)
		return err == nil && strings.Contains(string(data), managedMarker)
	}

	out, err := run(t)
	if err == nil || !strings.Contains(err.Error(), "could not converge 1 of") {
		t.Fatalf("err = %v, want only Claude Code to fail\n%s", err, out)
	}
	for _, want := range []string{"not in effect", policySource, `"managedSourcesBehavior": "merge"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
	// Written, so the hooks take effect as soon as the policy merges.
	if !hasHooks(managedFile) {
		t.Fatalf("managed settings have no hooks:\n%s", out)
	}
	if _, err := os.Stat(filepath.Dir(userFile)); !os.IsNotExist(err) {
		t.Fatalf("hooks were installed in user settings (err=%v)", err)
	}
	if !hasHooks(codexFile) {
		t.Fatalf("Codex did not converge alongside the Claude Code failure:\n%s", out)
	}

	supersedes = false
	if out, err := run(t); err != nil {
		t.Fatalf("run after the policy changed: %v\n%s", err, out)
	}
}

// TestRunKeepsOldClaudeUserHooksUnderMDMPolicy covers an upgrade on such a
// machine. The hooks an earlier release left in user settings are the only ones
// running, so they stay until the managed ones are in effect. They are never
// added to or updated.
func TestRunKeepsOldClaudeUserHooksUnderMDMPolicy(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}

	old, err := mergeClaude(nil, "/old/path/obot-sentry", "darwin", true)
	if err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(userFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userFile, old.data, 0o600); err != nil {
		t.Fatal(err)
	}

	supersedes := true
	run := func() (string, error) {
		var out bytes.Buffer
		inst := &Installer{
			GOOS:                "darwin",
			Privilege:           func() error { return nil },
			ResolveExe:          func() (string, error) { return macExe, nil },
			ResolveUser:         func() (*TargetUser, error) { return user, nil },
			ProvisionIdentity:   func() (string, error) { return "/id", nil },
			ResolveDestinations: tempDestinations(machineRoot),
			ClaudePolicy: func(*TargetUser) ClaudePolicy {
				return ClaudePolicy{Source: "policy", SupersedesFile: supersedes}
			},
			Enforce: true,
			Out:     &out,
		}
		err := inst.Run(t.Context())
		return out.String(), err
	}

	out, err := run()
	if err == nil || !strings.Contains(err.Error(), "could not converge 2 of") {
		t.Fatalf("err = %v, want both Claude Code destinations to fail\n%s", err, out)
	}
	if !strings.Contains(out, "left in place") {
		t.Fatalf("summary does not say the old hooks were kept:\n%s", out)
	}
	if got, err := os.ReadFile(userFile); err != nil || !bytes.Equal(got, old.data) {
		t.Fatalf("user settings changed under the policy: err=%v\n%s", err, got)
	}

	supersedes = false
	if out, err := run(); err != nil {
		t.Fatalf("run after the policy changed: %v\n%s", err, out)
	}
	if got, err := os.ReadFile(userFile); err != nil || strings.Contains(string(got), managedMarker) {
		t.Fatalf("old hooks survived in user settings once the managed ones took effect: err=%v\n%s", err, got)
	}
}

// TestUninstallRemovesFromEachClaudeDestinationIndependently covers an uninstall
// that cannot update managed settings. There is no replacement to wait for on
// uninstall, so the hooks in user settings are still removed.
func TestUninstallRemovesFromEachClaudeDestinationIndependently(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}

	hooks, err := mergeClaude(nil, macExe, "darwin", true)
	if err != nil {
		t.Fatal(err)
	}
	var (
		userFile    = filepath.Join(home, ".claude", "settings.json")
		managedFile = filepath.Join(machineRoot, "ClaudeCode/managed-settings.json")
	)
	for path, perm := range map[string]os.FileMode{userFile: 0o600, managedFile: 0o644} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, hooks.data, perm); err != nil {
			t.Fatal(err)
		}
	}

	plan := Plan{
		GOOS:      "darwin",
		User:      user,
		Uninstall: true,
		Destinations: []Destination{
			{Agent: localagent.ClaudeCode, Label: "Claude Code", Scope: ScopeMachine, Format: FormatJSON, Abs: managedFile},
			{Agent: localagent.ClaudeCode, Label: "Claude Code (user settings)", Scope: ScopeUser, Format: FormatJSON, Rel: ".claude/settings.json", Retired: true},
		},
	}
	changes, err := buildChanges(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	// The managed file changes between preflight and commit, so its removal is
	// refused.
	if err := os.WriteFile(managedFile, append(bytes.Clone(hooks.data), '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	results := commitChanges(t.Context(), plan, changes)
	if results[0].Status != StatusFailed {
		t.Fatalf("managed settings result = %+v, want a failure", results[0])
	}
	if results[1].Status != StatusRemoved || results[1].HooksRemoved == 0 {
		t.Fatalf("user settings result = %+v, want the hooks removed", results[1])
	}
	if data, err := os.ReadFile(userFile); err != nil || strings.Contains(string(data), managedMarker) {
		t.Fatalf("user settings still hold owned hooks: err=%v\n%s", err, data)
	}
}
