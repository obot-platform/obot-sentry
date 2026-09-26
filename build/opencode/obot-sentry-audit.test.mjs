import assert from "node:assert/strict"
import test from "node:test"
import plugin, {
  failureDetails,
  makePayload,
  mcpToolParts,
  normalizedToolName,
  responsePayload,
  submit,
  validateSentryExecutable,
  v1Server,
  v2Setup,
} from "./obot-sentry-audit.js"

test("exports both OpenCode plugin entrypoints", () => {
  assert.equal(plugin.id, "obot-sentry-audit")
  assert.equal(typeof plugin.setup, "function")
  assert.equal(typeof plugin.server, "function")
})

test("refuses a Sentry executable that cannot be validated", () => {
  // A relative path, an empty value, and a file that does not exist all resolve
  // to null, which submit and requestEnforcement treat as "no helper available".
  assert.equal(validateSentryExecutable(""), null)
  assert.equal(validateSentryExecutable("relative/obot-sentry.exe"), null)
  assert.equal(validateSentryExecutable("C:/definitely/missing/obot-sentry.exe"), null)
})

test("normalizes OpenCode MCP tool names without changing explicit namespaces", () => {
  assert.equal(normalizedToolName("docs_search", ["docs"]), "mcp__docs__search")
  assert.equal(normalizedToolName("mcp__docs__search", ["docs"]), "mcp__docs__search")
  assert.equal(normalizedToolName("github_enterprise_search", ["github", "github_enterprise"]), "mcp__github_enterprise__search")
  assert.deepEqual(mcpToolParts("github_enterprise_search", ["github", "github_enterprise"]), {
    name: "mcp__github_enterprise__search",
    server: "github_enterprise",
    tool: "search"
  })
  assert.equal(normalizedToolName("custom_tool", ["docs"]), "custom_tool")
})

test("recognizes error-shaped tool results", () => {
  assert.deepEqual(failureDetails({ isError: true, error: "failed" }), {
    failed: true,
    error: "failed",
  })
  assert.deepEqual(failureDetails({ metadata: { exitCode: 1 } }), {
    failed: true,
    error: "",
  })
  assert.deepEqual(failureDetails({ output: "ok" }), { failed: false, error: "" })
})

test("preserves generic tool response fields", () => {
  const response = { content: [{ type: "text", text: "ok" }], metadata: { requestID: "r1" } }
  assert.deepEqual(responsePayload(response), response)
})

test("V2 setup registers tool lifecycle hooks", async () => {
  const hooks = []
  await plugin.setup({
    app: { version: "2.0.16" },
    location: { directory: "C:/work/project" },
    mcp: { list: async () => ({ data: [] }) },
    tool: {
      hook: async (name, callback) => {
        hooks.push({ name, callback })
      },
    },
  })

  assert.deepEqual(
    hooks.map(({ name }) => name),
    ["execute.before", "execute.after"],
  )
})

function enforcementContext(hooks, { enable = true } = {}) {
  const ctx = {
    app: { version: "2.0.16" },
    location: { directory: "C:/work/project" },
    mcp: { list: async () => ({ data: { github: {} } }) },
    tool: {
      hook: async (name, callback) => {
        hooks.push({ name, callback })
      },
    },
  }
  if (enable) {
    ctx.options = { enforcement: true }
    ctx.permission = {
      hook: async (name, callback) => {
        hooks.push({ name, callback })
      },
    }
  }
  return ctx
}

test("V2 enforcement stays unregistered until it is explicitly enabled", async () => {
  const hooks = []
  await v2Setup(enforcementContext(hooks, { enable: false }), async () => {}, async () => ({ allow: true }))
  assert.deepEqual(hooks.map(({ name }) => name), ["execute.before", "execute.after"])
})

test("V2 enforcement sends only the action and cwd to Sentry", async () => {
  const hooks = []
  const decisions = []
  await v2Setup(
    enforcementContext(hooks),
    async () => {},
    async (payload) => {
      decisions.push(payload)
      return { allow: false, reason: "blocked by policy" }
    },
  )

  const evaluate = hooks.find(({ name }) => name === "evaluate").callback
  const event = {
    sessionID: "s-enforce",
    agent: "build",
    action: "github_search",
    resources: ["*"],
    metadata: { token: "secret" },
    source: { id: "call-1" },
    effect: "ask",
  }
  await evaluate(event)

  // A caller-supplied server name would be an untrustworthy hint, and the public
  // decision request is parameter-free, so nothing else may be forwarded.
  assert.deepEqual(Object.keys(decisions[0]).sort(), ["action", "cwd"])
  assert.equal(decisions[0].action, "github_search")
  assert.equal(decisions[0].cwd, "C:/work/project")
  assert.equal(event.effect, "deny")
  assert.equal(event.message, "blocked by policy")
})

test("V2 enforcement preserves an OpenCode ask on an Obot allow", async () => {
  const hooks = []
  await v2Setup(enforcementContext(hooks), async () => {}, async () => ({ allow: true }))

  const evaluate = hooks.find(({ name }) => name === "evaluate").callback
  const event = { sessionID: "s-ask", action: "shell", resources: [], effect: "ask" }
  await evaluate(event)
  assert.equal(event.effect, "ask")
  assert.equal(event.message, undefined)
})

test("V2 enforcement leaves an incoming allow untouched", async () => {
  const hooks = []
  await v2Setup(enforcementContext(hooks), async () => {}, async () => ({ allow: true }))

  const evaluate = hooks.find(({ name }) => name === "evaluate").callback
  const event = { sessionID: "s-allow", action: "read", resources: [], effect: "allow" }
  await evaluate(event)
  assert.equal(event.effect, "allow")
})

test("V2 enforcement fails closed when the decision bridge throws", async () => {
  const hooks = []
  await v2Setup(
    enforcementContext(hooks),
    async () => {},
    async () => {
      throw new Error("unreachable")
    },
  )

  const evaluate = hooks.find(({ name }) => name === "evaluate").callback
  const event = { sessionID: "s-error", action: "read", resources: [], effect: "allow" }
  await evaluate(event)
  assert.equal(event.effect, "deny")
  assert.match(event.message, /enforcement failed/)
})

test("V2 enforcement denies when the bridge returns anything but an explicit allow", async () => {
  const hooks = []
  await v2Setup(enforcementContext(hooks), async () => {}, async () => undefined)

  const evaluate = hooks.find(({ name }) => name === "evaluate").callback
  const event = { sessionID: "s-undefined", action: "read", resources: [], effect: "allow" }
  await evaluate(event)
  assert.equal(event.effect, "deny")
})

test("V2 enforcement refuses to load when the V2 permission API is missing", async () => {
  const ctx = enforcementContext([], { enable: false })
  ctx.options = { enforcement: true }
  await assert.rejects(
    () => v2Setup(ctx, async () => {}, async () => ({ allow: true })),
    /permission\.evaluate is unavailable/,
  )
})

test("V1 server returns legacy tool hooks", async () => {
  const hooks = await plugin.server({
    client: {},
    directory: "C:/work/project",
  })

  assert.equal(typeof hooks["tool.execute.before"], "function")
  assert.equal(typeof hooks["tool.execute.after"], "function")
})

test("V1 hooks normalize MCP tools and submit failures", async () => {
  const submissions = []
  const hooks = await v1Server(
    {
      client: {
        mcp: { status: async () => ({ data: { docs: { status: "connected" } } }) },
        config: { get: async () => ({ mcp: { docs: {} } }) },
      },
      directory: "C:/work/project",
    },
    async (payload, phase) => {
      submissions.push({ payload, phase })
    },
  )

  await hooks["tool.execute.before"](
    { tool: "docs_search", args: { query: "x" }, sessionID: "s1", callID: "c1" },
    { skip: true },
  )
  await hooks["tool.execute.after"](
    { tool: "docs_search", sessionID: "s1", callID: "c1" },
    { isError: true, error: "boom" },
  )

  assert.equal(submissions[0].payload.opencode_tool_name, "docs_search")
  assert.equal(submissions[0].payload.opencode_tool_name_normalized, "mcp__docs__search")
  assert.equal(submissions[0].phase, "failure")
  assert.equal(submissions[0].payload.error, "boom")
})

test("V2 hooks normalize MCP tools and submit error events", async () => {
  const hooks = []
  const submissions = []
  await v2Setup(
    {
      app: { version: "2.0.16" },
      location: { directory: "C:/work/project" },
      mcp: { list: async () => ({ data: { docs: {} } }) },
      tool: {
        hook: async (name, callback) => {
          hooks.push({ name, callback })
        },
      },
    },
    async (payload, phase) => {
      submissions.push({ payload, phase })
    },
  )

  const before = hooks.find(({ name }) => name === "execute.before").callback
  const after = hooks.find(({ name }) => name === "execute.after").callback
  await before({
    sessionID: "s1",
    id: "c1",
    tool: "docs_search",
    input: { query: "x" },
  })
  await after({
    sessionID: "s1",
    id: "c1",
    tool: "docs_search",
    status: "error",
    error: { message: "boom" },
  })

  assert.equal(submissions[0].payload.opencode_tool_name, "docs_search")
  assert.equal(submissions[0].payload.opencode_tool_name_normalized, "mcp__docs__search")
  assert.equal(submissions[0].phase, "failure")
  assert.equal(submissions[0].payload.error, "boom")
})

test("hook submission remains fail-open when the submitter rejects", async () => {
  const hooks = await v1Server(
    {
      client: { mcp: { status: async () => ({}) } },
      directory: "C:/work/project",
    },
    async () => {
      throw new Error("unreachable")
    },
  )

  await hooks["tool.execute.before"]({ tool: "read", args: {}, sessionID: "s", callID: "c" }, {})
  await hooks["tool.execute.after"]({ tool: "read", sessionID: "s", callID: "c" }, { output: "ok" })
})

test("makePayload keeps a stable raw tool name alongside normalized names", () => {
  const payload = makePayload({
    sessionID: "s",
    callID: "c",
    tool: "docs_search",
    args: { query: "x" },
    response: { output: "ok" },
    cwd: "C:/work",
    mcpServers: ["docs"],
  })
  assert.equal(payload.opencode_tool_name, "docs_search")
  assert.equal(payload.opencode_tool_name_normalized, "mcp__docs__search")
})

test("submit resolves even when the helper is missing", async () => {
  const previous = process.env.OBOT_SENTRY_EXE
  process.env.OBOT_SENTRY_EXE = "C:/definitely/missing/obot-sentry.exe"
  try {
    await submit({ tool: "read" }, "post-tool")
  } finally {
    if (previous === undefined) delete process.env.OBOT_SENTRY_EXE
    else process.env.OBOT_SENTRY_EXE = previous
  }
})
