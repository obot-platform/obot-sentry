package hookinstall

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/localagent"
)

func TestRemoveConfigRemovesOwnedHooksAcrossAllEvents(t *testing.T) {
	tests := []struct {
		name string
		dest Destination
		data string
	}{
		{
			name: "claude nested legacy event",
			dest: Destination{
				Agent:  localagent.ClaudeCode,
				Format: FormatJSON,
			},
			data: `{"hooks":{"LegacyEvent":[{"matcher":"*","hooks":[{"command":"/third/party keep"},{"command":"/old/obot-sentry audit submit --managed-by obot-sentry"}]}]}}`,
		},
		{
			name: "cursor direct legacy event",
			dest: Destination{
				Agent:  localagent.Cursor,
				Format: FormatJSON,
			},
			data: `{"hooks":{"LegacyEvent":[{"command":"/third/party keep"},{"command":"/old/obot-sentry enforce --agent cursor --managed-by obot-sentry"}]}}`,
		},
		{
			name: "vscode direct legacy event",
			dest: Destination{
				Agent:  localagent.VSCode,
				Format: FormatJSON,
			},
			data: `{"hooks":{"LegacyEvent":[{"command":"/third/party keep"},{"command":"/old/obot-sentry audit submit --managed-by obot-sentry"}]}}`,
		},
		{
			name: "codex nested legacy event",
			dest: Destination{
				Agent:  localagent.Codex,
				Format: FormatTOML,
			},
			data: `[[hooks.LegacyEvent]]
matcher = ".*"
[[hooks.LegacyEvent.hooks]]
command = "/third/party keep"
[[hooks.LegacyEvent.hooks]]
command = "/old/obot-sentry audit submit --managed-by obot-sentry"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := removeConfig(tt.dest, []byte(tt.data))
			if err != nil {
				t.Fatal(err)
			}
			if !out.write || out.status != StatusRemoved || out.removed != 1 {
				t.Fatalf("outcome = %+v, want one removed hook", out)
			}
			if strings.Contains(string(out.data), managedMarker) {
				t.Fatalf("managed hook survived:\n%s", out.data)
			}
			if !strings.Contains(string(out.data), "/third/party keep") {
				t.Fatalf("third-party hook was removed:\n%s", out.data)
			}

			again, err := removeConfig(tt.dest, out.data)
			if err != nil {
				t.Fatal(err)
			}
			if again.write || again.status != StatusUnchanged || !bytes.Equal(again.data, out.data) {
				t.Fatalf("second removal = %+v, want byte-identical unchanged", again)
			}
		})
	}
}

// TestRemoveConfigDropsEventsItEmpties covers what removal leaves behind. An
// event array that held only our hooks goes, and so does a hooks object left
// with no events. Anything that was empty before, or that carries a comment,
// was put there by someone else and stays.
func TestRemoveConfigDropsEventsItEmpties(t *testing.T) {
	const (
		ours  = `{"matcher":"*","hooks":[{"type":"command","command":"/old/obot-sentry audit submit --managed-by obot-sentry"}]}`
		user  = `{"matcher":"UserOwned","hooks":[{"type":"command","command":"echo user-hook"}]}`
		claim = `{"command":"/old/obot-sentry audit submit --managed-by obot-sentry"}`
	)
	claude := Destination{
		Agent:  localagent.ClaudeCode,
		Format: FormatJSON,
	}
	tests := []struct {
		name string
		dest Destination
		data string
		want string
	}{
		{
			name: "user settings keep their own hook and lose our events",
			dest: claude,
			data: `{"theme":"dark","hooks":{"PreToolUse":[` + user + `,` + ours + `],"PostToolUse":[` + ours + `],"PostToolUseFailure":[` + ours + `]}}`,
			want: `{"theme":"dark","hooks":{"PreToolUse":[` + user + `]}}`,
		},
		{
			name: "a hooks object holding only ours goes",
			dest: claude,
			data: `{"$schema":"https://json.schemastore.org/claude-code-settings.json","hooks":{"PreToolUse":[` + ours + `],"PostToolUse":[` + ours + `]}}`,
			want: `{"$schema":"https://json.schemastore.org/claude-code-settings.json"}`,
		},
		{
			name: "an event that was already empty stays",
			dest: claude,
			data: `{"hooks":{"Stop":[],"PostToolUse":[` + ours + `]}}`,
			want: `{"hooks":{"Stop":[]}}`,
		},
		{
			name: "an emptied event with a comment stays",
			dest: claude,
			data: "{\"hooks\":{\n// audit trail, see ticket 42\n\"PostToolUse\":[" + ours + "]}}",
			want: "{\"hooks\":{\n// audit trail, see ticket 42\n\"PostToolUse\":[]}}",
		},
		{
			name: "cursor direct entries",
			dest: Destination{
				Agent:  localagent.Cursor,
				Format: FormatJSON,
			},
			data: `{"version":1,"hooks":{"postToolUse":[` + claim + `],"stop":[{"command":"/third/party keep"}]}}`,
			want: `{"version":1,"hooks":{"stop":[{"command":"/third/party keep"}]}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := removeConfig(tt.dest, []byte(tt.data))
			if err != nil {
				t.Fatal(err)
			}
			if !out.write || out.status != StatusRemoved {
				t.Fatalf("outcome = %+v, want a removal", out)
			}
			if got := string(out.data); got != tt.want {
				t.Fatalf("removal left\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestAuditOnlyRemovesEnforcementFromUnexpectedEvents(t *testing.T) {
	tests := []struct {
		name string
		dest Destination
		data string
	}{
		{
			name: "claude",
			dest: Destination{
				Agent:  localagent.ClaudeCode,
				Format: FormatJSON,
			},
			data: `{"hooks":{"LegacyPreTool":[{"matcher":"*","hooks":[{"command":"/old/obot-sentry enforce --agent claude-code --event LegacyPreTool --managed-by obot-sentry"},{"command":"/old/obot-sentry audit submit --agent claude-code --phase post-tool --managed-by obot-sentry"},{"command":"/third/party keep"}]}]}}`,
		},
		{
			name: "cursor",
			dest: Destination{
				Agent:  localagent.Cursor,
				Format: FormatJSON,
			},
			data: `{"hooks":{"LegacyPreTool":[{"command":"/old/obot-sentry enforce --agent cursor --event LegacyPreTool --managed-by obot-sentry"},{"command":"/old/obot-sentry audit submit --agent cursor --phase post-tool --managed-by obot-sentry"},{"command":"/third/party keep"}]}}`,
		},
		{
			name: "codex",
			dest: Destination{
				Agent:  localagent.Codex,
				Format: FormatTOML,
			},
			data: `[[hooks.LegacyPreTool]]
matcher = ".*"
[[hooks.LegacyPreTool.hooks]]
command = "/old/obot-sentry enforce --agent codex --event LegacyPreTool --managed-by obot-sentry"
[[hooks.LegacyPreTool.hooks]]
command = "/old/obot-sentry audit submit --agent codex --phase post-tool --managed-by obot-sentry"
[[hooks.LegacyPreTool.hooks]]
command = "/third/party keep"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := mergeConfig(tt.dest, []byte(tt.data), macExe, "darwin", false)
			if err != nil {
				t.Fatal(err)
			}
			if out.removed != 1 || out.status != StatusUpdated || !out.write {
				t.Fatalf("outcome = %+v, want one removed enforcement hook", out)
			}
			body := string(out.data)
			if strings.Contains(body, "enforce --agent") {
				t.Fatalf("enforcement hook survived:\n%s", body)
			}
			if !strings.Contains(body, "/third/party keep") || strings.Count(body, "audit submit --agent") < 2 {
				t.Fatalf("expected hooks missing after convergence:\n%s", body)
			}
		})
	}
}

func TestRunUninstallEndToEnd(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}
	newInstaller := func(uninstall bool, out *bytes.Buffer) *Installer {
		return &Installer{
			GOOS:                "darwin",
			Privilege:           func() error { return nil },
			ResolveExe:          func() (string, error) { return macExe, nil },
			ResolveUsers:        func() (UserList, error) { return UserList{Users: []*TargetUser{user}}, nil },
			ProvisionIdentity:   func() (string, error) { return "/id", nil },
			ResolveDestinations: tempDestinations(machineRoot),
			Enforce:             true,
			Uninstall:           uninstall,
			Out:                 out,
		}
	}

	var installOut bytes.Buffer
	if err := newInstaller(false, &installOut).Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(home, "Library/Application Support/Code/User/settings.json")
	settingsBefore, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	var uninstallOut bytes.Buffer
	inst := newInstaller(true, &uninstallOut)
	inst.ResolveExe = func() (string, error) { t.Fatal("uninstall resolved the executable"); return "", nil }
	inst.ProvisionIdentity = func() (string, error) { t.Fatal("uninstall provisioned identity"); return "", nil }
	if err := inst.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(uninstallOut.String(), "Executable:") || !strings.Contains(uninstallOut.String(), "hook-uninstall") || !strings.Contains(uninstallOut.String(), "removed") {
		t.Fatalf("unexpected uninstall summary:\n%s", uninstallOut.String())
	}

	for _, path := range []string{
		filepath.Join(machineRoot, "ClaudeCode/managed-settings.json"),
		filepath.Join(machineRoot, "etc/codex/requirements.toml"),
		filepath.Join(home, ".copilot/hooks/obot-sentry.json"),
		filepath.Join(machineRoot, "Cursor/hooks.json"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), managedMarker) {
			t.Errorf("managed hook survived in %s:\n%s", path, data)
		}
	}
	settingsAfter, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(settingsBefore, settingsAfter) {
		t.Fatalf("unmarked VS Code settings changed:\n--- before ---\n%s\n--- after ---\n%s", settingsBefore, settingsAfter)
	}
	codex, err := os.ReadFile(filepath.Join(machineRoot, "etc/codex/requirements.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(codex), "hooks = true") || !strings.Contains(string(codex), "non_prefixed_mcp_tool_names = false") {
		t.Fatalf("unmarked Codex feature pins were removed:\n%s", codex)
	}

	var again bytes.Buffer
	if err := newInstaller(true, &again).Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(again.String(), string(StatusRemoved)) || !strings.Contains(again.String(), string(StatusUnchanged)) {
		t.Fatalf("second uninstall was not unchanged:\n%s", again.String())
	}
}

func TestRunUninstallDoesNotCreateMissingFiles(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	inst := &Installer{
		GOOS:      "darwin",
		Privilege: func() error { return nil },
		ResolveUsers: func() (UserList, error) {
			return UserList{Users: []*TargetUser{{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}}}, nil
		},
		ResolveDestinations: tempDestinations(machineRoot),
		Uninstall:           true,
		Out:                 &bytes.Buffer{},
	}
	if err := inst.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(machineRoot, "ClaudeCode/managed-settings.json"),
		filepath.Join(home, ".claude/settings.json"),
		filepath.Join(machineRoot, "etc/codex/requirements.toml"),
		filepath.Join(home, ".copilot/hooks/obot-sentry.json"),
		filepath.Join(machineRoot, "Cursor/hooks.json"),
		filepath.Join(home, "Library/Application Support/Code/User/settings.json"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("missing destination was created: %s (err=%v)", path, err)
		}
	}
}

// TestRunUninstallMalformedConfigFailsWithoutRewriting proves uninstall reports a
// malformed file as failed and leaves it as it was.
func TestRunUninstallMalformedConfigFailsWithoutRewriting(t *testing.T) {
	home := t.TempDir()
	machineRoot := realTempDir(t)
	user := &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}
	claudePath := filepath.Join(home, ".claude/settings.json")
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudePath, []byte(`{"hooks":`), 0o600); err != nil {
		t.Fatal(err)
	}

	inst := &Installer{
		GOOS:                "darwin",
		Privilege:           func() error { return nil },
		ResolveUsers:        func() (UserList, error) { return UserList{Users: []*TargetUser{user}}, nil },
		ResolveDestinations: tempDestinations(machineRoot),
		Uninstall:           true,
		Out:                 &bytes.Buffer{},
	}
	if err := inst.Run(t.Context()); err == nil {
		t.Fatal("expected malformed config to fail uninstall")
	}
	if got, err := os.ReadFile(claudePath); err != nil || string(got) != `{"hooks":` {
		t.Fatalf("malformed file was rewritten: %q (err=%v)", got, err)
	}
	if _, err := os.Stat(filepath.Join(machineRoot, "etc/codex/requirements.toml")); !os.IsNotExist(err) {
		t.Fatalf("uninstall created a destination: %v", err)
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
		Users:     []*TargetUser{user},
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
