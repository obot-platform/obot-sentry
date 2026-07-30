# Obot Sentry 

`obot-sentry` is a command-line tool designed to be used by MDMs for device scanning and agent hook configuration.

It enrolls each machine with an [Obot](https://github.com/obot-platform/obot) server as a single device shared by all of the machine's users, submits device scan manifests attributed per user, and provides managed local-agent audit hooks. Inventory collection (MCP servers, skills, plugins) is currently stubbed out — scans ship an empty manifest so the enrollment and submission flow works end to end while the scan engine lands separately.

## How it works

- **Machine-wide scheduler entries.** The MDM installer registers a scan task that the OS runs *as each signed-in user*: on Windows it uses a `BUILTIN\Users` group principal (logon + a 10-minute poll). Each run is a plain `obot-sentry scan --submit --quiet` in the user's own session, so attribution, paths, and permissions are native — no privileged fan-out and no MDM per-user scheduling. The poll is cheap: obot-sentry throttles real submissions to the MDM-configured `ScanIntervalMinutes` (default 60, clamped to 15–1440) against its per-user scan state, so admins retune the cadence from the MDM alone. Users who aren't signed in aren't scanned; their inventory can't change while they're signed out, so nothing is missed beyond first-report latency for accounts that haven't signed in since install. The installer also registers an elevated SYSTEM task that runs `obot-sentry hook-install` at logon and hourly. Because `hook-install` targets only the active console user, it converges at each sign-in (after a one-minute settle) plus the install kick, with the hourly run picking up later drift and console-user switches.
- **Enrollment.** Configuration (server URL + an `ode1-...` enrollment credential) is pushed by the MDM — via registry values on Windows, a managed-preferences profile on macOS. On the machine's first scan — by whichever user runs first — obot-sentry generates a shared Ed25519 identity key in the machine-scoped data dir (`%PROGRAMDATA%\obot\obot-sentry` on Windows, `/Library/Application Support/obot/obot-sentry` on macOS; both are prepared user-writable by the installer, with a per-user fallback when unavailable) and enrolls the public key via `POST /api/mdm/enroll` (trust-on-first-use). The device ID derives from the machine ID + key fingerprint, so all users present one device — and a lost key simply mints a fresh device ID instead of a TOFU conflict. Each user's first scan re-enrolls the same identity, which is an idempotent update server-side.
- **Submission.** Every submission is authenticated with a short-lived self-signed JWT (`aud=obot/device`) verified server-side against the enrolled key; scans land via `POST /api/devices/scans`, attributed to the submitting user by the manifest's `username`. Local-agent audit logs land via `POST /api/local-agent-audit-logs`, with the server stamping authoritative device attribution from the JWT.
- **Local-agent audit hooks.** Managed hook configuration invokes the hidden `obot-sentry audit submit` command for supported local agents. The hook parser normalizes terminal tool-call events, submits them fail-open, and writes only warnings to stderr when enrollment or submission is unavailable so agent execution is not blocked.
- **Tool-call enforcement hooks.** With enforcement enabled in Obot, managed pre-tool hook configuration invokes the hidden `obot-sentry enforce` command for Claude Code, Codex, and Cursor. It is the opposite of the audit hook in every important way: synchronous, fail-**closed**, and stdout is the hook protocol channel rather than something that must stay empty. See [Tool-call enforcement](#tool-call-enforcement).
- **Audit spool.** Transient audit-log submission failures are stored in an encrypted per-user spool under the obot-sentry cache directory and replayed after a later successful live submit. Server-side client errors are discarded instead of retried.
- **Scan state + logs.** Every scan run updates `scan.json` (last scan/submit times, status, last error) and appends a JSON record to `scan-logs/` — timestamp-sortable filenames, pruned by age and size — in obot-sentry's per-user cache dir (`%LOCALAPPDATA%\obot\obot-sentry` on Windows, `~/Library/Caches/obot/obot-sentry` on macOS). Support and MDM freshness checks read these; recording problems never fail a scan.

## Commands

```
obot-sentry scan              # build + print the manifest (add --submit to enroll + upload)
obot-sentry enroll            # explicit enrollment, for verifying a configuration
obot-sentry hook-install      # install managed local-agent hooks (root/Administrator)
                              #   --enforce also installs the tool-call enforcement hooks
obot-sentry version
```

`obot-sentry audit submit` and `obot-sentry enforce` are hidden because they are intended for managed local-agent hook configurations, not direct operator use. Both authenticate as the enrolled device. `obot-sentry enforce resolve` is the one part of the enforcement command meant to be run by hand — see [Tool-call enforcement](#tool-call-enforcement).

## Configuration

Resolution order per key: flags > env > MDM store.

| Key | Flag | Env | Windows registry / macOS managed prefs |
|-----|------|-----|----------------------------------------|
| Server URL | `--server-url` | `OBOT_SENTRY_SERVER_URL` | `ServerURL` |
| Enrollment key | `--enrollment-key` | `OBOT_SENTRY_ENROLLMENT_KEY` | `EnrollmentKey` |
| Scan interval (minutes) | `--scan-interval-minutes` | `OBOT_SENTRY_SCAN_INTERVAL_MINUTES` | `ScanIntervalMinutes` |
| Enforcement enabled | `hook-install --enforce` | `OBOT_SENTRY_ENFORCEMENT_ENABLED` | `EnforcementEnabled` |

`EnforcementEnabled` is a boolean and every store spells it differently, so all
of these are accepted: a real plist boolean, `1`/`0` from a `REG_DWORD`,
`true`/`false` from a `REG_SZ`, and `yes`/`no`/`on`/`off`. A value that is not
recognized is treated as absent rather than as false, so a typo falls through to
the next layer instead of quietly disabling the control. Absent everywhere means
enforcement is off.

On Windows the registry value is what turns enforcement on for a device that is
already installed: the hook scheduled task registers a fixed `hook-install`
argument list, so the MSI property (`ENFORCEMENT=1`) or a pushed `REG_DWORD` is
the only way in.

MDM stores: `HKLM\SOFTWARE\Obot\obot-sentry` on Windows; `/Library/Managed Preferences/com.obot.obot-sentry.plist` (fallback `/Library/Preferences/...`) on macOS.

`hook-install` writes the final MDM-owned executable path into every hook:
`/usr/local/bin/obot-sentry` on macOS and
`C:\Program Files\Obot\obot-sentry\obot-sentry.exe` on Windows. It fails before
changing hook files if the package has not installed a usable executable at
that location.

## Tool-call enforcement

`hook-install --enforce` adds a **pre-tool** hook to Claude Code, Codex, and
Cursor, on top of the post-tool audit hooks. Before each tool call runs, the hook
resolves what the call targets, asks obot for a verdict, and blocks the call
unless the answer is an explicit allow.

| Agent | File | Events added |
|---|---|---|
| Claude Code | `~/.claude/settings.json` | `PreToolUse` |
| Codex | `/etc/codex/requirements.toml` (macOS), `%ProgramData%\OpenAI\Codex\requirements.toml` | `PreToolUse` |
| Cursor | `/Library/Application Support/Cursor/hooks.json` (macOS), `%ProgramData%\Cursor\hooks.json` | `beforeMCPExecution`, `preToolUse` |

> Visual Studio Code is not supported for enforcement.

### It fails closed

Anything other than an explicit allow blocks the call: obot unreachable, slow
(the whole check is budgeted at 5 seconds), or returning something unparseable; a
device that is not enrolled or has no server configured; a payload the hook cannot
read; an MCP server it cannot identify. **A device whose enrollment never
completed blocks every tool call in all three agents until enrollment succeeds.**
`hook-install` provisions the machine identity before writing any hook file, so
the normal MDM path cannot land there — but a wiped identity directory or a
revoked enrollment key can, and it will be loud.

Turning enforcement off in obot stops the blocking immediately, for all devices: the
server allows every call when enforcement is disabled, and logs nothing.

### It never grants permission

A permitted call is answered by *withholding* the denial, not by approving the
call — zero bytes for Claude Code and Codex, and Cursor's `{"permission":"allow"}`,
which means "this hook does not object". Your agents' own approval prompts still
apply to everything enforcement allows. The allowlist and an agent's permission
model are separate controls and this deliberately does not collapse them.

### What can and cannot be allowlisted

An MCP server can be allowlisted by **URL**, by its **`npx`/`uvx` package**, or —
for a claude.ai account connector — by **display name**.

A stdio server started from a **local executable path** (`/opt/homebrew/bin/thing`,
`./node_modules/.bin/thing`) presents none of those: no URL, no registry package,
nothing an allowlist entry can name. Such a call is reported as unidentified and
**blocked by design**. If a decision log is full of unresolvable stdio servers,
this is why, and the fix is to run those servers from a package or a URL rather
than to look for an allowlist entry that can match them.

Two naming caveats that change what an allowlist entry has to say:

- **Codex reports server names with punctuation folded to underscores.** A config
  key of `probe-npx-stdio` arrives as `probe_npx_stdio`. The device matches it back
  to the key and reports the key, so copy the name from the decision log rather
  than from the tool name.
- **Cursor display names may carry a scope prefix** (`user-probe-uvx-stdio`), and a
  name declared in more than one Cursor scope is reported as unidentified — the
  payload cannot say which one ran. Rename one of them.

### `npx` / `uvx` package resolution

The device resolves a stdio command to a package identity itself, strictly: it
either produces an exact `(source, name, version)` or reports the call as
unidentified. The command must be exactly `npx` or `uvx` (plus `.cmd`/`.exe` on
Windows); any path separator, and any other runner, is unidentified.

Flags that change *what code runs* are refused — `npx -c`, `--registry`,
`--node-options`, `uvx --with`, `--index-url`, `--from git+…`, package extras —
because the package name would no longer identify the code. Flags that cannot
change identity are accepted: `-y`, `-q`, `--offline`, `npx -p/--package`,
`uvx --from`, and `uvx -p/--python` (note `-p` means `--package` to npx and
`--python` to uvx).

Names are canonicalized the way each registry does, on the device and again when
an allowlist entry is saved, so the two agree: PyPI by PEP 503
(`Mcp_Server.Git` → `mcp-server-git`), npm by lowercasing with the scope
preserved (`@Scope/Pkg` → `@scope/pkg`). Versions are reported verbatim, and an
allowlist entry with no version matches any version.

| Command | Resolves to |
|---|---|
| `npx -y @modelcontextprotocol/server-github` | npm `@modelcontextprotocol/server-github`, any version |
| `npx -y linear-mcp@1.2.3` | npm `linear-mcp` 1.2.3 |
| `npx -p some-pkg -y other` | npm `some-pkg` (`-p` wins, as in npx) |
| `uvx mcp-server-git` | pypi `mcp-server-git` |
| `uvx awslabs.core-mcp-server@latest` | pypi `awslabs-core-mcp-server` `latest` |
| `uvx --from 'pkg==1.4.0' entry` | pypi `pkg` 1.4.0 |
| `uvx -p 3.11 mcp-server-git` | pypi `mcp-server-git` (a Python pin, not a package) |
| `npx -c 'foo && bar'` | unidentified — runs an arbitrary command |
| `npx --registry https://x/ pkg` | unidentified — redirects where the code comes from |
| `uvx --with pandas pkg` | unidentified — brings in more than the named package |
| `uvx --from git+https://…` | unidentified — not a registry package |
| `uvx --from 'pkg[all]' entry` | unidentified — extras |
| `/opt/homebrew/bin/uvx pkg` | unidentified — a path, not a bare runner |
| `uv tool run pkg`, `node server.js`, `docker run …` | unidentified — unsupported runner |

### Diagnosing a block

`enforce resolve` prints every configuration source the hook would consult, in
order, and what it concluded — from the same resolver the hook uses, so a trace
that says FOUND is evidence about production behavior. It exits non-zero when the
server cannot be identified.

```
$ obot-sentry enforce resolve --agent claude-code --server linear
1  /Library/Application Support/ClaudeCode/managed-mcp.json  absent
2  /Users/me/proj/.mcp.json  absent
3  /Users/me/.claude.json  projects["/Users/me/proj"]  no match
4  /Users/me/.claude.json  mcpServers["linear"]  FOUND
server name: linear
resolved: npm / linear-mcp / any version
```

An unidentified server prints the reason on the last line and exits 1:

```
server name: local-binary
unresolved: stdio command "/opt/homebrew/bin/some-server" is a path, not a bare package runner
```

### Tamper resistance, stated plainly

The Codex and Cursor hook files are machine-scoped and administrator-owned. The
Claude Code hook file is **user-scoped** (`~/.claude/settings.json`), so the user
whose calls are being enforced can delete it. The hourly `hook-install` task
re-converges it, so the bypass window is bounded by that interval rather than
permanent. We intend to find a machine-scoped solution for Claude Code in the future.

### Turning it off

Set `EnforcementEnabled` to false (or drop `--enforce`) and the next
`hook-install` converges the audit hooks and **leaves the pre-tool entries on
disk exactly as it found them** — the merge is per hook event, so an event that is
not being installed is never touched. That is safe because when enforcement is
disabled, every tool call is allowed: a stale hook costs one round trip per tool
call and blocks nothing. Remove the entries with the steps below when you want
them gone.

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
the hook entries whose command contains `--managed-by obot-sentry` — the marker
is on every entry Obot Sentry writes, including the enforcement entries under
`PreToolUse`, `beforeMCPExecution`, and `preToolUse`, and in the
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
