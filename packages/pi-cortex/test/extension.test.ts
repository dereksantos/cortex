import test from "node:test";
import assert from "node:assert/strict";
import cortexExtension from "../extensions/cortex/index.ts";

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
  ) => Promise<{ content: Array<{ type: string; text: string }>; details: unknown }>;
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
  // The ExtensionAPI surface is wider than this test mock; cast at the boundary.
  cortexExtension(fakePi as unknown as Parameters<typeof cortexExtension>[0]);
  return { tools, events };
}

test("cortex_recall registers exactly once", () => {
  const { tools } = loadExtensionAndCapture();
  assert.strictEqual(tools.length, 1, "factory must register exactly one tool");
  assert.strictEqual(tools[0].name, "cortex_recall");
  assert.strictEqual(tools[0].label, "Cortex Recall");
  assert.ok(tools[0].description.length > 0, "tool must have a non-empty description");
});

test("cortex_recall schema declares required query and optional bounded limit", () => {
  const { tools } = loadExtensionAndCapture();
  const tool = tools[0];

  assert.strictEqual(tool.parameters.type, "object", "parameters must be an object schema");
  assert.ok(tool.parameters.properties.query, "schema must declare a query property");
  assert.strictEqual(tool.parameters.properties.query.type, "string", "query must be a string");

  assert.ok(tool.parameters.properties.limit, "schema must declare a limit property");
  assert.strictEqual(tool.parameters.properties.limit.type, "number", "limit must be a number");
  assert.strictEqual(tool.parameters.properties.limit.minimum, 1, "limit must be >= 1");
  assert.strictEqual(tool.parameters.properties.limit.maximum, 50, "limit must be <= 50");

  // typebox emits `required: [...]` only for non-Optional fields.
  assert.deepStrictEqual(tool.parameters.required, ["query"], "only query is required");
});

test("cortex_recall stubbed execute returns a non-empty text content item", async () => {
  const { tools } = loadExtensionAndCapture();
  const result = await tools[0].execute(
    "tool-call-id-1",
    { query: "authentication", limit: 3 },
    undefined,
    undefined,
    {},
  );
  assert.ok(Array.isArray(result.content), "result.content must be an array");
  assert.strictEqual(result.content.length, 1, "stub returns exactly one content item");
  assert.strictEqual(result.content[0].type, "text", "content item must be text");
  assert.ok(result.content[0].text.length > 0, "content text must be non-empty");
  assert.deepStrictEqual(result.details, {}, "stub details is empty object");
});

test("factory does not hook events yet (TODO 7 wires tool_call)", () => {
  const { events } = loadExtensionAndCapture();
  assert.deepStrictEqual(events, [], "no event hooks expected before TODO 7");
});

test("factory has a default export", () => {
  assert.strictEqual(typeof cortexExtension, "function", "default export must be the factory function");
});
