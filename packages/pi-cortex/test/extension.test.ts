import test from "node:test";
import assert from "node:assert/strict";
import cortexExtension from "../extensions/cortex/index.ts";

test("cortex factory loads without error and registers nothing (TODO 2 stub)", () => {
  let registerToolCalls = 0;
  let onCalls = 0;
  let registerCommandCalls = 0;

  const fakePi = {
    registerTool: () => {
      registerToolCalls++;
    },
    on: () => {
      onCalls++;
    },
    registerCommand: () => {
      registerCommandCalls++;
    },
  };

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  assert.doesNotThrow(() => cortexExtension(fakePi as any));

  assert.strictEqual(registerToolCalls, 0, "TODO 2 stub must not register tools yet");
  assert.strictEqual(onCalls, 0, "TODO 2 stub must not hook events yet");
  assert.strictEqual(registerCommandCalls, 0, "TODO 2 stub must not register commands yet");
});

test("cortex factory has a default export", () => {
  assert.strictEqual(typeof cortexExtension, "function", "default export must be the factory function");
});
