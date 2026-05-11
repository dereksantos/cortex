import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

const NO_RESULTS_TEXT = "No relevant context captured yet.";
const DEFAULT_BINARY = "cortex";
const SEARCH_TIMEOUT_MS = 5_000;
const SEARCH_MAX_BUFFER = 2 * 1024 * 1024; // 2 MiB — plenty for ≤50 results

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
 * Cortex extension factory for pi.dev.
 *
 * Phase 8 status:
 *   TODO 5 (this commit): cortex_recall shells out to
 *                         `cortex search --format json`. Failures
 *                         degrade to a benign text result so the
 *                         agent loop continues.
 *   TODO 7: tool_call hook forwards redacted events to cortex
 *           capture.
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
}
