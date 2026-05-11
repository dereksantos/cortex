import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

/**
 * Schema for the cortex_recall tool's parameters.
 *
 * `query` is required; `limit` is optional and bounded so the LLM
 * cannot request an unreasonably large result set.
 */
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
 * Cortex extension factory for pi.dev.
 *
 * Phase 8 status:
 *   TODO 3 (this commit): cortex_recall registered, execute stubbed.
 *   TODO 5: execute shells out to `cortex search --format json`.
 *   TODO 7: tool_call hook forwards redacted events to cortex capture.
 */
export default function cortexExtension(pi: ExtensionAPI): void {
  pi.registerTool({
    name: "cortex_recall",
    label: "Cortex Recall",
    description:
      "Recall relevant captured context (decisions, patterns, prior corrections, constraints) for the current task. Use when starting work in an unfamiliar area of the codebase, when the user references prior discussions or decisions, or when a task involves domain knowledge that may have been captured earlier. Returns a short list of relevant context items. Decide what to do with the results — do not act on them unconditionally.",
    parameters: CortexRecallParams,
    execute: async (_toolCallId, _params, _signal, _onUpdate, _ctx) => ({
      content: [
        {
          type: "text" as const,
          text: "[cortex_recall stub — wired to `cortex search --format json` in Phase 8 TODO 5]",
        },
      ],
      details: {},
    }),
  });
}
