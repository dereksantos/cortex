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
//
// PATH-shadowing is a real supply-chain attack: a malicious dep that
// drops `./cortex` into a writable PATH entry hijacks every recall and
// capture from that point on. Defense: require the resolved binary to
// be absolute. $CORTEX_BINARY may be set explicitly by callers (e.g.
// the eval-grid runner), and a relative value is rejected outright.
// When the env var is unset we fall back to a manual PATH walk that
// returns the resolved absolute path, never the bare `"cortex"` name.

test("resolveCortexBinary returns $CORTEX_BINARY when set to an absolute path", () => {
  const prior = process.env.CORTEX_BINARY;
  try {
    process.env.CORTEX_BINARY = "/tmp/some/cortex";
    assert.strictEqual(resolveCortexBinary(), "/tmp/some/cortex");
    process.env.CORTEX_BINARY = "  /padded/path  ";
    assert.strictEqual(resolveCortexBinary(), "/padded/path");
  } finally {
    if (prior === undefined) {
      delete process.env.CORTEX_BINARY;
    } else {
      process.env.CORTEX_BINARY = prior;
    }
  }
});

test("resolveCortexBinary throws if $CORTEX_BINARY is a relative path", () => {
  const prior = process.env.CORTEX_BINARY;
  try {
    process.env.CORTEX_BINARY = "cortex";
    assert.throws(() => resolveCortexBinary(), /absolute/i);
    process.env.CORTEX_BINARY = "./bin/cortex";
    assert.throws(() => resolveCortexBinary(), /absolute/i);
    process.env.CORTEX_BINARY = "../cortex";
    assert.throws(() => resolveCortexBinary(), /absolute/i);
  } finally {
    if (prior === undefined) {
      delete process.env.CORTEX_BINARY;
    } else {
      process.env.CORTEX_BINARY = prior;
    }
  }
});

test("resolveCortexBinary throws when env var unset and PATH lookup fails", () => {
  const priorBinary = process.env.CORTEX_BINARY;
  const priorPath = process.env.PATH;
  try {
    delete process.env.CORTEX_BINARY;
    process.env.PATH = "/nonexistent-dir-for-cortex-test";
    assert.throws(() => resolveCortexBinary(), /not found/i);
  } finally {
    if (priorBinary === undefined) {
      delete process.env.CORTEX_BINARY;
    } else {
      process.env.CORTEX_BINARY = priorBinary;
    }
    process.env.PATH = priorPath;
  }
});

test("resolveCortexBinary resolves PATH to an absolute path when env var unset", async () => {
  // Build a fake cortex executable in a temp dir, point PATH at it,
  // and assert the absolute path comes back — NOT bare "cortex".
  const fs = await import("node:fs");
  const path = await import("node:path");
  const os = await import("node:os");
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "cortex-which-"));
  const fakeBin = path.join(tmp, "cortex");
  fs.writeFileSync(fakeBin, "#!/bin/sh\necho fake\n", { mode: 0o755 });

  const priorBinary = process.env.CORTEX_BINARY;
  const priorPath = process.env.PATH;
  try {
    delete process.env.CORTEX_BINARY;
    process.env.PATH = tmp;
    const resolved = resolveCortexBinary();
    assert.strictEqual(resolved, fakeBin);
    assert.ok(path.isAbsolute(resolved), `expected absolute, got: ${resolved}`);
  } finally {
    if (priorBinary === undefined) {
      delete process.env.CORTEX_BINARY;
    } else {
      process.env.CORTEX_BINARY = priorBinary;
    }
    process.env.PATH = priorPath;
    fs.rmSync(tmp, { recursive: true, force: true });
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
  assert.match(text, /2 retrieved context items/);
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
  assert.match(text, /1 retrieved context item/);
  assert.match(text, /\*\*\[decision\]\*\*/);
  assert.match(text, /_\(50%\)_/);
});

// ---- formatRecallResults trust framing -------------------------------------
// Retrieved context flows into the agent's prompt verbatim. That makes the
// retrieval pipeline an indirect-prompt-injection vector: a poisoned source
// (a malicious README, a tainted commit message, an attacker-controlled
// session log captured into the store) could carry "ignore prior
// instructions" payloads that the LLM would otherwise treat as commands.
// The mitigation is a clear in-band signal that the wrapped content is
// data, not instructions.

test("formatRecallResults frames non-empty output with an untrusted-context directive", () => {
  const text = formatRecallResults([
    { id: "a", content: "alpha", score: 0.9, captured_at: "2026-05-10T00:00:00Z" },
  ]);
  // The directive must mention that the content is untrusted / data,
  // and must instruct the model not to follow embedded instructions.
  assert.match(text, /untrusted/i);
  assert.match(text, /do not (?:follow|execute|treat .* as instructions)/i);
});

test("formatRecallResults wraps items in a tagged block the LLM can recognize", () => {
  const text = formatRecallResults([
    { id: "a", content: "alpha", score: 0.9, captured_at: "2026-05-10T00:00:00Z" },
    { id: "b", content: "beta", score: 0.8, captured_at: "2026-05-10T00:00:00Z" },
  ]);
  // Both an opening and closing tag must surround the numbered list.
  assert.match(text, /<retrieved_context\b[^>]*>/);
  assert.match(text, /<\/retrieved_context>/);
  const open = text.indexOf("<retrieved_context");
  const close = text.indexOf("</retrieved_context>");
  const itemAt = text.indexOf("1. ");
  assert.ok(open < itemAt && itemAt < close, "items must live inside the tagged block");
});

test("formatRecallResults: no framing on empty results (no-results sentence stays clean)", () => {
  // When there are no results, the function returns the dedicated
  // no-results sentence verbatim. Framing it would just add noise — the
  // LLM has nothing to be misled by.
  const text = formatRecallResults([]);
  assert.strictEqual(text, NO_RESULTS_TEXT);
  assert.ok(!/<retrieved_context/.test(text));
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

// ---- expanded SECRET_VALUE_PATTERN coverage --------------------------------
// LLM tool output regularly contains pasted secrets from .env files, error
// messages, or curl examples. The original regex only caught Anthropic /
// OpenRouter / OpenAI shapes; the expansion below catches the most common
// other formats so they don't end up in the capture queue verbatim.

test("SECRET_VALUE_PATTERN catches AWS access key IDs (AKIA-prefix)", () => {
  SECRET_VALUE_PATTERN.lastIndex = 0;
  assert.match("AKIAIOSFODNN7EXAMPLE", SECRET_VALUE_PATTERN);
  SECRET_VALUE_PATTERN.lastIndex = 0;
  // Inline in a string (e.g. a curl example) should still be caught.
  const out = "AKIAIOSFODNN7EXAMPLE".replace(SECRET_VALUE_PATTERN, REDACTED);
  assert.strictEqual(out, REDACTED);
});

test("SECRET_VALUE_PATTERN catches GitHub PAT shapes (ghp/ghs/gho/ghr/ghu)", () => {
  for (const prefix of ["ghp_", "ghs_", "gho_", "ghr_", "ghu_"]) {
    SECRET_VALUE_PATTERN.lastIndex = 0;
    const tok = `${prefix}${"A".repeat(36)}`;
    assert.match(tok, SECRET_VALUE_PATTERN);
  }
});

test("SECRET_VALUE_PATTERN catches Slack tokens (xoxb / xoxp / xoxa / xoxs)", () => {
  for (const prefix of ["xoxb-", "xoxp-", "xoxa-", "xoxs-"]) {
    SECRET_VALUE_PATTERN.lastIndex = 0;
    const tok = `${prefix}1234567890-1234567890-deadbeefdeadbeefdeadbeef`;
    assert.match(tok, SECRET_VALUE_PATTERN);
  }
});

test("SECRET_VALUE_PATTERN catches GCP service-account private-key marker", () => {
  // The signature material itself rather than the (large) PEM body —
  // hitting just the marker is enough to redact the surrounding text
  // when it appears in a JSON service-account dump.
  SECRET_VALUE_PATTERN.lastIndex = 0;
  assert.match("-----BEGIN PRIVATE KEY-----", SECRET_VALUE_PATTERN);
  SECRET_VALUE_PATTERN.lastIndex = 0;
  assert.match("-----BEGIN RSA PRIVATE KEY-----", SECRET_VALUE_PATTERN);
  SECRET_VALUE_PATTERN.lastIndex = 0;
  assert.match("-----BEGIN OPENSSH PRIVATE KEY-----", SECRET_VALUE_PATTERN);
});

test("SECRET_VALUE_PATTERN does NOT over-match common ambient strings", () => {
  // False-positive guard: short tokens, normal sentences, file paths must
  // pass through unchanged. The point of the expansion is sensitivity to
  // realistic shapes, not paranoia.
  for (const s of [
    "hello world",
    "/usr/local/bin/cortex",
    "ghp_short", // too short — must NOT match
    "BEGIN PRIVATE KEY", // missing the dash anchor
    "AKIAshort", // too short — must NOT match
    "import { Foo } from 'bar'",
  ]) {
    SECRET_VALUE_PATTERN.lastIndex = 0;
    assert.ok(!SECRET_VALUE_PATTERN.test(s), `unexpected match for: ${s}`);
  }
});

test("redactSecrets scrubs AWS / GitHub / Slack / PEM-header values inline", () => {
  const input = {
    deploy_cmd: "export AWS=AKIAIOSFODNN7EXAMPLE && deploy",
    note: `token: ghp_${"A".repeat(36)}`,
    slack: `xoxb-1234567890-1234567890-${"d".repeat(24)}`,
    keypem: "header\n-----BEGIN PRIVATE KEY-----\nMIIE…",
  };
  const out = redactSecrets(input) as Record<string, string>;
  assert.ok(!/AKIAIOSFODNN7EXAMPLE/.test(out.deploy_cmd));
  assert.ok(!/ghp_AAA/.test(out.note));
  assert.ok(!/xoxb-1234567890-1234567890-d{24}/.test(out.slack));
  assert.ok(!/BEGIN PRIVATE KEY/.test(out.keypem));
});
