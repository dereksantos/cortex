// Pure helpers shared across the opencode-cortex plugin (cortex_recall
// tool + tool.execute.after capture hook). No I/O, no plugin-API
// imports — keeps these unit-testable in isolation and reusable in the
// capture-hook commit (TODO 5).
//
// Copied verbatim from packages/pi-cortex/extensions/cortex/index.ts
// where the logic is API-agnostic. The opencode plugin diverges only
// in the entry point (Zod schema, async Plugin export, tool.output
// shape, tool.execute.after hook signature) — handled in cortex.ts.

export const NO_RESULTS_TEXT = "No relevant context captured yet.";
export const DEFAULT_BINARY = "cortex";

/**
 * Tools the cortex capture hook forwards to `cortex capture`. Read,
 * glob, find, ls, grep are deliberately excluded — they fire constantly
 * during normal coding work and would bury the signal in noise. The
 * allowlist is intentionally narrow.
 */
export const CAPTURE_ALLOWLIST: ReadonlySet<string> = new Set([
  "edit",
  "write",
  "bash",
  "cortex_recall",
]);

/**
 * Maximum chars to keep in `result_summary`. Long bash output, file
 * dumps, etc. get truncated so cortex events stay scannable.
 */
export const RESULT_SUMMARY_MAX = 500;

/**
 * Key-name patterns to drop from `args_redacted`. Matches `api_key`,
 * `auth_token`, `openrouter_token`, `client_secret`, etc.
 */
export const SECRET_KEY_PATTERN = /^(api[_-]?key|.+[_-]token|.+[_-]secret)$/i;

/**
 * Value patterns to redact in-place even when the key looks innocuous.
 * Covers OpenRouter (`sk-or-…`), Anthropic (`sk-ant-…`), and the
 * generic OpenAI-shape `sk-` prefix at length ≥ 32. Conservative on
 * purpose — false positives are preferable to leaked secrets.
 */
export const SECRET_VALUE_PATTERN =
  /sk-or-v1-[a-zA-Z0-9_-]+|sk-ant-[a-zA-Z0-9_-]+|sk-[a-zA-Z0-9]{32,}/g;

export const REDACTED = "<REDACTED>";

/**
 * Normalized recall row shape emitted by `cortex search --format json`.
 * Matches the Go-side `recallEntry` struct in
 * cmd/cortex/commands/query.go.
 */
export type RecallEntry = {
  id: string;
  content: string;
  score: number;
  captured_at: string;
  tags?: string[];
  category?: string;
  source?: string;
};

/**
 * Resolve the cortex binary path. Prefers $CORTEX_BINARY (set by
 * the eval grid runner) over PATH lookup; falls back to the literal
 * "cortex" name which `execFile` will resolve via PATH.
 */
export function resolveCortexBinary(): string {
  const fromEnv = process.env.CORTEX_BINARY?.trim();
  if (fromEnv && fromEnv.length > 0) {
    return fromEnv;
  }
  return DEFAULT_BINARY;
}

/**
 * Format a recall-entries list as a short markdown list. Empty
 * input returns the dedicated no-results sentence (never throws,
 * never returns an empty string — the LLM needs *something* to
 * read).
 */
export function formatRecallResults(entries: RecallEntry[]): string {
  if (!Array.isArray(entries) || entries.length === 0) {
    return NO_RESULTS_TEXT;
  }
  const lines = entries.map((e, i) => {
    const scorePct =
      typeof e.score === "number" && e.score > 0
        ? ` _(${(e.score * 100).toFixed(0)}%)_`
        : "";
    const category = e.category ? ` **[${e.category}]**` : "";
    return `${i + 1}.${category}${scorePct} ${e.content}`;
  });
  const header = `Found ${entries.length} relevant context item${entries.length === 1 ? "" : "s"}:`;
  return `${header}\n\n${lines.join("\n")}`;
}

/**
 * Recursively scrub secret-shaped fields from arbitrary tool args.
 * Two passes:
 *   1. Keys matching SECRET_KEY_PATTERN get their value replaced
 *      with the REDACTED sentinel regardless of the underlying type.
 *   2. String values are scanned for SECRET_VALUE_PATTERN matches
 *      (OpenRouter / Anthropic / OpenAI shapes) and the matches
 *      get replaced inline.
 * Nested objects and arrays are traversed in place; primitives
 * other than strings pass through unchanged.
 */
export function redactSecrets(value: unknown): unknown {
  if (typeof value === "string") {
    return value.replace(SECRET_VALUE_PATTERN, REDACTED);
  }
  if (Array.isArray(value)) {
    return value.map(redactSecrets);
  }
  if (value !== null && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      if (SECRET_KEY_PATTERN.test(k)) {
        out[k] = REDACTED;
      } else {
        out[k] = redactSecrets(v);
      }
    }
    return out;
  }
  return value;
}

/**
 * Pull the first text content item out of a tool result and clip
 * to RESULT_SUMMARY_MAX. Missing / non-text content returns an
 * empty string so the schema invariant ("string field exists")
 * holds.
 */
export function summarizeResultContent(
  content: unknown,
  maxChars: number = RESULT_SUMMARY_MAX,
): string {
  if (!Array.isArray(content) || content.length === 0) {
    return "";
  }
  const first = content[0] as { type?: string; text?: string };
  const text = typeof first.text === "string" ? first.text : "";
  if (text.length <= maxChars) {
    return text;
  }
  return text.slice(0, maxChars) + "…";
}
