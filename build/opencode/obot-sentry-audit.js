import { spawn } from "node:child_process"
import { accessSync, constants, lstatSync, realpathSync } from "node:fs"
import { tmpdir } from "node:os"
import { isAbsolute, relative, resolve } from "node:path"

// OpenCode plugin adapter for Obot Sentry.
//
// OpenCode's V1 and V2 plugin APIs expose tool execution hooks. The adapter
// keeps the plugin deliberately small and fail-open: it submits one normalized
// JSON payload to the installed Sentry binary, and never throws into OpenCode's
// tool execution path.
const MAX_PENDING = 256
const SUBMIT_TIMEOUT_MS = 5_500
const ENFORCEMENT_TIMEOUT_MS = 5_000
const ENFORCEMENT_ENV = "OBOT_SENTRY_OPENCODE_ENFORCEMENT"

function sanitizeToolPart(value) {
  return String(value).replace(/[^a-zA-Z0-9_-]/g, "_")
}

function within(promise, timeoutMs = 750) {
  return Promise.race([
    Promise.resolve(promise).catch(() => undefined),
    new Promise((resolve) => setTimeout(resolve, timeoutMs)),
  ])
}

async function configuredMcpServers(client, directory) {
  const names = new Set()
  const addNames = (value) => {
    if (!value || typeof value !== "object") return
    for (const name of Object.keys(value)) names.add(sanitizeToolPart(name))
  }

  try {
    const result = await within(client?.mcp?.status?.({ query: { directory } }))
    addNames(result?.data ?? result)
  } catch {
    // Status is an optimization for MCP classification. A failed lookup must
    // not prevent ordinary local-tool auditing.
  }

  // A plugin can load before MCP connections are established. Config names are
  // still useful for normalizing a tool name in that case.
  try {
    const result = await within(client?.config?.get?.())
    const config = result?.data ?? result
    addNames(config?.mcp)
  } catch {
    // Keep the status-derived names when config lookup is unavailable.
  }

  return [...names]
}

async function configuredMcpServersV2(ctx) {
  try {
    const result = await within(ctx?.mcp?.list?.())
    const entries = result?.data ?? result
    if (Array.isArray(entries)) {
      return entries
        .map((entry) => entry?.name)
        .filter(Boolean)
        .map(sanitizeToolPart)
    }
    if (entries && typeof entries === "object") return Object.keys(entries).map(sanitizeToolPart)
  } catch {
    // MCP name discovery is optional; local tool auditing must remain fail-open.
  }
  return []
}

function matchingMcpServer(value, servers) {
  return servers
    .filter((server) => server && value.startsWith(`${server}_`))
    .sort((a, b) => b.length - a.length)[0]
}

function mcpToolParts(name, mcpServers) {
  const value = String(name || "")
  if (/^mcp__/i.test(value)) {
    const body = value.slice(5)
    const server = mcpServers
      .filter((candidate) => candidate && body.startsWith(`${candidate}__`))
      .sort((a, b) => b.length - a.length)[0]
    if (server) {
      return { name: value, server, tool: body.slice(server.length + 2) }
    }
    const separator = body.indexOf("__")
    if (separator > 0) {
      return { name: value, server: body.slice(0, separator), tool: body.slice(separator + 2) }
    }
    return { name: value, server: "", tool: "" }
  }
  if (/^mcp[:_]/i.test(value)) return { name: value, server: "", tool: "" }

  const server = matchingMcpServer(value, mcpServers)
  if (!server) return { name: value, server: "", tool: "" }
  const prefix = `${server}_`
  return { name: `mcp__${server}__${value.slice(prefix.length)}`, server, tool: value.slice(prefix.length) }
}

function normalizedToolName(name, mcpServers) {
  return mcpToolParts(name, mcpServers).name
}

function isPathInside(candidate, parent) {
  const pathRelative = relative(parent, candidate)
  return pathRelative === "" || (pathRelative !== ".." && !pathRelative.startsWith(`..${process.platform === "win32" ? "\\" : "/"}`) && !isAbsolute(pathRelative))
}

function validateSentryExecutable(candidate) {
  if (typeof candidate !== "string" || !candidate.trim() || !isAbsolute(candidate.trim())) return null
  const requested = resolve(candidate.trim())
  let resolved
  try {
    resolved = realpathSync(requested)
    const info = lstatSync(requested)
    if (info.isSymbolicLink() || !info.isFile()) return null
    if (isPathInside(resolved, realpathSync(tmpdir()))) return null
    if (process.platform !== "win32") {
      accessSync(resolved, constants.X_OK)
      // A user-writable binary could be replaced between validation and spawn.
      // Refuse group/world-writable paths; the owner may be the current user for
      // an explicit non-MDM installation.
      // eslint-disable-next-line no-bitwise
      if ((info.mode & 0o022) !== 0) return null
    }
    return resolved
  } catch {
    return null
  }
}

function sentryExecutable() {
  const configured = process.env.OBOT_SENTRY_EXE
  if (configured) return validateSentryExecutable(configured)

  const fallback = process.platform === "win32"
    ? "C:\\Program Files\\Obot\\obot-sentry\\obot-sentry.exe"
    : "/usr/local/bin/obot-sentry"
  return validateSentryExecutable(fallback)
}

function callKey(input) {
  return `${input.sessionID || ""}\u0000${input.callID || input.tool || ""}`
}

function safeObject(value) {
  return value && typeof value === "object" ? value : {}
}

function isNonzeroExit(metadata) {
  const value = metadata.exit ?? metadata.exitCode ?? metadata.code
  if (typeof value === "number") return value !== 0
  if (typeof value === "string" && value.trim() !== "") return value !== "0"
  return false
}

function failureDetails(output) {
  const result = safeObject(output)
  const metadata = safeObject(result.metadata)
  const error =
    (typeof result.error === "string" && result.error) ||
    (typeof metadata.error === "string" && metadata.error) ||
    (typeof result.message === "string" && result.message) ||
    (typeof metadata.message === "string" && metadata.message) ||
    ""

  const failed =
    result.isError === true ||
    result.status === "error" ||
    result.status === "failed" ||
    metadata.status === "error" ||
    metadata.status === "failed" ||
    isNonzeroExit(metadata)

  return { failed, error }
}

function responsePayload(output) {
  if (output === undefined) return null
  if (typeof output === "string" || typeof output === "number" || typeof output === "boolean") {
    return output
  }

  const result = safeObject(output)
  // The documented V1 result shape has title/output/metadata. Generic MCP
  // tools in 1.18.29 can expose their raw result here instead, so preserve it
  // rather than dropping fields.
  if ("output" in result) {
    return {
      title: result.title ?? "",
      output: result.output ?? null,
      metadata: result.metadata ?? null,
    }
  }
  return result
}

function submit(payload, phase) {
  return new Promise((resolve) => {
    let child
    try {
      child = spawn(
        sentryExecutable(),
        [
          "audit",
          "submit",
          "--agent",
          "opencode",
          "--phase",
          phase,
          "--input",
          "-",
          "--managed-by",
          "obot-sentry",
        ],
        { stdio: ["pipe", "ignore", "ignore"], windowsHide: true },
      )
    } catch {
      resolve()
      return
    }

    let finished = false
    let timer
    const finish = () => {
      if (finished) return
      finished = true
      if (timer) clearTimeout(timer)
      resolve()
    }

    timer = setTimeout(() => {
      try {
        child.kill()
      } catch {
        // The process may already have exited.
      }
      finish()
    }, SUBMIT_TIMEOUT_MS)

    child.once("error", finish)
    child.once("close", finish)
    child.stdin.once("error", finish)

    try {
      child.stdin.end(JSON.stringify(payload))
    } catch {
      finish()
    }
  })
}

// isEnforcementEnabled keeps the permission bridge off unless an operator asks
// for it. The plugin option wins over the environment variable so a project can
// pin the behavior; neither is set by the audit install.
function isEnforcementEnabled(options) {
  const configured = options?.enforcement ?? options?.enforce
  if (configured !== undefined) return configured === true || configured === "true" || configured === "1"
  const value = String(process.env[ENFORCEMENT_ENV] || "").trim().toLowerCase()
  return value === "1" || value === "true" || value === "yes" || value === "on"
}

// mcpServerCandidates is deliberately absent from the enforcement payload.
// OpenCode sanitizes the server half into the permission action, so a name
// supplied here would be a guess: a caller could omit a longer allowlisted
// server and make the call resolve as a shorter one. Sentry enumerates the
// candidates from OpenCode's own configuration instead.

// requestEnforcement is the only part of this plugin that can block a call, so
// it is deliberately asymmetric: anything other than an explicit allow on a
// clean exit is a denial. A slow, crashed, or unparseable Sentry never releases
// a tool call.
function requestEnforcement(payload) {
  return new Promise((resolve) => {
    let child
    try {
      child = spawn(
        sentryExecutable(),
        ["enforce", "--agent", "opencode", "--event", "permission.evaluate", "--input", "-"],
        { stdio: ["pipe", "pipe", "pipe"], windowsHide: true },
      )
    } catch {
      resolve({ allow: false, reason: "Obot Sentry could not start" })
      return
    }

    let stdout = ""
    let finished = false
    let timer
    const finish = (result) => {
      if (finished) return
      finished = true
      if (timer) clearTimeout(timer)
      resolve(result)
    }

    child.stdout.on("data", (chunk) => {
      if (stdout.length < 64 * 1024) stdout += chunk.toString()
    })
    // stderr is diagnostics only and must never become a decision input.
    child.stderr.resume()
    child.once("error", () => finish({ allow: false, reason: "Obot Sentry could not start" }))
    child.once("close", (code) => {
      let response
      try {
        response = JSON.parse(stdout.trim())
      } catch {
        finish({ allow: false, reason: "Obot Sentry returned an invalid decision" })
        return
      }
      const effect = response?.effect ?? response?.decision
      if (code === 0 && effect === "allow") {
        finish({ allow: true, reason: "" })
        return
      }
      if (effect === "deny") {
        finish({ allow: false, reason: response?.message || response?.reason || "Obot denied the tool call" })
        return
      }
      finish({ allow: false, reason: "Obot Sentry returned an invalid decision" })
    })

    timer = setTimeout(() => {
      try {
        child.kill()
      } catch {
        // The process may already have exited.
      }
      finish({ allow: false, reason: "Obot Sentry decision timed out" })
    }, ENFORCEMENT_TIMEOUT_MS)

    try {
      child.stdin.on("error", () => finish({ allow: false, reason: "Obot Sentry could not read the request" }))
      child.stdin.end(JSON.stringify(payload))
    } catch {
      finish({ allow: false, reason: "Obot Sentry could not read the request" })
    }
  })
}

function makePayload({
  sessionID,
  callID,
  tool,
  args,
  response,
  cwd,
  timestamp,
  agentVersion,
  failed = false,
  error = "",
  toolErrorCode = "opencode_tool_failure",
  mcpServers = [],
}) {
  const target = mcpToolParts(tool, mcpServers)
  return {
    session_id: sessionID,
    call_id: callID,
    tool_name: target.name,
    opencode_tool_name: tool,
    opencode_tool_name_normalized: target.name,
    ...(target.server && target.tool ? { mcp_server: target.server, mcp_tool: target.tool } : {}),
    tool_input: args ?? {},
    tool_response: response,
    cwd,
    timestamp: timestamp ?? new Date().toISOString(),
    agent_version: agentVersion || "opencode",
    ...(failed
      ? {
          error: error || "OpenCode tool reported a failure",
          tool_error_code: toolErrorCode,
        }
      : {}),
  }
}

// V1 hooks are used by OpenCode 1.18.x. The V2 setup below uses the same
// payload shape but subscribes to the newer ctx.tool hook API.
async function v1Server({ client, directory }, submitPayload = submit) {
  const mcpServers = await configuredMcpServers(client, directory)
  const pending = new Map()

  return {
    "tool.execute.before": async (input, output) => {
      const key = callKey(input)
      pending.set(key, {
        args: safeObject(output).args ?? {},
        timestamp: new Date().toISOString(),
      })

      // A tool can be interrupted before its after hook. Bound memory without
      // affecting the active call; the oldest pending record is no longer
      // useful once its call has left the normal execution window.
      while (pending.size > MAX_PENDING) {
        const oldest = pending.keys().next().value
        if (oldest === undefined) break
        pending.delete(oldest)
      }
    },

    "tool.execute.after": async (input, output) => {
      const key = callKey(input)
      const before = pending.get(key)
      pending.delete(key)

      // The task hook in OpenCode 1.18.29 can call after with undefined for a
      // failed subtask. There is no result to audit in that case.
      if (output === undefined) return

      const details = failureDetails(output)
      const payload = makePayload({
        sessionID: input.sessionID,
        callID: input.callID,
        tool: input.tool,
        args: input.args ?? before?.args ?? {},
        response: responsePayload(output),
        cwd: directory,
        timestamp: before?.timestamp,
        agentVersion: process.env.OPENCODE_VERSION || "opencode",
        failed: details.failed,
        error: details.error,
        mcpServers,
      })

      // Waiting for the helper keeps the event reliable while the helper's
      // five-second timeout and fail-open behavior bound the impact on the
      // agent. Any helper failure is intentionally swallowed.
      try {
        await submitPayload(payload, details.failed ? "failure" : "post-tool")
      } catch {
        // Audit submission is deliberately fail-open.
      }
    },
  }
}

async function v2Setup(ctx, submitPayload = submit, decidePayload = requestEnforcement) {
  if (!ctx?.tool?.hook) return

  const mcpServers = await configuredMcpServersV2(ctx)
  const pending = new Map()
  const directory = ctx.location?.directory || process.cwd()
  const agentVersion = ctx.app?.version || "opencode"

  await ctx.tool.hook("execute.before", (event) => {
    const key = `${event.sessionID || ""}\u0000${event.id || event.tool || ""}`
    pending.set(key, {
      args: event.input ?? {},
      timestamp: new Date().toISOString(),
    })

    while (pending.size > MAX_PENDING) {
      const oldest = pending.keys().next().value
      if (oldest === undefined) break
      pending.delete(oldest)
    }
  })

  await ctx.tool.hook("execute.after", async (event) => {
    const key = `${event.sessionID || ""}\u0000${event.id || event.tool || ""}`
    const before = pending.get(key)
    pending.delete(key)
    const failed = event.status === "error"
    const result = failed ? null : event.result
    const details = failureDetails(result)
    const error = failed
      ? event.error?.message || details.error || "OpenCode tool reported a failure"
      : details.error
    const errorCode = event.error?.metadata?.code ?? event.error?.metadata?.name
    const payload = makePayload({
      sessionID: event.sessionID,
      callID: event.id,
      tool: event.tool,
      args: event.input ?? before?.args ?? {},
      response: result,
      cwd: directory,
      timestamp: before?.timestamp,
      agentVersion,
      failed: failed || details.failed,
      error,
      toolErrorCode: errorCode == null ? "opencode_tool_failure" : String(errorCode),
      mcpServers,
    })

    try {
      await submitPayload(payload, failed || details.failed ? "failure" : "post-tool")
    } catch {
      // Audit submission is deliberately fail-open.
    }
  })

  if (!isEnforcementEnabled(ctx.options)) return
  if (!ctx.permission?.hook) {
    // Fail closed at load time rather than silently running audit-only while an
    // operator believes enforcement is on.
    throw new Error("OpenCode V2 enforcement is enabled but permission.evaluate is unavailable")
  }

  await ctx.permission.hook("evaluate", async (event) => {
    // An ordinary configured deny never reaches this hook, so an incoming deny
    // here is already final.
    if (event.effect === "deny") return

    let decision
    try {
      // Only the action and the working directory are sent. The public
      // enforcement request is parameter-free by design, so arguments,
      // resources, metadata, and call identifiers never reach Obot.
      decision = await decidePayload({
        action: event.action,
        cwd: directory,
      })
    } catch {
      decision = { allow: false, reason: "Obot Sentry enforcement failed" }
    }

    if (decision?.allow !== true) {
      event.effect = "deny"
      event.message = decision?.reason || "Obot denied the tool call"
      return
    }

    // An Obot allow must never remove OpenCode's own confirmation prompt, so an
    // incoming ask stays ask and an incoming allow stays allow.
    if (event.effect === "allow" || event.effect === "ask") return
    event.effect = "deny"
    event.message = "OpenCode reported an unsupported permission effect"
  })
}

// Named exports keep the adapter's pure normalization and hook builders
// testable without spawning a real Sentry process. OpenCode consumes only the
// default export below.
export {
  failureDetails,
  makePayload,
  mcpToolParts,
  normalizedToolName,
  responsePayload,
  submit,
  validateSentryExecutable,
  v1Server,
  v2Setup,
}

// One default export supports both generations: OpenCode 1.18.x calls server(),
// while V2 reads id/setup().
export default {
  id: "obot-sentry-audit",
  setup: v2Setup,
  server: v1Server,
}
