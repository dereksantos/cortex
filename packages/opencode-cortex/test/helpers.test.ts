import test from "node:test";
import assert from "node:assert/strict";
import {
  CAPTURE_ALLOWLIST,
  formatRecallResults,
  NO_RESULTS_TEXT,
  REDACTED,
  redactSecrets,
  resolveCortexBinary,
  RESULT_SUMMARY_MAX,
  SECRET_KEY_PATTERN,
  SECRET_VALUE_PATTERN,
  summarizeResultContent,
  type RecallEntry,
} from "../plugins/_helpers.ts";

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
  assert.strictEqual(formatRecallResults([]), NO_RESULTS_TEXT);
  assert.strictEqual(
    formatRecallResults(null as unknown as RecallEntry[]),
    NO_RESULTS_TEXT,
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

test("formatRecallResults: singular header for one entry; category badge present", () => {
  const text = formatRecallResults([
    {
      id: "x",
      content: "hello",
      score: 0.5,
      captured_at: "2026-05-10T00:00:00Z",
      category: "decision",
    },
  ]);
  assert.match(text, /Found 1 relevant context item:/);
  assert.match(text, /\*\*\[decision\]\*\*/);
  assert.match(text, /_\(50%\)_/);
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
  assert.strictEqual(out.api_key, REDACTED);
  assert.strictEqual(out.auth_token, REDACTED);
  assert.strictEqual(out.client_secret, REDACTED);
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
  // "X-Api-Key" doesn't match the regex (anchored, lowercase base name).
  // The value pattern catches the actual sk-or-/sk-ant- shapes.
  assert.strictEqual(out.headers["X-Api-Key"], "secret123");
  assert.strictEqual(out.headers["Content-Type"], "application/json");
  assert.strictEqual((out.args[0] as Record<string, unknown>).api_key, REDACTED);
  assert.match(out.args[1] as string, /<REDACTED>/);
});

test("redactSecrets passes through primitives unchanged", () => {
  assert.strictEqual(redactSecrets(42), 42);
  assert.strictEqual(redactSecrets(null), null);
  assert.strictEqual(redactSecrets(true), true);
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

test("summarizeResultContent: truncates long content and appends ellipsis at default cap", () => {
  const long = "x".repeat(RESULT_SUMMARY_MAX + 100);
  const out = summarizeResultContent([{ type: "text", text: long }]);
  assert.strictEqual(out.length, RESULT_SUMMARY_MAX + 1); // 500 chars + 1 ellipsis char
  assert.ok(out.endsWith("…"));
});

test("summarizeResultContent: missing text field returns empty string", () => {
  const out = summarizeResultContent([{ type: "text" }]);
  assert.strictEqual(out, "");
});

// ---- constants -------------------------------------------------------------

test("CAPTURE_ALLOWLIST contains the four expected entries (matches pi-cortex)", () => {
  assert.ok(CAPTURE_ALLOWLIST.has("edit"));
  assert.ok(CAPTURE_ALLOWLIST.has("write"));
  assert.ok(CAPTURE_ALLOWLIST.has("bash"));
  assert.ok(CAPTURE_ALLOWLIST.has("cortex_recall"));
  assert.strictEqual(CAPTURE_ALLOWLIST.size, 4);
  // Read/glob/find/ls/grep are deliberately excluded.
  assert.ok(!CAPTURE_ALLOWLIST.has("read"));
  assert.ok(!CAPTURE_ALLOWLIST.has("grep"));
});

test("SECRET_KEY_PATTERN matches expected key shapes; misses unanchored or cased variants", () => {
  assert.ok(SECRET_KEY_PATTERN.test("api_key"));
  assert.ok(SECRET_KEY_PATTERN.test("API_KEY"));
  assert.ok(SECRET_KEY_PATTERN.test("apikey"));
  assert.ok(SECRET_KEY_PATTERN.test("auth_token"));
  assert.ok(SECRET_KEY_PATTERN.test("openrouter_token"));
  assert.ok(SECRET_KEY_PATTERN.test("client_secret"));
  // Anchored: substring matches don't trigger.
  assert.ok(!SECRET_KEY_PATTERN.test("X-Api-Key"));
  assert.ok(!SECRET_KEY_PATTERN.test("user_id"));
});

test("SECRET_VALUE_PATTERN catches sk-or / sk-ant / sk-* shapes", () => {
  assert.match("sk-or-v1-abcdef123", SECRET_VALUE_PATTERN);
  // Reset lastIndex for global regex.
  SECRET_VALUE_PATTERN.lastIndex = 0;
  assert.match("sk-ant-api03-XYZdef", SECRET_VALUE_PATTERN);
  SECRET_VALUE_PATTERN.lastIndex = 0;
  assert.match(`sk-${"a".repeat(40)}`, SECRET_VALUE_PATTERN);
  SECRET_VALUE_PATTERN.lastIndex = 0;
  // Short sk- prefix doesn't trigger (length floor is 32).
  assert.ok(!SECRET_VALUE_PATTERN.test("sk-tooshort"));
});
