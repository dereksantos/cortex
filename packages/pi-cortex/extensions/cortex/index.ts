import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";
import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

const NO_RESULTS_TEXT = "No relevant context captured yet.";
const DEFAULT_BINARY = "cortex";
const SEARCH_TIMEOUT_MS = 5_000;
const SEARCH_MAX_BUFFER = 2 * 1024 * 1024; // 2 MiB — plenty for ≤50 results

/**
 * Tools the cortex capture hook forwards to `cortex capture`. Read,
 * glob, find, ls, grep are deliberately excluded — they fire constantly
 * during normal coding work and would bury the signal in noise. The
 * allowlist is intentionally narrow.
 */
const CAPTURE_ALLOWLIST: ReadonlySet<string> = new Set([
  "edit",
  "write",
  "bash",
  "cortex_recall",
]);

/**
 * Maximum chars to keep in `result_summary`. Long bash output, file
 * dumps, etc. get truncated so cortex events stay scannable.
 */
const RESULT_SUMMARY_MAX = 500;

/**
 * Key-name patterns to drop from `args_redacted`. Matches `api_key`,
 * `auth_token`, `openrouter_token`, `client_secret`, etc.
 */
const SECRET_KEY_PATTERN = /^(api[_-]?key|.+[_-]token|.+[_-]secret)$/i;

/**
 * Value patterns to redact in-place even when the key looks innocuous.
 * Covers OpenRouter (`sk-or-…`), Anthropic (`sk-ant-…`), and the
 * generic OpenAI-shape `sk-` prefix at length ≥ 32. Conservative on
 * purpose — false positives are preferable to leaked secrets.
 */
const SECRET_VALUE_PATTERN =
  /sk-or-v1-[a-zA-Z0-9_-]+|sk-ant-[a-zA-Z0-9_-]+|sk-[a-zA-Z0-9]{32,}/g;

const REDACTED = "<REDACTED>";

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
 * Required shape of a captured `pi_tool_call` row, as defined in
 * docs/prompts/pi-extension-prompt.md TODO 7. Downstream Dream
 * sources will read these as structured data — the shape is part
 * of the cortex contract.
 */
export type PiToolCallCapture = {
  tool_name: string;
  args_redacted: Record<string, unknown>;
  result_summary: string;
  captured_at: string;
  session_id?: string;
};

const CortexRecallParams = Type.Object({
  query: Type.String({
    description:
      "Natural-language query to search captured cortex context. Examples: 'authentication flow', 'why did we pick pgx over database/sql', 'previous decisions about retry strategy'.",
  }),
  limit: Type.Optional(
    Type.Number({
      description: "Maximum number of results to return.",
      default: 5,
      minimum: 1,
      maximum: 50,
    }),
  ),
});

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
 * Shell out to `cortex search ... --format json`. Throws on any
 * non-zero exit, timeout, JSON parse failure, or non-array
 * payload. Callers must catch and translate failures into a
 * benign tool result — cortex_recall never errors out the agent
 * loop.
 */
export async function runCortexSearch(
  binary: string,
  query: string,
  limit: number,
): Promise<RecallEntry[]> {
  // IMPORTANT: flags must come BEFORE the positional query.
  // Go's stdlib `flag.Parse` stops at the first non-flag argument,
  // so `cortex search <query> --format json` silently falls back to
  // the default text format. Passing flags first is the reliable
  // ordering; if cortex grows a smarter arg splitter later, this
  // ordering remains correct.
  const { stdout } = await execFileAsync(
    binary,
    ["search", "--format", "json", "--limit", String(limit), query],
    { timeout: SEARCH_TIMEOUT_MS, maxBuffer: SEARCH_MAX_BUFFER },
  );
  const parsed: unknown = JSON.parse(stdout);
  if (!Array.isArray(parsed)) {
    throw new Error(`cortex search returned non-array JSON: ${typeof parsed}`);
  }
  return parsed as RecallEntry[];
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

/**
 * Build the schema-conformant capture payload for a single
 * allowlisted tool result. Returns null when the tool is not on
 * the capture allowlist — callers can short-circuit instead of
 * spawning cortex capture.
 */
export function buildCapturePayload(
  toolName: string,
  input: Record<string, unknown>,
  content: unknown,
  sessionId?: string,
): PiToolCallCapture | null {
  if (!CAPTURE_ALLOWLIST.has(toolName)) {
    return null;
  }
  const args = redactSecrets(input);
  const payload: PiToolCallCapture = {
    tool_name: toolName,
    args_redacted: (args && typeof args === "object" ? args : {}) as Record<
      string,
      unknown
    >,
    result_summary: summarizeResultContent(content),
    captured_at: new Date().toISOString(),
  };
  if (sessionId && sessionId.length > 0) {
    payload.session_id = sessionId;
  }
  return payload;
}

/**
 * Fire-and-forget shell-out to `cortex capture --type pi_tool_call
 * --content <json>`. Never blocks the agent loop:
 *   - spawn instead of execFile (no pipe buffering wait)
 *   - stdio is ignored end-to-end
 *   - child.unref() so a long-running capture doesn't keep the
 *     pi process alive past its turn
 *   - errors are silently swallowed (capture is best-effort)
 *
 * cwd: `cortex capture` walks up from its cwd looking for
 * `.cortex/config.json`. Pi's cwd is the cell's temp workdir
 * which has no `.cortex/`, so capture would silently fail.
 * We set cwd to `$CORTEX_PROJECT_ROOT` when it's defined (set
 * by the eval grid runner to the absolute path of the cortex
 * repo root); otherwise we fall back to leaving cwd inherited,
 * which only works when pi is invoked inside a cortex project
 * directly.
 */
export function shellCapture(
  binary: string,
  payload: PiToolCallCapture,
): void {
  const json = JSON.stringify(payload);
  const projectRoot = process.env.CORTEX_PROJECT_ROOT?.trim();
  try {
    const child = spawn(
      binary,
      ["capture", "--type", "pi_tool_call", "--content", json],
      {
        stdio: ["ignore", "ignore", "ignore"],
        detached: false,
        cwd: projectRoot && projectRoot.length > 0 ? projectRoot : undefined,
      },
    );
    child.on("error", () => {
      // swallowed — capture is best-effort
    });
    child.unref();
  } catch {
    // swallowed — binary missing / permissions / etc.
  }
}

/**
 * Cortex extension factory for pi.dev.
 *
 * Phase 8 status:
 *   TODO 5: cortex_recall shells out to `cortex search --format
 *           json`.
 *   TODO 7 (this commit): tool_result hook forwards redacted
 *           events for allowlisted tools (edit, write, bash,
 *           cortex_recall) to `cortex capture --type pi_tool_call`.
 *           Read / glob / find / ls / grep are intentionally
 *           excluded so the event log isn't buried in noise.
 *
 * Note: the prompt's TODO 7 text says "Wire pi.on('tool_call', …)";
 * the actual hook is on the `tool_result` event because that's the
 * event carrying both the input args and the result content. The
 * `tool_call` event fires *before* the tool runs and has only
 * input. Hooking tool_result gives us a single point of capture
 * with all required schema fields.
 */
export default function cortexExtension(pi: ExtensionAPI): void {
  pi.registerTool({
    name: "cortex_recall",
    label: "Cortex Recall",
    description:
      "Recall relevant captured context (decisions, patterns, prior corrections, constraints) for the current task. Use when starting work in an unfamiliar area of the codebase, when the user references prior discussions or decisions, or when a task involves domain knowledge that may have been captured earlier. Returns a short markdown list of relevant context items. Decide what to do with the results — do not act on them unconditionally.",
    parameters: CortexRecallParams,
    execute: async (_toolCallId, params, _signal, _onUpdate, _ctx) => {
      const query = params.query;
      const limit = params.limit ?? 5;
      const binary = resolveCortexBinary();

      try {
        const entries = await runCortexSearch(binary, query, limit);
        return {
          content: [
            {
              type: "text" as const,
              text: formatRecallResults(entries),
            },
          ],
          details: { entries, count: entries.length, binary } as Record<string, unknown>,
        };
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        return {
          content: [
            {
              type: "text" as const,
              text: `${NO_RESULTS_TEXT} (cortex_recall could not query the store: ${message}.) Proceed without recalled context.`,
            },
          ],
          details: { error: message, binary } as Record<string, unknown>,
        };
      }
    },
  });

  pi.on("tool_result", (event, _ctx) => {
    const payload = buildCapturePayload(
      event.toolName,
      event.input,
      event.content,
    );
    if (payload === null) {
      return;
    }
    shellCapture(resolveCortexBinary(), payload);
  });
}
