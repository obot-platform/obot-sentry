# OpenCode audit adapter

`obot-sentry-audit.js` is a local OpenCode plugin that forwards completed tool
calls to the installed Obot Sentry binary. It supports both the OpenCode 1.x
`server()` hook API and the OpenCode V2 `setup()`/`ctx.tool.hook()` API. It is
intentionally separate from Sentry's native client hook installer because
OpenCode exposes a plugin API rather than the hook files used by Claude Code,
Codex, Cursor, and VS Code.

The versioned Sentry release bundle includes this adapter at
`opencode/obot-sentry-audit.js`, but it remains an explicit user-installed
plugin. Sentry does not silently modify OpenCode's global plugin directory.

## Requirements

- OpenCode 1.18.29 or another OpenCode 1.x release with the V1
  `tool.execute.before` and `tool.execute.after` plugin hooks, or OpenCode V2
  with the `ctx.tool.hook("execute.before"/"execute.after")` API.
- An enrolled Obot Sentry installation. On Windows the default executable is
  `C:\Program Files\Obot\obot-sentry\obot-sentry.exe`.

Set `OBOT_SENTRY_EXE` when Sentry is installed elsewhere. The adapter accepts
only an absolute, existing, regular executable outside the operating system
temporary directory; on Unix the binary must be executable and not
group/world-writable. Invalid overrides are ignored and audit submission remains
fail-open.

## Install globally

Copy the adapter into OpenCode's global plugin directory and restart OpenCode:

```powershell
$source = "C:\path\to\obot-sentry\build\opencode\obot-sentry-audit.js"
$target = "$HOME\.config\opencode\plugins\obot-sentry-audit.js"
New-Item -ItemType Directory -Force (Split-Path $target) | Out-Null
Copy-Item $source $target -Force
```

The global plugin directory is auto-discovered; no change to `opencode.jsonc` is
required. The Sentry `hook-install`/`hook-uninstall` commands do not manage this
external plugin file; remove it separately when uninstalling OpenCode auditing.

On macOS/Linux, copy the release asset to the same global directory:

```sh
mkdir -p "${XDG_CONFIG_HOME:-$HOME/.config}/opencode/plugins"
cp opencode/obot-sentry-audit.js \
  "${XDG_CONFIG_HOME:-$HOME/.config}/opencode/plugins/obot-sentry-audit.js"
```

## Behavior

The plugin records the arguments and result of each completed tool call and
submits a normalized payload to:

```text
obot-sentry audit submit --agent opencode --phase post-tool
```

OpenCode names MCP tools as `<server>_<tool>`. When the connected MCP server
list is available, the adapter rewrites that name to Sentry's
`mcp__<server>__<tool>` convention so the server and tool are visible in the
Obot audit log. The original name is retained in the raw event.

Calls that return an error-shaped result (for example a non-zero shell exit or
an MCP result with `isError: true`) use the `failure` phase. Submission is
fail-open and bounded to 5.5 seconds so an unavailable Obot server never blocks
OpenCode.

OpenCode 1.x does not run `tool.execute.after` when a tool throws or rejects.
Those exceptional calls cannot be captured by the V1 adapter; failures returned
normally as tool results are captured. OpenCode V2 reports rejected tools
through its `execute.after` error event and those failures are captured too. The
adapter does not modify tool arguments or results.

## V2 enforcement (opt-in)

Audit is always on for the installed adapter. Enforcement is separate and is
**off unless an operator turns it on**, either with the plugin option
`{"enforcement": true}` or with the `OBOT_SENTRY_OPENCODE_ENFORCEMENT=1`
environment variable. The plugin option wins when both are set.

When enabled on OpenCode V2, the adapter also registers
`ctx.permission.hook("evaluate", …)` and asks Obot before each permission
evaluation:

```text
obot-sentry enforce --agent opencode --event permission.evaluate
```

The bridge is deny-only. It never sets `event.effect = "allow"`, so an Obot
allow leaves OpenCode's own `ask` prompt and `allow` decision exactly as they
were. It sets `event.effect = "deny"` only when Obot refuses, when the Sentry
helper cannot start, times out, exits non-zero, or returns anything other than a
well-formed allow, and when the call's MCP target cannot be resolved.

The payload sent to Sentry contains only the permission `action` and the working
`cwd`. Arguments, resources, metadata, session identifiers, and MCP server names
are not forwarded: the public enforcement request is parameter-free by design,
and a caller-supplied server name would be an untrustworthy identity hint.

### Coverage and limits

- V1 (1.18.29) remains audit-only. There is no V1 decision point.
- The V2 hook fires for OpenCode's permission-aware built-ins and MCP tools. It
  does not fire for tools that perform no permission check, including some
  management tools and arbitrary plugin-defined tools. Those are not enforced.
- An OpenCode action that is not a known built-in is treated as an MCP tool and
  is denied unless exactly one configured MCP server in OpenCode's own
  configuration explains it. A new OpenCode built-in is denied until it is added
  to `openCodeBuiltinKinds` in `pkg/enforce/opencode.go`.
- Obot's `EnforcementEnabled` fleet setting still applies. With enforcement
  disabled fleet-side, Obot allows unconditionally, so the bridge only withholds
  denials; the local unresolved-target rule is the one check that still refuses.

## Test

From the `obot-sentry` repository, run:

```powershell
node --test build/opencode/obot-sentry-audit.test.mjs
```
