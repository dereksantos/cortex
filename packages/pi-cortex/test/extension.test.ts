import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync, chmodSync, readFileSync, rmSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import cortexExtension, {
  buildCapturePayload,
  formatRecallResults,
  redactSecrets,
  resolveCortexBinary,
  summarizeResultContent,
  type PiToolCallCapture,
  type RecallEntry,
} from "../extensions/cortex/index.ts";

type RegisteredTool = {
  name: string;
  label: string;
  description: string;
  parameters: {
    type: string;
    properties: Record<string, { type: string; description?: string; default?: unknown; minimum?: number; maximum?: number }>;
    required?: string[];
  };
  execute: (
    toolCallId: string,
    params: Record<string, unknown>,
    signal: unknown,
    onUpdate: unknown,
    ctx: unknown,
  ) => Promise<{ content: Array<{ type: string; text: string }>; details: Record<string, unknown> }>;
};

type Hook = { event: string; handler: (e: unknown, ctx: unknown) => void };

function loadExtensionAndCapture(): {
  tools: RegisteredTool[];
  hooks: Hook[];
} {
  const tools: RegisteredTool[] = [];
  const hooks: Hook[] = [];
  const fakePi = {
    registerTool: (tool: RegisteredTool) => {
      tools.push(tool);
    },
    on: (event: string, handler: (e: unknown, ctx: unknown) => void) => {
      hooks.push({ event, handler });
    },
    registerCommand: () => {},
  };
  cortexExtension(fakePi as unknown as Parameters<typeof cortexExtension>[0]);
  return { tools, hooks };
}

// ---- registration ----------------------------------------------------------

test("cortex_recall registers exactly once with the real pi v0.74 API shape", () => {
  const { tools } = loadExtensionAndCapture();
  assert.strictEqual(tools.length, 1);
  assert.strictEqual(tools[0].name, "cortex_recall");
  assert.strictEqual(tools[0].label, "Cortex Recall");
});

test("schema declares required query and optional bounded limit", () => {
  const { tools } = loadExtensionAndCapture();
  const tool = tools[0];
  assert.strictEqual(tool.parameters.properties.query.type, "string");
  assert.strictEqual(tool.parameters.properties.limit.type, "number");
  assert.strictEqual(tool.parameters.properties.limit.minimum, 1);
  assert.strictEqual(tool.parameters.properties.limit.maximum, 50);
  assert.deepStrictEqual(tool.parameters.required, ["query"]);
});

test("factory hooks tool_result exactly once (TODO 7)", () => {
  const { hooks } = loadExtensionAndCapture();
  const toolResultHooks = hooks.filter((h) => h.event === "tool_result");
  assert.strictEqual(toolResultHooks.length, 1, "exactly one tool_result hook");
  // Other events not hooked yet.
  const otherHooks = hooks.filter((h) => h.event !== "tool_result");
  assert.strictEqual(otherHooks.length, 0, "no other event hooks yet");
});

// ---- resolveCortexBinary ---------------------------------------------------

test("resolveCortexBinary prefers $CORTEX_BINARY over default", () => {
  const prior = process.env.CORTEX_BINARY;
  try {
    process.env.CORTEX_BINARY = "/tmp/some/cortex";
    assert.strictEqual(resolveCortexBinary(), "/tmp/some/cortex");
    process.env.CORTEX_BINARY = "  /padded/path  ";
    assert.strictEqual(resolveCortexBinary(), "/padded/path");
    delete process.env.CORTEX_BINARY;
    assert.strictEqual(resolveCortexBinary(), "cortex");
    process.env.CORTEX_BINARY = "";
    assert.strictEqual(resolveCortexBinary(), "cortex");
  } finally {
    if (prior === undefined) {
      delete process.env.CORTEX_BINARY;
    } else {
      process.env.CORTEX_BINARY = prior;
    }
  }
});

// ---- formatRecallResults ---------------------------------------------------

test("formatRecallResults: empty / non-array input returns the no-results sentence", () => {
  assert.strictEqual(formatRecallResults([]), "No relevant context captured yet.");
  assert.strictEqual(
    formatRecallResults(null as unknown as RecallEntry[]),
    "No relevant context captured yet.",
  );
});

test("formatRecallResults: pluralizes header and omits 0% scores", () => {
  const text = formatRecallResults([
    { id: "1", content: "A", score: 0.9, captured_at: "2026-05-10T18:04:00Z" },
    { id: "2", content: "B", score: 0, captured_at: "2026-05-10T18:04:00Z" },
  ]);
  assert.match(text, /Found 2 relevant context items:/);
  assert.match(text, /^1\. .*A$/m);
  assert.match(text, /^2\. B$/m);
  assert.ok(!/_\(0%\)_/.test(text));
});

// ---- redactSecrets ---------------------------------------------------------

test("redactSecrets drops fields whose key matches the secret-key pattern", () => {
  const input = {
    file_path: "/tmp/x",
    api_key: "AKIA12345",
    auth_token: "abc",
    client_secret: "xyz",
    user_id: "alice",
  };
  const out = redactSecrets(input) as Record<string, unknown>;
  assert.strictEqual(out.api_key, "<REDACTED>");
  assert.strictEqual(out.auth_token, "<REDACTED>");
  assert.strictEqual(out.client_secret, "<REDACTED>");
  // Non-matching fields pass through unchanged.
  assert.strictEqual(out.file_path, "/tmp/x");
  assert.strictEqual(out.user_id, "alice");
});

test("redactSecrets replaces secret-shaped substrings in string values", () => {
  const input = {
    command: "export OPENROUTER=sk-or-v1-deadbeef12345 && curl …",
    other: "no secrets here",
  };
  const out = redactSecrets(input) as Record<string, unknown>;
  assert.match(out.command as string, /<REDACTED>/);
  assert.ok(!/sk-or-v1-deadbeef12345/.test(out.command as string));
  assert.strictEqual(out.other, "no secrets here");
});

test("redactSecrets traverses nested objects and arrays", () => {
  const input = {
    headers: {
      "X-Api-Key": "secret123",
      "Content-Type": "application/json",
    },
    args: [{ api_key: "inner-secret" }, "literal sk-ant-deadbeef-xyz-string"],
  };
  const out = redactSecrets(input) as { headers: Record<string, unknown>; args: unknown[] };
  // Note: header key "X-Api-Key" doesn't match the regex (anchored, lowercase
  // base name); only `api_key` / `*_token` / `*_secret` do. Behavior is
  // documented in SECRET_KEY_PATTERN — false negatives are acceptable iff
  // the value pattern catches actual key strings, which it does for the
  // sk-or-… / sk-ant-… shapes we know about.
  assert.strictEqual(out.headers["X-Api-Key"], "secret123"); // key doesn't match
  assert.strictEqual(out.headers["Content-Type"], "application/json");
  assert.strictEqual((out.args[0] as Record<string, unknown>).api_key, "<REDACTED>");
  assert.match(out.args[1] as string, /<REDACTED>/);
});

// ---- summarizeResultContent ------------------------------------------------

test("summarizeResultContent: empty / non-array returns empty string", () => {
  assert.strictEqual(summarizeResultContent([]), "");
  assert.strictEqual(summarizeResultContent(null), "");
  assert.strictEqual(summarizeResultContent({ x: 1 }), "");
});

test("summarizeResultContent: returns first text content as-is when short", () => {
  const out = summarizeResultContent([{ type: "text", text: "short result" }]);
  assert.strictEqual(out, "short result");
});

test("summarizeResultContent: truncates long content and appends ellipsis", () => {
  const long = "x".repeat(600);
  const out = summarizeResultContent([{ type: "text", text: long }]);
  assert.strictEqual(out.length, 501); // 500 chars + 1 ellipsis char
  assert.ok(out.endsWith("…"));
});

// ---- buildCapturePayload ---------------------------------------------------

test("buildCapturePayload returns null for tools not on the allowlist", () => {
  assert.strictEqual(buildCapturePayload("read", {}, []), null);
  assert.strictEqual(buildCapturePayload("glob", { pattern: "*.ts" }, []), null);
  assert.strictEqual(buildCapturePayload("ls", { path: "/tmp" }, []), null);
});

test("buildCapturePayload conforms to the schema for allowlisted tools", () => {
  for (const tool of ["edit", "write", "bash", "cortex_recall"]) {
    const payload = buildCapturePayload(
      tool,
      { command: "echo hello" },
      [{ type: "text", text: "hello\n" }],
    );
    assert.ok(payload !== null, `${tool} should produce a payload`);
    const p = payload as PiToolCallCapture;
    assert.strictEqual(p.tool_name, tool);
    assert.deepStrictEqual(p.args_redacted, { command: "echo hello" });
    assert.strictEqual(p.result_summary, "hello\n");
    assert.match(p.captured_at, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/);
    assert.strictEqual(p.session_id, undefined);
  }
});

test("buildCapturePayload includes session_id when provided", () => {
  const payload = buildCapturePayload("bash", { command: "ls" }, [], "sess-42");
  assert.strictEqual(payload?.session_id, "sess-42");
});

test("buildCapturePayload redacts secret-shaped args before emitting", () => {
  const payload = buildCapturePayload(
    "bash",
    { command: "AUTH_TOKEN=sk-or-v1-abc123def456 curl https://api" },
    [],
  );
  const cmd = payload?.args_redacted.command as string;
  assert.match(cmd, /<REDACTED>/);
  assert.ok(!/sk-or-v1-abc123def456/.test(cmd));
});

// ---- end-to-end: factory + tool_result hook + real cortex stub -------------

test("tool_result hook spawns cortex capture for allowlisted tools (smoke)", async () => {
  const { hooks } = loadExtensionAndCapture();
  const toolResultHandler = hooks.find((h) => h.event === "tool_result")?.handler;
  assert.ok(toolResultHandler, "tool_result hook must be registered");

  const dir = mkdtempSync(join(tmpdir(), "cortex-capture-test-"));
  const fakeCortex = join(dir, "cortex-fake-capture");
  const outFile = join(dir, "capture.log");
  // Tiny script that just records its own argv to outFile so we can
  // assert pi-cortex shelled out with the right args.
  writeFileSync(
    fakeCortex,
    `#!/bin/sh
echo "$@" > "${outFile}"
`,
    "utf8",
  );
  chmodSync(fakeCortex, 0o755);

  const prior = process.env.CORTEX_BINARY;
  try {
    process.env.CORTEX_BINARY = fakeCortex;
    toolResultHandler(
      {
        type: "tool_result",
        toolCallId: "id-1",
        toolName: "bash",
        input: { command: "echo hi" },
        content: [{ type: "text", text: "hi" }],
        isError: false,
      },
      {},
    );
    // shellCapture is fire-and-forget; give the child time to land.
    const start = Date.now();
    while (!existsSync(outFile) && Date.now() - start < 3000) {
      await delay(50);
    }
    assert.ok(existsSync(outFile), "fake cortex must have run within 3s");
    const captured = readFileSync(outFile, "utf8");
    assert.match(captured, /^capture --type pi_tool_call --content /);
    assert.match(captured, /"tool_name":"bash"/);
    assert.match(captured, /"args_redacted":\{"command":"echo hi"\}/);
    assert.match(captured, /"result_summary":"hi"/);
  } finally {
    if (prior === undefined) {
      delete process.env.CORTEX_BINARY;
    } else {
      process.env.CORTEX_BINARY = prior;
    }
    rmSync(dir, { recursive: true, force: true });
  }
});

test("tool_result hook does NOT spawn capture for unlisted tools", async () => {
  const { hooks } = loadExtensionAndCapture();
  const toolResultHandler = hooks.find((h) => h.event === "tool_result")?.handler;
  assert.ok(toolResultHandler);

  const dir = mkdtempSync(join(tmpdir(), "cortex-capture-skip-"));
  const fakeCortex = join(dir, "cortex-fake-skip");
  const outFile = join(dir, "capture.log");
  writeFileSync(fakeCortex, `#!/bin/sh\necho "$@" > "${outFile}"\n`, "utf8");
  chmodSync(fakeCortex, 0o755);

  const prior = process.env.CORTEX_BINARY;
  try {
    process.env.CORTEX_BINARY = fakeCortex;
    for (const skip of ["read", "glob", "ls", "find", "grep"]) {
      toolResultHandler(
        {
          type: "tool_result",
          toolCallId: "id-skip",
          toolName: skip,
          input: {},
          content: [],
          isError: false,
        },
        {},
      );
    }
    await delay(300); // give any (unwanted) spawn time to write
    assert.ok(!existsSync(outFile), "fake cortex must NOT have been invoked for unlisted tools");
  } finally {
    if (prior === undefined) {
      delete process.env.CORTEX_BINARY;
    } else {
      process.env.CORTEX_BINARY = prior;
    }
    rmSync(dir, { recursive: true, force: true });
  }
});

// ---- cortex_recall.execute (regression coverage) ---------------------------

test("cortex_recall.execute returns a benign result when the binary errors", async () => {
  const { tools } = loadExtensionAndCapture();
  const prior = process.env.CORTEX_BINARY;
  try {
    process.env.CORTEX_BINARY = "/nonexistent/cortex-binary-for-test";
    const result = await tools[0].execute(
      "tool-call-id-1",
      { query: "anything" },
      undefined,
      undefined,
      {},
    );
    assert.match(result.content[0].text, /No relevant context captured yet/);
    assert.ok(typeof result.details.error === "string");
  } finally {
    if (prior === undefined) {
      delete process.env.CORTEX_BINARY;
    } else {
      process.env.CORTEX_BINARY = prior;
    }
  }
});

test("cortex_recall.execute shells out flags-first and renders JSON as markdown", async () => {
  const { tools } = loadExtensionAndCapture();
  const dir = mkdtempSync(join(tmpdir(), "cortex-recall-test-"));
  const fakeCortex = join(dir, "cortex-fake");
  writeFileSync(
    fakeCortex,
    `#!/bin/sh
if [ "$1" != "search" ] || [ "$2" != "--format" ] || [ "$3" != "json" ] || [ "$4" != "--limit" ]; then
  echo "BAD ARG ORDER: $@" >&2
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
    const result = await tools[0].execute(
      "tool-call-id-fake",
      { query: "pgx", limit: 3 },
      undefined,
      undefined,
      {},
    );
    assert.match(result.content[0].text, /Found 1 relevant context item:/);
    assert.strictEqual(result.details.count, 1);
  } finally {
    if (prior === undefined) {
      delete process.env.CORTEX_BINARY;
    } else {
      process.env.CORTEX_BINARY = prior;
    }
    rmSync(dir, { recursive: true, force: true });
  }
});

test("factory has a default export", () => {
  assert.strictEqual(typeof cortexExtension, "function");
});
