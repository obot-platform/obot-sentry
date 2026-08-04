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
			ResolveUser:         func() (*TargetUser, error) { return user, nil },
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
	if strings.Contains(uninstallOut.String(), "Executable:") || !strings.Contains(uninstallOut.String(), "hook-install --uninstall") || !strings.Contains(uninstallOut.String(), "removed") {
		t.Fatalf("unexpected uninstall summary:\n%s", uninstallOut.String())
	}

	for _, path := range []string{
		filepath.Join(home, ".claude/settings.json"),
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
		ResolveUser: func() (*TargetUser, error) {
			return &TargetUser{Username: "alice", HomeDir: home, UID: os.Getuid(), GID: os.Getgid()}, nil
		},
		ResolveDestinations: tempDestinations(machineRoot),
		Uninstall:           true,
		Out:                 &bytes.Buffer{},
	}
	if err := inst.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
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

func TestRunUninstallMalformedConfigAbortsBeforeWriting(t *testing.T) {
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
		ResolveUser:         func() (*TargetUser, error) { return user, nil },
		ResolveDestinations: tempDestinations(machineRoot),
		Uninstall:           true,
		Out:                 &bytes.Buffer{},
	}
	if err := inst.Run(t.Context()); err == nil {
		t.Fatal("expected malformed config to abort uninstall")
	}
	if _, err := os.Stat(filepath.Join(machineRoot, "etc/codex/requirements.toml")); !os.IsNotExist(err) {
		t.Fatalf("later destination was written despite preflight failure: %v", err)
	}
}
