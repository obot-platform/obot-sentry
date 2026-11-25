# Obot Sentry

`obot-sentry` is a command-line tool that an MDM installs on each device. It enrolls the device with an [Obot](https://github.com/obot-platform/obot) server, reports each user's AI client configuration, and installs hooks that audit and enforce local agents' tool calls.

## How it works

The installer registers two scheduled jobs.

**Scan.** A scan job runs as each signed-in user at logon and then every 10 minutes on Windows or every 15 minutes on macOS. Each run inventories that user's MCP servers, skills, and plugins and submits the result to Obot. Submissions are throttled to `ScanIntervalMinutes`, which defaults to 60. Users who are not signed in are not scanned.

**Hooks.** A hook job runs as SYSTEM on Windows and as root on macOS. It runs `obot-sentry hook-install --if-due` at startup, at logon on Windows, and every 5 minutes. A run is due once `HookIntervalMinutes` has passed since the last one, which defaults to 15. A run is also due on the next poll after a restart, a new or removed user profile, a change to the enforcement setting, an upgrade, or a failed run. Each run restores missing or modified Obot Sentry hooks in the machine-wide agent files and in every local user's files, signed in or not, and leaves other hooks in place.

Each hook run appends the hooks it changed and any failures to `hook-install.log`, and records its inputs in `schedule.json`. Both files live in a folder that only administrators can write:

- macOS: `/Library/Application Support/obot/obot-sentry-hook-install`
- Windows: `C:\Program Files\Obot\obot-sentry\hook-install`

**Enrollment.** On the device's first scan, obot-sentry creates an Ed25519 key in the machine data folder and enrolls its public key with Obot. All users on the device share this key, so Obot sees one device with several users. The machine data folder is `%PROGRAMDATA%\obot\obot-sentry` on Windows and `/Library/Application Support/obot/obot-sentry` on macOS. Every scan and audit submission carries a short-lived JWT signed with the key.

**Audit hooks.** After a tool call finishes, the audit hook submits a record of it to Obot. The audit hook never blocks the agent. A failed submission is saved in an encrypted per-user spool and sent again after a later submission succeeds.

**Enforcement hooks.** When enforcement is enabled, a hook runs before each tool call and blocks it unless Obot allows it. See [Tool-call enforcement](#tool-call-enforcement).

Each scan updates `scan.json` and writes a record to `scan-logs/` in the per-user cache folder. That folder is `%LOCALAPPDATA%\obot\obot-sentry` on Windows and `~/Library/Caches/obot/obot-sentry` on macOS.

## Commands

```
obot-sentry scan              # print the manifest; --submit enrolls and uploads it
                              #   --submit --force ignores the scan interval
obot-sentry enroll            # enroll explicitly, to check a configuration
obot-sentry hook-install      # install the hooks (root or Administrator)
                              #   --enforce also installs the enforcement hooks
obot-sentry hook-uninstall    # remove every Obot Sentry hook (root or Administrator)
obot-sentry version
```

The agent hooks call the hidden commands `obot-sentry audit submit` and `obot-sentry enforce`. Only `obot-sentry enforce resolve` is meant to be run by hand. [docs/enforcement.md](docs/enforcement.md#diagnosing-a-block) describes it.

## Configuration

A flag overrides an environment variable, and an environment variable overrides the MDM setting.

| Setting | Flag | Environment variable | MDM key |
|---|---|---|---|
| Server URL | `--server-url` | `OBOT_SENTRY_SERVER_URL` | `ServerURL` |
| Enrollment key | `--enrollment-key` | `OBOT_SENTRY_ENROLLMENT_KEY` | `EnrollmentKey` |
| Scan interval in minutes, 15 to 1440 | `--scan-interval-minutes` | `OBOT_SENTRY_SCAN_INTERVAL_MINUTES` | `ScanIntervalMinutes` |
| Hook check interval in minutes, 5 to 1440 | `hook-install --hook-interval-minutes` | `OBOT_SENTRY_HOOK_INTERVAL_MINUTES` | `HookIntervalMinutes` |
| Enforcement | `hook-install --enforce` | `OBOT_SENTRY_ENFORCEMENT_ENABLED` | `EnforcementEnabled` |

The MDM settings are read from `HKLM\SOFTWARE\Obot\obot-sentry` on Windows. On macOS they are read from `/Library/Managed Preferences/ai.obot.obot-sentry.plist`, or from `/Library/Preferences/ai.obot.obot-sentry.plist` when the managed file is absent.

`EnforcementEnabled` accepts a plist boolean, `1` or `0`, `true` or `false`, `yes` or `no`, and `on` or `off`. obot-sentry ignores any other value and falls through to the next layer, so a typo does not turn enforcement off. Enforcement is off when no layer sets it. On an installed Windows device, the registry value is the only way to turn enforcement on, because the scheduled task's arguments are fixed.

## Tool-call enforcement

`hook-install --enforce` adds a hook that runs before each tool call in these agents:

| Agent | macOS file | Windows file | Events |
|---|---|---|---|
| Claude Code | `/Library/Application Support/ClaudeCode/managed-settings.json` | `C:\Program Files\ClaudeCode\managed-settings.json` | `PreToolUse` |
| Codex | `/etc/codex/requirements.toml` | `%ProgramData%\OpenAI\Codex\requirements.toml` | `PreToolUse` |
| Cursor | `/Library/Application Support/Cursor/hooks.json` | `%ProgramData%\Cursor\hooks.json` | `beforeMCPExecution`, `preToolUse` |
| Kiro | `~/.kiro/hooks/obot-sentry.json` | `%USERPROFILE%\.kiro\hooks\obot-sentry.json` | `PreToolUse` |

VS Code is not supported for enforcement.

The hook works out which MCP server or tool the call targets, asks Obot for a verdict, and blocks the call unless Obot explicitly allows it. The hook also blocks the call when Obot is unreachable, takes longer than 5 seconds, or returns an answer the hook cannot read. It also blocks calls on a device that is not enrolled, so a device whose enrollment fails blocks every tool call in every enforced agent. When enforcement is turned off in Obot, the server allows every call.

An allowed call still goes through the agent's own approval prompts. The hook only withholds a denial and never approves a call.

An MCP server can be allowlisted by URL, by `npx` or `uvx` package, or by display name for a claude.ai connector. A server started from a local executable path has none of these, so its calls are always blocked. [docs/enforcement.md](docs/enforcement.md) covers how each agent names servers, which `npx` and `uvx` commands resolve to a package, and how to diagnose a block.

### Limits

Kiro's hook file is in the user's home folder, so the user can delete it. The hook job restores it within `HookIntervalMinutes`.

Kiro runs no hooks in a workspace the user has not trusted or in a window with no folder open. A folder trusted after Kiro starts runs no hooks until Kiro restarts ([kirodotdev/Kiro#11884](https://github.com/kirodotdev/Kiro/issues/11884)). The Kiro CLI runs hooks only on its v3 agent engine, `kiro-cli chat --v3` or `chat.agentEngine` set to `v3`. obot-sentry does not change the engine setting.

Claude Code reads only one managed settings source by default. If the device also receives Claude Code server-managed settings or an MDM policy, Claude Code ignores the managed settings file and its hooks. The MDM policy is the `com.anthropic.claudecode` preference domain on macOS and `HKLM\SOFTWARE\Policies\ClaudeCode` on Windows. `hook-install` does not detect this and still reports success. If you deploy either source, set `managedSourcesBehavior` to `merge` in the highest-ranked one.

With enforcement on, `hook-install` sets `"disableSideloadFlags": true` in Claude Code's managed settings file. Claude Code then refuses `--plugin-dir`, `--plugin-url`, `--agents`, `--mcp-config`, and the other ways to load a plugin for one session. Without this setting, a session plugin could pose as an installed plugin with the same name. `hook-install` and `hook-uninstall` never remove the setting, because the file does not record who set it. To allow those flags again, delete `disableSideloadFlags` from the managed settings file.

### Turning enforcement off

Set `EnforcementEnabled` to false, or run `hook-install --enforce=false`. The next hook run removes the enforcement hooks and keeps the audit hooks. A plain `hook-install` reads the configured value, so set it to false explicitly.

## Removing the hooks

Before you remove the binary, run this as root, or from an elevated PowerShell on Windows:

```sh
sudo obot-sentry hook-uninstall
```

`hook-uninstall` removes every hook whose command contains `--managed-by obot-sentry` from the machine-wide files and from every local user's files. It leaves other hooks in place. Stop the scheduled hook job or uninstall the package afterward, or the next hook run installs the hooks again. Restart the agents to unload the hooks. Each configuration's `INSTRUCTIONS.md` under `build/` has the per-OS steps.

Obot Sentry writes to these files:

| Agent | macOS | Windows |
|---|---|---|
| Claude Code | `/Library/Application Support/ClaudeCode/managed-settings.json` | `C:\Program Files\ClaudeCode\managed-settings.json` |
| Codex | `/etc/codex/requirements.toml` | `%ProgramData%\OpenAI\Codex\requirements.toml` |
| VS Code Copilot hook | `~/.copilot/hooks/obot-sentry.json` | `%USERPROFILE%\.copilot\hooks\obot-sentry.json` |
| VS Code settings | `~/Library/Application Support/Code/User/settings.json` | `%APPDATA%\Code\User\settings.json` |
| Cursor | `/Library/Application Support/Cursor/hooks.json` | `%ProgramData%\Cursor\hooks.json` |
| Kiro | `~/.kiro/hooks/obot-sentry.json` | `%USERPROFILE%\.kiro\hooks\obot-sentry.json` |

`hook-uninstall` does not delete files. It also leaves three settings that carry no marker, because their earlier values cannot be recovered: Codex's `[features]` pins, VS Code's `chat.hookFilesLocations` values, and Claude Code's `disableSideloadFlags`. Remove them by hand if you no longer want them.

## MDM packaging

`build/` holds the installers and the per-channel instructions:

- `build/windows/`: the MSI, built with WiX v4, and the Intune and manual channels.
- `build/macos/`: the pkg, the LaunchAgent and LaunchDaemon, the configuration and Full Disk Access profiles, and the Intune, Jamf Pro, and manual channels.
- `build/manifest.json`: the fields Obot's setup form asks for and the configurations it offers for download.

The installers carry no tenant settings. The admin supplies the server URL and enrollment key at deploy time, as MSI properties on Windows or a configuration profile on macOS.

CI builds the full asset tree on every push and publishes it as the `mdm-assets` workflow artifact. Obot reads the tree from `OBOT_SERVER_MDM_ASSET_SOURCE`, which accepts a local folder, a tar archive, or an HTTP(S) tarball URL.

## Development

```
make build          # bin/obot-sentry
make test
make validate-go-code
```

To scan against a local Obot server:

```
OBOT_SENTRY_SERVER_URL=http://localhost:8080 OBOT_SENTRY_ENROLLMENT_KEY=ode1-... bin/obot-sentry scan --submit --force
```

[docs/releasing.md](docs/releasing.md) covers building signed MDM assets, cutting a release, and rolling it out in Obot.
