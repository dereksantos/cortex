// Cortex plugin for opencode. Phase 8 of the cortex eval-harness program.
//
// TODO 3: registers cortex_recall as a tool the agent can call to query
//         captured context.
// TODO 5 (this commit): hooks tool.execute.after to forward redacted
//         tool-call events for allowlisted tools (edit, write, bash,
//         cortex_recall) to `cortex capture --type opencode_tool_call`.
//         Read / glob / find / ls / grep are intentionally excluded so
//         the event log isn't buried in noise.
//
// Mirrors packages/pi-cortex/extensions/cortex/index.ts but adapted to
// opencode's plugin API:
//   - async Plugin factory returning Hooks   (vs pi's sync default fn)
//   - Zod 4 (tool.schema)                    (vs pi's typebox)
//   - ToolResult is `{output, metadata?}`    (vs pi's content-array)
//   - tool.execute.after(input, output) with input.{tool,sessionID,callID,args}
//                                            (vs pi's tool_result event with toolName/input/content)
//   - Uses ToolContext.directory             (vs process.cwd())
//   - Discovery: flat `.opencode/plugins/cortex.ts`
//                                            (vs `.pi/extensions/cortex/`)

import type { Plugin } from "@opencode-ai/plugin";
import { tool } from "@opencode-ai/plugin/tool";
import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";
import {
  CAPTURE_ALLOWLIST,
  formatRecallResults,
  NO_RESULTS_TEXT,
  redactSecrets,
  resolveCortexBinary,
  RESULT_SUMMARY_MAX,
  type RecallEntry,
} from "./_helpers.ts";

const execFileAsync = promisify(execFile);

const SEARCH_TIMEOUT_MS = 5_000;
const SEARCH_MAX_BUFFER = 2 * 1024 * 1024; // 2 MiB — plenty for ≤50 results

/**
 * Required shape of a captured `opencode_tool_call` row. Parallels
 * pi-cortex's PiToolCallCapture but emits `opencode_tool_call` so
 * source attribution stays clean across harnesses in the events table.
 * Downstream Dream sources read these as structured data — the shape
 * is part of the cortex contract.
 *
 * NOT exported — opencode's plugin loader iterates every named export
 * looking for Plugin functions. Exporting helper functions/types from
 * a discovered plugin file confuses the loader (observed: "plugin
 * config hook failed" with `O.config` evaluation on null/undefined).
 * Keep helpers file-local.
 */
type OpencodeToolCallCapture = {
  tool_name: string;
  args_redacted: Record<string, unknown>;
  result_summary: string;
  captured_at: string;
  session_id?: string;
};

/**
 * Truncate opencode's string-typed tool output to RESULT_SUMMARY_MAX.
 * Opencode's tool.execute.after delivers `output.output` as a string
 * (per dist/index.d.ts), so the array-walking summarizeResultContent
 * helper from pi-cortex doesn't apply here.
 */
function summarizeStringOutput(s: unknown, maxChars: number = RESULT_SUMMARY_MAX): string {
  if (typeof s !== "string") return "";
  if (s.length <= maxChars) return s;
  return s.slice(0, maxChars) + "…";
}

/**
 * Build the schema-conformant capture payload for a single allowlisted
 * tool result. Returns null when the tool is not on the capture
 * allowlist — callers can short-circuit instead of spawning cortex
 * capture.
 */
function buildOpencodeCapturePayload(
  toolName: string,
  args: unknown,
  output: unknown,
  sessionId?: string,
): OpencodeToolCallCapture | null {
  if (!CAPTURE_ALLOWLIST.has(toolName)) {
    return null;
  }
  const redacted = redactSecrets(args);
  const payload: OpencodeToolCallCapture = {
    tool_name: toolName,
    args_redacted: (redacted && typeof redacted === "object" ? redacted : {}) as Record<
      string,
      unknown
    >,
    result_summary: summarizeStringOutput(output),
    captured_at: new Date().toISOString(),
  };
  if (sessionId && sessionId.length > 0) {
    payload.session_id = sessionId;
  }
  return payload;
}

/**
 * Fire-and-forget shell-out to `cortex capture --type opencode_tool_call
 * --content <json>`. Never blocks the agent loop:
 *   - spawn instead of execFile (no pipe buffering wait)
 *   - stdio is ignored end-to-end
 *   - child.unref() so a long-running capture doesn't keep opencode
 *     alive past its turn
 *   - errors are silently swallowed (capture is best-effort)
 *
 * cwd: `cortex capture` walks up from its cwd looking for
 * `.cortex/config.json`. Opencode's per-session cwd may be the cell's
 * temp workdir (no .cortex/), so capture would silently fail. We use
 * $CORTEX_PROJECT_ROOT when set; otherwise fall back to the project
 * directory captured from PluginInput at plugin-load time.
 */
function shellCapture(
  binary: string,
  payload: OpencodeToolCallCapture,
  fallbackCwd: string,
): void {
  const json = JSON.stringify(payload);
  const projectRoot = process.env.CORTEX_PROJECT_ROOT?.trim();
  const cwd = projectRoot && projectRoot.length > 0 ? projectRoot : fallbackCwd;
  try {
    const child = spawn(
      binary,
      ["capture", "--type", "opencode_tool_call", "--content", json],
      {
        stdio: ["ignore", "ignore", "ignore"],
        detached: false,
        cwd,
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

const CORTEX_RECALL_DESCRIPTION =
  "Recall relevant captured context (decisions, patterns, prior corrections, constraints) for the current task. " +
  "Use when starting work in an unfamiliar area of the codebase, when the user references prior discussions or decisions, " +
  "or when a task involves domain knowledge that may have been captured earlier. " +
  "Returns a short markdown list of relevant context items. Decide what to do with the results — do not act on them unconditionally.";

export const CortexPlugin: Plugin = async ({ directory: pluginDirectory }) => ({
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

  // tool.execute.after fires after every tool call. We capture an
  // allowlisted subset (edit/write/bash/cortex_recall) to keep signal
  // high — read/glob/find/ls/grep would bury the event log in noise.
  //
  // The hook MUST resolve quickly (return Promise<void>) — opencode
  // awaits it. shellCapture is fire-and-forget (spawn + unref), so the
  // hook returns as soon as the child is launched, never blocking the
  // agent loop.
  async "tool.execute.after"(input, _output) {
    const payload = buildOpencodeCapturePayload(
      input.tool,
      input.args,
      _output.output,
      input.sessionID,
    );
    if (payload === null) {
      return;
    }
    shellCapture(resolveCortexBinary(), payload, pluginDirectory);
  },
});
