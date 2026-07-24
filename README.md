# Obot Sentry 

`obot-sentry` is a command-line tool designed to be used by MDMs for device scanning and agent hook configuration.

It enrolls each machine with an [Obot](https://github.com/obot-platform/obot) server as a single device shared by all of the machine's users, submits device scan manifests attributed per user, and provides managed local-agent audit hooks. Inventory collection (MCP servers, skills, plugins) is currently stubbed out — scans ship an empty manifest so the enrollment and submission flow works end to end while the scan engine lands separately.

## How it works

- **Machine-wide scheduler entries.** The MDM installer registers a scan task that the OS runs *as each signed-in user*: on Windows it uses a `BUILTIN\Users` group principal (logon + a 10-minute poll). Each run is a plain `obot-sentry scan --submit --quiet` in the user's own session, so attribution, paths, and permissions are native — no privileged fan-out and no MDM per-user scheduling. The poll is cheap: obot-sentry throttles real submissions to the MDM-configured `ScanIntervalMinutes` (default 60, clamped to 15–1440) against its per-user scan state, so admins retune the cadence from the MDM alone. Users who aren't signed in aren't scanned; their inventory can't change while they're signed out, so nothing is missed beyond first-report latency for accounts that haven't signed in since install. The installer also registers an elevated SYSTEM task that runs `obot-sentry hook-install` at logon and hourly. Because `hook-install` targets only the active console user, it converges at each sign-in (after a one-minute settle) plus the install kick, with the hourly run picking up later drift and console-user switches.
- **Enrollment.** Configuration (server URL + an `ode1-...` enrollment credential) is pushed by the MDM — via registry values on Windows, a managed-preferences profile on macOS. On the machine's first scan — by whichever user runs first — obot-sentry generates a shared Ed25519 identity key in the machine-scoped data dir (`%PROGRAMDATA%\obot\obot-sentry` on Windows, `/Library/Application Support/obot/obot-sentry` on macOS; both are prepared user-writable by the installer, with a per-user fallback when unavailable) and enrolls the public key via `POST /api/mdm/enroll` (trust-on-first-use). The device ID derives from the machine ID + key fingerprint, so all users present one device — and a lost key simply mints a fresh device ID instead of a TOFU conflict. Each user's first scan re-enrolls the same identity, which is an idempotent update server-side.
- **Submission.** Every submission is authenticated with a short-lived self-signed JWT (`aud=obot/device`) verified server-side against the enrolled key; scans land via `POST /api/devices/scans`, attributed to the submitting user by the manifest's `username`. Local-agent audit logs land via `POST /api/local-agent-audit-logs`, with the server stamping authoritative device attribution from the JWT.
- **Local-agent audit hooks.** Managed hook configuration invokes the hidden `obot-sentry audit submit` command for supported local agents. The hook parser normalizes terminal tool-call events, submits them fail-open, and writes only warnings to stderr when enrollment or submission is unavailable so agent execution is not blocked.
- **Audit spool.** Transient audit-log submission failures are stored in an encrypted per-user spool under the obot-sentry cache directory and replayed after a later successful live submit. Server-side client errors are discarded instead of retried.
- **Scan state + logs.** Every scan run updates `scan.json` (last scan/submit times, status, last error) and appends a JSON record to `scan-logs/` — timestamp-sortable filenames, pruned by age and size — in obot-sentry's per-user cache dir (`%LOCALAPPDATA%\obot\obot-sentry` on Windows, `~/Library/Caches/obot/obot-sentry` on macOS). Support and MDM freshness checks read these; recording problems never fail a scan.

## Commands

```
obot-sentry scan              # build + print the manifest (add --submit to enroll + upload)
obot-sentry enroll            # explicit enrollment, for verifying a configuration
obot-sentry hook-install      # install managed local-agent audit hooks (root/Administrator)
obot-sentry version
```

`obot-sentry audit submit` is hidden because it is intended for managed local-agent hook configurations, not direct operator use. It submits with enrolled-device JWT authentication.

## Configuration

Resolution order per key: flags > env > MDM store.

| Key | Flag | Env | Windows registry / macOS managed prefs |
|-----|------|-----|----------------------------------------|
| Server URL | `--server-url` | `OBOT_SENTRY_SERVER_URL` | `ServerURL` |
| Enrollment key | `--enrollment-key` | `OBOT_SENTRY_ENROLLMENT_KEY` | `EnrollmentKey` |
| Scan interval (minutes) | `--scan-interval-minutes` | `OBOT_SENTRY_SCAN_INTERVAL_MINUTES` | `ScanIntervalMinutes` |

MDM stores: `HKLM\SOFTWARE\Obot\obot-sentry` on Windows; `/Library/Managed Preferences/com.obot.obot-sentry.plist` (fallback `/Library/Preferences/...`) on macOS.

`hook-install` writes the final MDM-owned executable path into every hook:
`/usr/local/bin/obot-sentry` on macOS and
`C:\Program Files\Obot\obot-sentry\obot-sentry.exe` on Windows. It fails before
changing hook files if the package has not installed a usable executable at
that location.

## Removing the hooks

`hook-install` has no uninstall subcommand, and neither packaging path
removes the hook configuration it writes: on Windows `msiexec /x` unregisters
the scheduled tasks but leaves the hooks already written into agent config
files, and on macOS there is no automated removal at all. Remove the hooks by
hand from each agent's configuration.

Every entry Obot Sentry writes carries the `--managed-by obot-sentry` marker
— the same signal `hook-install` uses to recognize and replace its own entries
on each run — so it is also how you identify what to delete. The managed files
are:

| Agent | macOS | Windows |
|---|---|---|
| Claude Code | `~/.claude/settings.json` | `%USERPROFILE%\.claude\settings.json` |
| Codex | `/etc/codex/requirements.toml` | `%ProgramData%\OpenAI\Codex\requirements.toml` |
| Copilot (VS Code) hook | `~/.copilot/hooks/obot-sentry.json` | `%USERPROFILE%\.copilot\hooks\obot-sentry.json` |
| Cursor | `/Library/Application Support/Cursor/hooks.json` | `%ProgramData%\Cursor\hooks.json` |
| VS Code settings | `~/Library/Application Support/Code/User/settings.json` | `%APPDATA%\Code\User\settings.json` |

The Copilot hook file is written solely by Obot Sentry, so it can be deleted
outright. The other four are shared with your own configuration: delete only
the hook entries whose command contains `--managed-by obot-sentry`, and in the
VS Code settings file also remove the `chat.hookFilesLocations` keys Obot
Sentry added (`~/.copilot/hooks`, `.claude/settings.json`,
`.claude/settings.local.json`, `~/.claude/settings.json`). The user-scoped
files exist once per signed-in user, so repeat for each user on a shared
machine. Restart the agents afterward to drop the hooks. Per-OS commands are in
the `INSTRUCTIONS.md` for each configuration under `build/`.

## MDM packaging

Everything MDM-related lives in `build/`, one directory per OS with the
MDM channels nested under the OS they deliver to:

```
build/
  manifest.json          # authored: schemaVersion, fields, configurations (${VERSION} tokens)
  mdm-assets.sh          # completes + sanity-checks the manifest, stages dist/mdm-assets/
  windows/               # the Windows installer (MSI, WiX v4)
    msi.ps1  obot-sentry.wxs  scan-task.ps1  hook-task.ps1  obot-sentry.ico
    intune/              # Intune channel: .intunewin wrap + instructions
      intunewin.ps1  INSTRUCTIONS.md.tmpl
    manual/              # manual channel: instructions for the MSI + exe
      INSTRUCTIONS.md.tmpl
  macos/                 # macOS assets (universal binary, built in CI)
    manual/              # manual channel: instructions for the standalone binary
      INSTRUCTIONS.md.tmpl
```

The installers are tenant-agnostic; per-tenant configuration (server URL
+ an enrollment key created in obot) is applied at install time as MSI
properties, so one installer serves every tenant. The Windows chain runs
**on Windows**: [obot-sentry.wxs](build/windows/obot-sentry.wxs) via
[WiX Toolset v4](https://docs.firegiant.com/wix/) (`dotnet tool install
--global wix`), wrapped by Microsoft's
[Win32 Content Prep Tool](https://github.com/microsoft/Microsoft-Win32-Content-Prep-Tool):

```
build\windows\msi.ps1 -Version 1.2.3 -Exe bin\obot-sentry.exe  # dist\obot-sentry.msi (version inside, name stable)
build\windows\intune\intunewin.ps1                         # dist\obot-sentry.intunewin
build/mdm-assets.sh 1.2.3                                  # dist/mdm-assets/ (any OS)
```

CI (`build.yaml`) runs the same chain — a Linux job builds the binaries
with GoReleaser (`.goreleaser.yaml`): the Windows exe plus the macOS
universal (amd64+arm64) binary, signed and notarized with
[quill](https://github.com/anchore/quill) without a macOS runner; the
Windows runner packages the MSI — and `make mdm` dispatches that
workflow on your fork and downloads the assembled `dist/mdm-assets`.

`mdm-assets.sh` produces the tree obot consumes: the platform installers
and instruction templates plus one `manifest.json`. The manifest is the
whole contract: `fields` is a JSON Schema obot renders the admin form from and
validates values against, and `configurations` lists the downloadable
(platform, OS) units — display names, descriptions, setup instructions,
and asset files included — so obot's wizard carries no platform
knowledge of its own. Obot renders the `*.tmpl` assets and serves each
unit as a zip. The enrollment key is never rendered — templates carry a
`REPLACE_WITH_ENROLLMENT_KEY` placeholder the admin fills in the MDM.

CI assembles the assets on every PR/push and publishes them as the
`mdm-assets` workflow artifact; releases add a tarball with a
cosign-signed checksums file. The Windows installers ship unsigned:
Intune installs them silently in SYSTEM context, so nothing gates on
Authenticode there. The macOS universal binary is Developer ID-signed
and notarized via quill when the `QUILL_*` secrets are available (the
same secrets obot's release workflow uses); PRs and forks fall back to
a dry run with an ad-hoc signature.
Obot reads the assembled tree via `OBOT_SERVER_MDM_ASSETS_PATH`.

| Platform | installer | scheduling | tenant config |
|---|---|---|---|
| Intune (Windows) | `.msi` wrapped as `.intunewin` | per-user scan task (logon + 10-min poll, submissions throttled to `ScanIntervalMinutes`) plus elevated SYSTEM hook-install task (logon + hourly) | MSI properties → `HKLM\SOFTWARE\Obot\obot-sentry` |
| Manual (macOS) | universal binary installed to `/usr/local/bin` | none — run `obot-sentry scan --submit` manually | `OBOT_SENTRY_*` environment variables |

## Development

```
make build          # bin/obot-sentry
make test
make validate-go-code
```

Local end-to-end against a dev server:

```
OBOT_SENTRY_SERVER_URL=http://localhost:8080 OBOT_SENTRY_ENROLLMENT_KEY=ode1-... bin/obot-sentry scan --submit
```
