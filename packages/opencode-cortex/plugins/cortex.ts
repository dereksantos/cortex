// Cortex plugin for opencode. Phase 8 of the cortex eval-harness program.
//
// TODO 3 (this commit): registers cortex_recall as a tool the agent can
// call to query captured context. The capture-side hook
// (tool.execute.after → cortex capture) is added in TODO 5.
//
// Mirrors packages/pi-cortex/extensions/cortex/index.ts but adapted to
// opencode's plugin API:
//   - async Plugin factory returning Hooks   (vs pi's sync default fn)
//   - Zod 4 (tool.schema)                    (vs pi's typebox)
//   - ToolResult is `{output, metadata?}`    (vs pi's content-array)
//   - Uses ToolContext.directory             (vs process.cwd())
//   - Discovery: flat `.opencode/plugins/cortex.ts`
//                                            (vs `.pi/extensions/cortex/`)

import type { Plugin } from "@opencode-ai/plugin";
import { tool } from "@opencode-ai/plugin/tool";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import {
  formatRecallResults,
  NO_RESULTS_TEXT,
  resolveCortexBinary,
  type RecallEntry,
} from "./_helpers.ts";

const execFileAsync = promisify(execFile);

const SEARCH_TIMEOUT_MS = 5_000;
const SEARCH_MAX_BUFFER = 2 * 1024 * 1024; // 2 MiB — plenty for ≤50 results

const CORTEX_RECALL_DESCRIPTION =
  "Recall relevant captured context (decisions, patterns, prior corrections, constraints) for the current task. " +
  "Use when starting work in an unfamiliar area of the codebase, when the user references prior discussions or decisions, " +
  "or when a task involves domain knowledge that may have been captured earlier. " +
  "Returns a short markdown list of relevant context items. Decide what to do with the results — do not act on them unconditionally.";

export const CortexPlugin: Plugin = async () => ({
  tool: {
    cortex_recall: tool({
      description: CORTEX_RECALL_DESCRIPTION,
      args: {
        query: tool.schema
          .string()
          .describe(
            "Natural-language query to search captured cortex context. " +
              "Examples: 'authentication flow', 'why did we pick pgx over database/sql', " +
              "'previous decisions about retry strategy'.",
          ),
        limit: tool.schema
          .number()
          .int()
          .min(1)
          .max(50)
          .default(5)
          .describe("Maximum number of results to return (1–50, default 5)."),
      },
      async execute({ query, limit }, ctx) {
        const binary = resolveCortexBinary();
        // Prefer $CORTEX_PROJECT_ROOT (set by the eval grid runner to the
        // directory holding .cortex/) over the session's directory; fall
        // back to ctx.directory when unset. Same precedence as pi-cortex.
        const cwd = process.env.CORTEX_PROJECT_ROOT?.trim() || ctx.directory;
        try {
          // IMPORTANT: flags BEFORE positional query.
          // Go's stdlib flag.Parse stops at the first non-flag argument,
          // so `cortex search <query> --format json` silently falls back
          // to the default text format. Same gotcha pi-cortex calls out.
          const { stdout } = await execFileAsync(
            binary,
            ["search", "--format", "json", "--limit", String(limit), query],
            { cwd, timeout: SEARCH_TIMEOUT_MS, maxBuffer: SEARCH_MAX_BUFFER },
          );
          const parsed: unknown = JSON.parse(stdout);
          if (!Array.isArray(parsed)) {
            throw new Error(
              `cortex search returned non-array JSON: ${typeof parsed}`,
            );
          }
          const entries = parsed as RecallEntry[];
          return {
            output: formatRecallResults(entries),
            metadata: { count: entries.length, binary },
          };
        } catch (err) {
          // cortex_recall must NEVER throw — that would break the agent
          // loop. Return a benign result with the error in metadata.
          const message = err instanceof Error ? err.message : String(err);
          return {
            output: `${NO_RESULTS_TEXT} (cortex_recall could not query the store: ${message}.) Proceed without recalled context.`,
            metadata: { error: message, binary },
          };
        }
      },
    }),
  },
});
