import test from "node:test";
import assert from "node:assert/strict";
import type { Plugin } from "@opencode-ai/plugin";
import { tool } from "@opencode-ai/plugin/tool";

test("scaffold: @opencode-ai/plugin types resolve and tool() helper exists at runtime", () => {
  const _check: Plugin = async () => ({});
  assert.equal(typeof _check, "function");
  assert.equal(typeof tool, "function");
  assert.equal(typeof tool.schema, "object");
});
