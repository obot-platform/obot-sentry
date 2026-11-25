# Tool-call enforcement reference

The [README](../README.md#tool-call-enforcement) explains what the enforcement hook does. This page covers how the hook identifies MCP servers, which affects what an allowlist entry has to say.

## Allowlisting

An MCP server can be allowlisted by URL, by `npx` or `uvx` package, or by display name for a claude.ai connector.

A stdio server started from a local executable path, such as `/opt/homebrew/bin/thing` or `./node_modules/.bin/thing`, has no URL and no registry package. The hook reports its calls as unidentified and blocks them. To allow such a server, run it from a package or a URL.

Each agent names servers differently:

- Codex replaces punctuation in server names with underscores, so a config key of `probe-npx-stdio` arrives as `probe_npx_stdio`. The hook matches the name back to the config key and reports the key. Copy server names from the decision log, not from the tool name.
- Cursor display names can carry a scope prefix, such as `user-probe-uvx-stdio`. A name declared in more than one Cursor scope is unidentified, because the payload does not say which one ran. Rename one of them.
- Kiro lowercases tool names, as in `mcp_<server>_<tool>`. The hook matches the server back to its config key, but the tool name stays lowercase, such as `createissue`, so copy tool names from the decision log. A tool name that two configured servers could have produced is unidentified. For example, `mcp_github_enterprise_x` is unidentified when both `github` and `github_enterprise` are configured.

## `npx` and `uvx` packages

The hook resolves a stdio command to an exact source, name, and version, or reports it as unidentified. The command must be exactly `npx` or `uvx`, or `npx.cmd`, `npx.exe`, `uvx.cmd`, or `uvx.exe` on Windows. A command with a path separator, or any other runner, is unidentified.

The hook refuses flags that change which code runs, because the package name would no longer identify the code. These include `npx -c`, `--registry`, `--node-options`, `uvx --with`, `--index-url`, `--from git+...`, and package extras. It accepts flags that cannot change the package: `-y`, `-q`, `--offline`, `npx -p` or `--package`, `uvx --from`, and `uvx -p` or `--python`. Note that `-p` means `--package` to `npx` and `--python` to `uvx`.

Environment variables follow the same rule. An MCP entry is unidentified when its `env` redirects the runner or the package source, or injects code. Examples are `PATH`, `NODE_OPTIONS`, `NPM_CONFIG_*`, `COREPACK_*`, `UV_*`, `PYTHONPATH`, and dynamic-loader variables. The hook accepts variables that the server only reads as input, such as `GITHUB_TOKEN`, and never sends their values to Obot. The hook also refuses identity-changing variables in its own environment. It accepts an ordinary inherited `PATH`, including one that finds `npx` or `uvx` under Homebrew or another per-user install.

Package names are normalized the way each registry does it, both on the device and when an allowlist entry is saved. PyPI names follow PEP 503, so `Mcp_Server.Git` becomes `mcp-server-git`. npm names are lowercased with the scope kept, so `@Scope/Pkg` becomes `@scope/pkg`. Versions are reported as written. An allowlist entry with no version matches every version.

| Command | Resolves to |
|---|---|
| `npx -y @modelcontextprotocol/server-github` | npm `@modelcontextprotocol/server-github`, any version |
| `npx -y linear-mcp@1.2.3` | npm `linear-mcp` 1.2.3 |
| `npx -p some-pkg -y other` | npm `some-pkg`, because `-p` takes precedence in npx |
| `uvx mcp-server-git` | PyPI `mcp-server-git` |
| `uvx awslabs.core-mcp-server@latest` | PyPI `awslabs-core-mcp-server` `latest` |
| `uvx --from 'pkg==1.4.0' entry` | PyPI `pkg` 1.4.0 |
| `uvx -p 3.11 mcp-server-git` | PyPI `mcp-server-git`, since `-p` pins Python |
| `npx -c 'foo && bar'` | Unidentified, because it runs an arbitrary command |
| `npx --registry https://x/ pkg` | Unidentified, because it changes where the code comes from |
| `uvx --with pandas pkg` | Unidentified, because it installs more than the named package |
| `uvx --from git+https://...` | Unidentified, because it is not a registry package |
| `uvx --from 'pkg[all]' entry` | Unidentified, because it requests extras |
| `/opt/homebrew/bin/uvx pkg` | Unidentified, because it is a path |
| `uv tool run pkg`, `node server.js`, `docker run ...` | Unidentified, because the runner is not supported |

## Diagnosing a block

`obot-sentry enforce resolve` uses the same resolver as the hook. It prints every configuration source the hook would read, in order, and what it found. It exits with a non-zero status when it cannot identify the server.

```
$ obot-sentry enforce resolve --agent claude-code --server linear --server-source project
1  /Users/me/proj/.mcp.json  mcpServers["linear"]  FOUND
server name: linear
resolved: https://mcp.linear.app/mcp
```

An unidentified server prints the reason on the last line:

```
server name: local-binary
unresolved: stdio command "/opt/homebrew/bin/some-server" is a path, not a bare package runner
```

## Claude Code servers

Claude Code's hook payload has an `mcp_server` field with the server's config key in `name` and the scope it was loaded from in `source`. Pass these to `enforce resolve` as `--server` and `--server-source`. The hook reads only that scope and matches the key exactly:

| `source` | The hook reads |
|---|---|
| `user` | `mcpServers` in `.claude.json` |
| `local` | The project's entry in `.claude.json`, keyed by the git root or by a linked worktree's main checkout |
| `project` | Every `.mcp.json` from the session's start folder upward, where a deeper file wins |
| `plugin` | The installed plugins in `plugins/installed_plugins.json` |
| `enterprise` | `managed-mcp.json` |
| `managed` | `managedMcpServers` in the managed settings files |
| `claudeai` | Nothing. The connector's display name is its identity. |

If `~/.claude/.config.json` exists, Claude Code reads `user` and `local` servers from it instead of `.claude.json`, and so does the hook.

The hook leaves these servers unresolved, so their calls are blocked:

- Servers from `--mcp-config`, the Agent SDK, or an agent definition, whose sources are `dynamic`, `sdk`, and `agent`. Claude Code's built-in servers are the exception and are reported by name.
- Every MCP call from a Claude Code release before 2.1.274, which is the first to send `mcp_server`.
- `local` and `project` servers when the hook runs without `CLAUDE_PROJECT_DIR`.
- A server whose file Claude Code may have loaded but the hook cannot decode, such as a file nested more than 10000 levels deep or a `.mcp.json` that is not an object. The hook does not let another file answer for it.

`local` and `project` servers are read for the folder the session started or resumed in, which Claude Code passes to every hook as `CLAUDE_PROJECT_DIR`. A session keeps that configuration after it changes folders. Pass `--project-dir` to reproduce a hook call. Without it, `enforce resolve` uses the current folder. Claude Code parses its files as strict JSON, and so does the hook, so a file with comments or trailing commas is skipped.

### Plugins

The hook resolves plugin servers only while the managed settings set `disableSideloadFlags`. Otherwise a plugin loaded for one session could pose as an installed plugin with the same name.

Every installation of a plugin counts, whatever its scope. Claude Code loads a plugin installed for one project in any session whose settings enable it, so one project's committed settings can enable another project's installation. If installations define the server differently, the hook leaves it unresolved.

A plugin's servers come from its `.mcp.json` and from `mcpServers` in its `.claude-plugin/plugin.json`. A plugin with no `plugin.json` uses its marketplace entry as its manifest. The hook then also reads the entry's `mcpServers` from Claude Code's copy of `marketplace.json` under `plugins/marketplaces`, and those servers override the plugin's `.mcp.json`. A plugin from a marketplace added from a local folder or catalog file is read from that folder, which is where Claude Code loads it.

The hook leaves a plugin server unresolved when `mcpServers` names something it does not read, such as an `.mcpb` bundle, or when the marketplace entry declares a server Claude Code would reject.

### `CLAUDE_CONFIG_DIR`

Claude Code reads its managed settings file from a fixed path, so starting it with a different `CLAUDE_CONFIG_DIR` does not skip the hooks. The hooks also run when an administrator sets `allowManagedHooksOnly`. On Windows the path is always on drive `C:`, even when Program Files has been moved, and `hook-install` writes there.

When a session sets `CLAUDE_CONFIG_DIR`, the hook reads plugin servers from `$CLAUDE_CONFIG_DIR/plugins`, where Claude Code loads them from. The hook blocks user and local servers in such a session and says why. Claude Code reads them from the `.claude.json` of the folder it started with, but its settings can change the variable for its hooks afterward, and the hook cannot tell the two apart. Project and managed servers and claude.ai connectors are unaffected. The variable must be an absolute path. Claude Code resolves a relative path against the folder it started in, which the hook cannot know, so the hook blocks MCP calls from such a session.

Releases of Obot Sentry before managed settings support installed Claude Code's hooks in `~/.claude/settings.json`. `hook-install` removes those entries when it installs the managed ones.
