# pi.dev extension API — Phase 8 probe notes

Findings from Phase 8 TODO 1 (the `packages/pi-cortex-probe/`
throwaway). Captured against pi-coding-agent **v0.74.0**
(`/opt/homebrew/lib/node_modules/@earendil-works/pi-coding-agent`).
Event-stream evidence: `docs/pi-extension-probe.json`.

## 1. Factory signature

```ts
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

export default function (pi: ExtensionAPI): void | Promise<void> {
  // register tools, hook events, etc.
}
```

The default export is invoked once at extension load. `ExtensionAPI`
is the only argument. Returning a Promise is supported but not
required.

## 2. Tool registration — ToolDefinition shape

The actual shape (from
`dist/core/extensions/types.d.ts:328-359`) is **not** the
`{ name, description, input_schema, run }` shape that the prompt's
TODO 3 illustrative snippet currently shows. The real fields are:

| Field | Required | Notes |
|---|---|---|
| `name` | yes | Tool identifier; what the LLM emits in tool calls |
| `label` | yes | Human-readable label shown in the TUI |
| `description` | yes | LLM-visible description |
| `parameters` | yes | A **TypeBox** `TSchema` (use `Type.Object({...})` from the `typebox` package) |
| `execute` | yes | `(toolCallId, params, signal?, onUpdate?, ctx) => Promise<AgentToolResult>` |
| `promptSnippet` | no | One-line snippet for the "Available tools" section of the system prompt |
| `promptGuidelines` | no | Bullet list appended to the Guidelines section when the tool is active |
| `executionMode` | no | `"sequential"` or `"parallel"` |
| `prepareArguments` | no | Compatibility shim run before schema validation |
| `renderCall` / `renderResult` | no | Custom TUI renderers |

`execute` must return:

```ts
interface AgentToolResult<T> {
  content: (TextContent | ImageContent)[]; // shown to the model
  details: T;                                // structured logs / UI extras
  terminate?: boolean;                       // ends the tool batch early
}
```

**Implication for the cortex extension:** the TODO 3 snippet in
`docs/prompts/pi-extension-prompt.md` should be rewritten to use
`label` + `parameters` + `execute` + `AgentToolResult` (this is
already on the road map for TODO 3 itself — the probe confirms
the target shape).

## 3. Hookable events (`pi.on(...)`)

Full set surfaced by `ExtensionAPI` in v0.74.0:

- `resources_discover`
- Session lifecycle: `session_start`, `session_before_switch`,
  `session_before_fork`, `session_before_compact`,
  `session_compact`, `session_shutdown`, `session_before_tree`,
  `session_tree`
- Context: `context`
- Provider: `before_provider_request`, `after_provider_response`
- Agent: `before_agent_start`, `agent_start`, `agent_end`
- Turn: `turn_start`, `turn_end`
- Message: `message_start`, `message_update`, `message_end`
- Tool: `tool_execution_start`, `tool_execution_update`,
  `tool_execution_end`, `tool_call`, `tool_result`
- Misc: `model_select`, `thinking_level_select`, `user_bash`,
  `input`

**For TODO 7 (capture hook):** `tool_call` fires *before* the
tool runs (good for redaction-gated capture); `tool_result`
fires after with the result. `tool_execution_end` is what shows
up in the `--mode json` event stream — that's the event our
pass criterion #2 should match against (already correctly
named in the prompt).

## 4. Other ExtensionAPI surface worth knowing

These showed up while reading the type and are worth remembering
for later TODOs:

- `registerCommand(name, options)` — slash commands
- `registerShortcut(shortcut, options)` — keyboard shortcuts
- `registerFlag(name, options)` / `getFlag(name)` — CLI flags
- `registerMessageRenderer(customType, renderer)` — custom UI
- `sendMessage(...)` / `sendUserMessage(...)` — programmatic
  message injection (potential vector for proactive injection
  in a future RESOLVE-mode extension; out of scope for Phase 8)
- `appendEntry(customType, data)` — persist arbitrary data on
  the session without sending it to the LLM
- `exec(command, args, options)` — run shell commands (this is
  how cortex_recall will shell out to `cortex search`)
- `getActiveTools()` / `getAllTools()` / `setActiveTools(...)`
- `setModel(...)` / `setThinkingLevel(...)` / `getThinkingLevel()`
- `registerProvider(name, config)` — register custom providers
- `events: EventBus` — shared bus for cross-extension comms

## 5. Install layout — project-local

pi-coding-agent v0.74.0 reads project-local extensions from
`.pi/extensions/<name>/` (configured via `piConfig.configDir`
in its own `package.json`).

What worked for the probe:

```
packages/pi-cortex-probe/
├── index.ts              # default export = factory
├── package.json          # has "pi": { "extensions": ["./index.ts"] }
└── node_modules/         # only `typebox` (the runtime dep)

.pi/extensions/pi-cortex-probe -> ../../packages/pi-cortex-probe   (symlink)
```

The probe's `package.json`:

```json
{
  "name": "pi-cortex-probe",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "pi": { "extensions": ["./index.ts"] },
  "dependencies": { "typebox": "^1.1.24" }
}
```

Notes that matter for TODO 2 (real `packages/pi-cortex/` scaffold):

- Do **not** add `@earendil-works/pi-coding-agent` as a `peerDependency`.
  Doing so triggers npm to pull in pi's full transitive tree
  (~150 packages including @anthropic-ai, @aws, openai, zod, etc.)
  — pi is already installed globally, and the probe needs only
  `typebox`. Use `import type { ExtensionAPI } from "@earendil-works/pi-coding-agent"`
  (type-only) and let the TypeScript compiler resolve the type
  against the user's global pi install when checking, while the
  runtime loader (jiti) resolves the import path from pi's
  installation.
- `type: "module"` is required (ESM); pi loads extensions via
  `jiti`.
- `pi.extensions` array points to the entrypoint(s) relative to
  the package directory.

## 6. Discovery: `pi list` vs `pi config`

A nuance worth recording — **`pi list` does NOT show
project-local extensions discovered from `.pi/extensions/`**.
`pi list` reports only what's been explicitly installed via
`pi install <source>`, which writes to settings.

To verify project-local discovery, use **`pi config`** — its
TUI lists "Project (.pi/) > Extensions > [x] <name>/index.ts"
for each discovered extension.

This means the prompt's **pass criterion #1** ("The extension
loads and pi recognizes it. `pi list` shows it.") is incorrect
for project-local extensions. The right check is one of:

- `pi config` TUI lists the extension under Project > Extensions
  with `[x]` (enabled), **or**
- The extension fires in a real run (a `tool_execution_end`
  event for one of its tools appears in `--mode json` output).

Recommendation: update pass criterion #1 in TODO 9 / pass criteria
to use either of those checks instead of `pi list`. This is a
follow-up doc edit; the probe itself doesn't gate on this.

## 7. Run command that worked

```bash
pi --mode json \
  --provider openrouter \
  --model openai/gpt-oss-20b:free \
  --api-key "$OPEN_ROUTER_API_KEY" \
  -p "Call the pi_cortex_probe tool with no arguments, then summarize what it returned." \
  --no-session
```

The `--api-key` flag is necessary because the env var pi expects
for OpenRouter is named differently than `OPEN_ROUTER_API_KEY` (the
cortex repo's existing convention). Pass it explicitly to avoid a
secret-redaction concern in `tool_call` captures (TODO 7) — the
key is never logged into pi's session output.

The probe fired:

```jsonl
{"type":"tool_execution_start","toolCallId":"chatcmpl-tool-97c7d37e40c1afda","toolName":"pi_cortex_probe","args":{"tool":"pi_cortex_probe","args":{}}}
{"type":"tool_execution_end","toolCallId":"chatcmpl-tool-97c7d37e40c1afda","toolName":"pi_cortex_probe","result":{"content":[{"type":"text","text":"hello from cortex probe"}],"details":{}},"isError":false}
```

Full event stream: `docs/pi-extension-probe.json` (111 lines).

## 8. Carry-forward items for TODOs 2 / 3

1. Rewrite TODO 3's `registerTool` snippet to use the real shape
   (`label` + `parameters: Type.Object(...)` + `execute` returning
   `{content, details}`). Done as part of TODO 3 itself, not now.
2. Replace pass criterion #1 with a `pi config` check or a
   `--mode json` smoke check. Done as part of TODO 9, not now.
3. `packages/pi-cortex/` (TODO 2) should mirror the layout above
   (`type: module`, `pi.extensions` field, typebox-only runtime
   deps, type-only import of `ExtensionAPI`).
4. Add `.pi/` to `.gitignore` for the final extension (the
   symlink to `packages/pi-cortex/` is install-time state, not
   source). The probe lives there transiently and gets deleted
   in TODO 2.
