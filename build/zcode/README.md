# Obot Sentry for ZCode

The Sentry release asset contains a platform-specific ZCode personal
marketplace archive:

- Windows: `obot-sentry-zcode-windows.zip`
- macOS: `obot-sentry-zcode-macos.zip`

Each archive preserves the `marketplace.json` and plugin directory structure.
Installation is explicit: Sentry never writes a user's ZCode configuration.

## Requirements

- ZCode 3.14 or newer
- An enrolled Obot Sentry installation
- The platform archive matching the device OS

The plugin invokes the installed Sentry executable through ZCode's hook
executors. `PreToolUse` is a fail-closed launcher that maps a missing or failed
helper to exit code 2 (ZCode's blocking shortcut); the audit hooks invoke
Sentry directly with `--defer` and never block. The default paths are:

- Windows: `C:\Program Files\Obot\obot-sentry\obot-sentry.exe`
- macOS: `/usr/local/bin/obot-sentry`

If Sentry is installed elsewhere, use the corresponding platform archive with
its command path adjusted before installing, or provide a managed package
variant with the durable absolute path.

## Install

1. Extract the platform archive so the directory containing `marketplace.json`
   is available locally.
2. In ZCode open **Settings → Plugins → Create → Add marketplace**.
3. Select the extracted directory.
4. Install and enable **obot-sentry**.
5. Start a new ZCode session. Hook configuration is captured at session start.

The plugin does not modify `~/.zcode/cli/config.json`, `.zcode/config.json`, or
`.agents/mcp.json`.

## Behavior

- `PreToolUse` synchronously invokes:

  ```text
  obot-sentry enforce --agent zcode --event PreToolUse --input -
  ```

- The Windows archive uses ZCode's `command` executor for `PreToolUse` so the
  whole launcher line reaches `cmd.exe` intact. The `process` executor re-quotes
  each argument for the Windows C runtime, which strips the quoting `cmd.exe`
  needs around a path such as `C:\Program Files\...` and leaves the hook unable
  to find the helper. The macOS archive uses `process` with `/bin/sh -c`, where
  arguments are passed through verbatim.
- An empty/allow decision leaves ZCode's own permission flow unchanged.
- A deny or any helper/configuration/timeout failure blocks the call. Sentry's
  deny response is the strict ZCode `permissionDecision: deny` shape; an
  unusable helper exits with the blocking status.
- `PostToolUse` and `PostToolUseFailure` invoke `audit submit --defer`. Sentry
  normalizes the event, writes it to its durable local spool, and returns
  without waiting for the server. A later scheduled scan/audit drains the
  spool.
- Unknown MCP targets and unreadable ZCode configuration fail closed during
  enforcement.
- ZCode workspace hook files are read for Inventory but are not used as the
  plugin execution source; the plugin's standard `hooks/hooks.json` is the
  execution source.

## Uninstall

Disable or uninstall **obot-sentry** from ZCode's plugin manager. The native
Sentry `hook-uninstall` command does not manage this external plugin and does
not modify ZCode's user configuration.
