import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync, chmodSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import cortexExtension, {
  formatRecallResults,
  resolveCortexBinary,
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

function loadExtensionAndCapture(): { tools: RegisteredTool[]; events: string[] } {
  const tools: RegisteredTool[] = [];
  const events: string[] = [];
  const fakePi = {
    registerTool: (tool: RegisteredTool) => {
      tools.push(tool);
    },
    on: (event: string) => {
      events.push(event);
    },
    registerCommand: () => {},
  };
  cortexExtension(fakePi as unknown as Parameters<typeof cortexExtension>[0]);
  return { tools, events };
}

test("cortex_recall registers exactly once with the real pi v0.74 API shape", () => {
  const { tools } = loadExtensionAndCapture();
  assert.strictEqual(tools.length, 1);
  assert.strictEqual(tools[0].name, "cortex_recall");
  assert.strictEqual(tools[0].label, "Cortex Recall");
  assert.ok(tools[0].description.length > 100, "description must guide tool-call decisions");
});

test("schema declares required query and optional bounded limit", () => {
  const { tools } = loadExtensionAndCapture();
  const tool = tools[0];
  assert.strictEqual(tool.parameters.type, "object");
  assert.strictEqual(tool.parameters.properties.query.type, "string");
  assert.strictEqual(tool.parameters.properties.limit.type, "number");
  assert.strictEqual(tool.parameters.properties.limit.minimum, 1);
  assert.strictEqual(tool.parameters.properties.limit.maximum, 50);
  assert.deepStrictEqual(tool.parameters.required, ["query"]);
});

test("factory does not hook events yet (TODO 7 wires tool_call)", () => {
  const { events } = loadExtensionAndCapture();
  assert.deepStrictEqual(events, []);
});

test("resolveCortexBinary prefers $CORTEX_BINARY over default", () => {
  const prior = process.env.CORTEX_BINARY;
  try {
    process.env.CORTEX_BINARY = "/tmp/some/cortex";
    assert.strictEqual(resolveCortexBinary(), "/tmp/some/cortex");

    process.env.CORTEX_BINARY = "  /padded/path  ";
    assert.strictEqual(resolveCortexBinary(), "/padded/path", "must trim whitespace");

    delete process.env.CORTEX_BINARY;
    assert.strictEqual(resolveCortexBinary(), "cortex", "falls back to the bare name for PATH lookup");

    process.env.CORTEX_BINARY = "";
    assert.strictEqual(resolveCortexBinary(), "cortex", "empty env var falls back to default");
  } finally {
    if (prior === undefined) {
      delete process.env.CORTEX_BINARY;
    } else {
      process.env.CORTEX_BINARY = prior;
    }
  }
});

test("formatRecallResults: empty / non-array input returns the no-results sentence", () => {
  assert.strictEqual(formatRecallResults([]), "No relevant context captured yet.");
  // Defensive: callers should always pass an array, but if they
  // pass null/undefined the formatter must not throw.
  assert.strictEqual(
    formatRecallResults(null as unknown as RecallEntry[]),
    "No relevant context captured yet.",
  );
});

test("formatRecallResults: one entry renders as a single-item list", () => {
  const text = formatRecallResults([
    {
      id: "1",
      content: "Use pgx not database/sql.",
      score: 0.87,
      captured_at: "2026-05-10T18:04:00Z",
      category: "decision",
    },
  ]);
  assert.match(text, /Found 1 relevant context item:/);
  assert.match(text, /1\. \*\*\[decision\]\*\* _\(87%\)_ Use pgx not database\/sql\./);
});

test("formatRecallResults: pluralizes header and lists multiple entries", () => {
  const text = formatRecallResults([
    { id: "1", content: "A", score: 0.9, captured_at: "2026-05-10T18:04:00Z" },
    { id: "2", content: "B", score: 0, captured_at: "2026-05-10T18:04:00Z" },
  ]);
  assert.match(text, /Found 2 relevant context items:/);
  assert.match(text, /^1\. .*A$/m, "entry 1 must end with A on its own line");
  assert.match(text, /^2\. B$/m, "entry 2 must be the bare numbered line with B");
  // Entries must appear in order.
  const ixA = text.indexOf(" A");
  const ixB = text.indexOf(" B");
  assert.ok(ixA > -1 && ixB > -1 && ixA < ixB, "A must precede B in the rendered list");
  // Score 0 should not render a percentage suffix.
  assert.ok(!/_\(0%\)_/.test(text), "score=0 should not render a percentage");
});

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
    assert.strictEqual(result.content.length, 1);
    assert.strictEqual(result.content[0].type, "text");
    assert.match(
      result.content[0].text,
      /No relevant context captured yet/,
      "must degrade to the no-results sentence so the agent loop continues",
    );
    assert.ok(
      typeof result.details.error === "string" && (result.details.error as string).length > 0,
      "details.error must capture the underlying message for debugging",
    );
  } finally {
    if (prior === undefined) {
      delete process.env.CORTEX_BINARY;
    } else {
      process.env.CORTEX_BINARY = prior;
    }
  }
});

test("cortex_recall.execute shells out and renders the JSON output as markdown", async () => {
  const { tools } = loadExtensionAndCapture();
  const dir = mkdtempSync(join(tmpdir(), "cortex-recall-test-"));
  const fakeCortex = join(dir, "cortex-fake");
  // Shell script that VERIFIES its args are flags-first (the
  // ordering required for Go's stdlib flag.Parse to consume
  // --format and --limit before hitting the positional query)
  // and then emits the JSON shape cortex_recall expects.
  // If runCortexSearch ever regresses to flag-after-query
  // ordering, this script exits 2 and the test fails loudly.
  writeFileSync(
    fakeCortex,
    `#!/bin/sh
# Expected: cortex search --format json --limit <N> <query>
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
    assert.strictEqual(result.content[0].type, "text");
    assert.match(result.content[0].text, /Found 1 relevant context item:/);
    assert.match(result.content[0].text, /Use pgx not database\/sql\./);
    assert.strictEqual(result.details.count, 1);
    assert.ok(Array.isArray(result.details.entries));
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
