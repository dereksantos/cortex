import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync, chmodSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { PluginInput } from "@opencode-ai/plugin";
import type { ToolContext } from "@opencode-ai/plugin/tool";
import { CortexPlugin } from "../plugins/cortex.ts";

// ---- test fixtures ---------------------------------------------------------

function fakePluginInput(overrides: Partial<PluginInput> = {}): PluginInput {
  return {
    client: {} as PluginInput["client"],
    project: {} as PluginInput["project"],
    directory: "/fake/project",
    worktree: "/fake/project",
    experimental_workspace: { register() {} },
    serverUrl: new URL("http://localhost:1234"),
    $: {} as PluginInput["$"],
    ...overrides,
  };
}

function fakeToolContext(overrides: Partial<ToolContext> = {}): ToolContext {
  return {
    sessionID: "ses_test",
    messageID: "msg_test",
    agent: "build",
    directory: "/fake/project",
    worktree: "/fake/project",
    abort: new AbortController().signal,
    metadata: () => {},
    ask: (() => {}) as unknown as ToolContext["ask"],
    ...overrides,
  };
}

// ---- registration / shape --------------------------------------------------

test("CortexPlugin returns Hooks with cortex_recall registered", async () => {
  const hooks = await CortexPlugin(fakePluginInput());
  assert.ok(hooks.tool, "Hooks.tool must be present");
  assert.ok(hooks.tool.cortex_recall, "cortex_recall must be registered");
  assert.strictEqual(typeof hooks.tool.cortex_recall.execute, "function");
});

test("cortex_recall description directs the LLM toward situational use", async () => {
  const hooks = await CortexPlugin(fakePluginInput());
  const desc = hooks.tool!.cortex_recall.description;
  assert.match(desc, /context/i);
  assert.match(desc, /decisions|patterns|corrections|constraints/i);
  assert.match(desc, /do not act on them unconditionally/i);
});

test("cortex_recall.args declares both query (required) and limit (optional)", async () => {
  const hooks = await CortexPlugin(fakePluginInput());
  const args = hooks.tool!.cortex_recall.args;
  assert.ok(args.query, "args.query must be present");
  assert.ok(args.limit, "args.limit must be present");
  // Validate the schema actually enforces what we claim.
  const { tool } = await import("@opencode-ai/plugin/tool");
  const schema = tool.schema.object(args);
  assert.ok(schema.safeParse({ query: "hello" }).success, "query alone should validate");
  assert.ok(schema.safeParse({ query: "hello", limit: 10 }).success, "limit in range should validate");
  assert.ok(!schema.safeParse({ query: "hello", limit: 0 }).success, "limit below min must reject");
  assert.ok(!schema.safeParse({ query: "hello", limit: 51 }).success, "limit above max must reject");
  assert.ok(!schema.safeParse({ limit: 5 }).success, "missing query must reject");
});

// ---- execute happy path ----------------------------------------------------

test("cortex_recall.execute shells out flags-first and renders JSON as markdown", async () => {
  const hooks = await CortexPlugin(fakePluginInput());
  const dir = mkdtempSync(join(tmpdir(), "opencode-cortex-recall-"));
  const fakeCortex = join(dir, "cortex-fake");
  // The fake validates the FLAGS-BEFORE-POSITIONAL gotcha: Go flag.Parse
  // stops at the first non-flag, so `cortex search <query> --format json`
  // would silently fall back to text. The fake hard-fails on bad order.
  writeFileSync(
    fakeCortex,
    `#!/bin/sh
if [ "$1" != "search" ] || [ "$2" != "--format" ] || [ "$3" != "json" ] || [ "$4" != "--limit" ]; then
  echo "BAD ARG ORDER: $*" >&2
  exit 2
fi
cat <<'EOF'
[
  {
    "id": "ev-1",
    "content": "Use pgx not database/sql.",
    "score": 0.91,
    "captured_at": "2026-05-10T18:04:00Z",
    "category": "decision"
  }
]
EOF
`,
    "utf8",
  );
  chmodSync(fakeCortex, 0o755);

  const prior = process.env.CORTEX_BINARY;
  try {
    process.env.CORTEX_BINARY = fakeCortex;
    const result = await hooks.tool!.cortex_recall.execute(
      { query: "pgx", limit: 3 },
      fakeToolContext({ directory: dir }),
    );
    // ToolResult shape: {output, metadata} (NOT a content array)
    assert.strictEqual(typeof result, "object");
    const r = result as { output: string; metadata?: Record<string, unknown> };
    assert.match(r.output, /Found 1 relevant context item:/);
    assert.match(r.output, /Use pgx not database\/sql/);
    assert.strictEqual(r.metadata?.count, 1);
    assert.strictEqual(r.metadata?.binary, fakeCortex);
  } finally {
    if (prior === undefined) delete process.env.CORTEX_BINARY;
    else process.env.CORTEX_BINARY = prior;
    rmSync(dir, { recursive: true, force: true });
  }
});

// ---- execute error path ----------------------------------------------------

test("cortex_recall.execute returns benign output when binary errors (never throws)", async () => {
  const hooks = await CortexPlugin(fakePluginInput());
  const prior = process.env.CORTEX_BINARY;
  try {
    process.env.CORTEX_BINARY = "/nonexistent/cortex-binary-for-test";
    const result = await hooks.tool!.cortex_recall.execute(
      { query: "anything", limit: 5 },
      fakeToolContext(),
    );
    const r = result as { output: string; metadata?: Record<string, unknown> };
    assert.match(r.output, /No relevant context captured yet/);
    assert.match(r.output, /Proceed without recalled context/);
    assert.strictEqual(typeof r.metadata?.error, "string");
  } finally {
    if (prior === undefined) delete process.env.CORTEX_BINARY;
    else process.env.CORTEX_BINARY = prior;
  }
});

test("cortex_recall.execute returns benign output when cortex returns non-array JSON", async () => {
  const hooks = await CortexPlugin(fakePluginInput());
  const dir = mkdtempSync(join(tmpdir(), "opencode-cortex-bad-json-"));
  const fakeCortex = join(dir, "cortex-fake");
  writeFileSync(fakeCortex, `#!/bin/sh\necho '{"not":"an array"}'\n`, "utf8");
  chmodSync(fakeCortex, 0o755);

  const prior = process.env.CORTEX_BINARY;
  try {
    process.env.CORTEX_BINARY = fakeCortex;
    const result = await hooks.tool!.cortex_recall.execute(
      { query: "x", limit: 5 },
      fakeToolContext({ directory: dir }),
    );
    const r = result as { output: string; metadata?: Record<string, unknown> };
    assert.match(r.output, /No relevant context captured yet/);
    assert.match(r.metadata?.error as string, /non-array/i);
  } finally {
    if (prior === undefined) delete process.env.CORTEX_BINARY;
    else process.env.CORTEX_BINARY = prior;
    rmSync(dir, { recursive: true, force: true });
  }
});
