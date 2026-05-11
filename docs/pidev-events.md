# pi.dev CLI — event-stream schema (probe results)

Source: `docs/pidev-probe.json` (one run of
`pi --mode json --provider openrouter --model openai/gpt-oss-20b:free -p "<prompt>"`,
pi 0.74.0, 2026-05-10).

Used by `internal/eval/v2/library_service_pidev_harness.go` (Phase 7
TODO 6) to parse a HarnessResult out of stdout.

## Invocation

```
pi --mode json \
   --provider openrouter \
   --model   openai/gpt-oss-20b:free \
   -p "<prompt>"
```

- `-p` (= `--print`) is the non-interactive flag — pi exits after the
  prompt completes.
- The prompt is the last positional argument (per `pi [options] [@files...] [messages...]`).
- **Working directory**: pi operates against `cmd.Dir`. The probe set
  `cmd.Dir = <scratch>` and no `--add`/`--file` flag was needed — the
  model's first move was `bash: ls -R .` and it found `hello.go`
  immediately. The session-opening `type:"session"` event echoes the
  resolved `cwd` for verification.
- **No `~/.pi/agent/models.json` is required** for OpenRouter use.
  Setting `OPENROUTER_API_KEY` in the child env plus passing
  `--provider openrouter --model <openrouter-model-slug>` is sufficient.
  pi 0.74.0 ships built-in support for `openrouter` as a provider.
- **Env**: `OPENROUTER_API_KEY` is the canonical name pi reads (listed
  in `pi --help`'s env-var section). The harness must re-export
  `OPEN_ROUTER_API_KEY` → `OPENROUTER_API_KEY` for the child env, the
  same as AiderHarness and OpenCodeHarness.
- **Exit code**: 0 on success. Non-zero on hard failures.

## Output format

`--mode json` emits **newline-delimited JSON** — one event per line on
stdout. The probe captured 206 events / 244 KB for a single-edit
session. Stderr is empty on a clean run.

## Event types observed

| `type`                  | Emitted when                                                       |
|-------------------------|--------------------------------------------------------------------|
| `session`               | First line; carries `id`, `version`, `cwd`, `timestamp`.           |
| `agent_start`           | Agent loop begins.                                                 |
| `turn_start`            | A model turn begins.                                               |
| `message_start`         | A message in the conversation starts streaming.                    |
| `message_update`        | Streaming chunk inside a message (omit from parsing — noisy).      |
| `message_end`           | A message completes. Assistant messages carry `usage` + `cost`.    |
| `tool_execution_start`  | A tool call begins.                                                |
| `tool_execution_update` | Streaming tool output (omit from parsing).                         |
| `tool_execution_end`    | Tool call completes with `result` + `isError`.                     |
| `turn_end`              | A turn completes. Duplicates `message_end`'s assistant payload.    |
| `agent_end`             | Agent loop ends. Carries the entire `messages` log.                |

Unknown event types should be tolerated (skipped) by the parser, not
fatal.

## Token + cost rollup

Token usage and per-call cost live on **`message_end` events with
`message.role == "assistant"`**. Schema:

```json
{
  "type": "message_end",
  "message": {
    "role": "assistant",
    "api":   "openai-completions",
    "provider": "openrouter",
    "model": "openai/gpt-oss-20b:free",
    "usage": {
      "input":      605,
      "output":     47,
      "cacheRead":  544,
      "cacheWrite": 0,
      "totalTokens": 1196,
      "cost": {
        "input":     0,
        "output":    0,
        "cacheRead": 0,
        "cacheWrite": 0,
        "total":     0
      }
    },
    "stopReason":  "toolUse",
    "responseId":  "gen-...",
    "timestamp":   1778449155238
  }
}
```

- **Per-turn**, not cumulative. Sum `message.usage.input` and
  `message.usage.output` across all assistant `message_end` events.
- `message.usage.cacheRead` is the cache-hit portion (already included
  in `input`). For `tokens_in` we report `usage.input` raw, matching
  the convention OpenCodeHarness uses.
- `message.usage.cost.total` is the per-turn USD cost. Free models
  report `0`.
- **`turn_end` and `message_end` carry the same `usage` block** for an
  assistant turn — picking both would double-count. **Parse
  `message_end` only.**
- User and toolResult `message_end` events have no `usage` field;
  skip them.

## File edits

File edits come through as `tool_execution_end` events:

```json
{
  "type": "tool_execution_end",
  "toolCallId": "chatcmpl-tool-...",
  "toolName": "edit",
  "args": {
    "path": "hello.go",
    "edits": [{"oldText": "...", "newText": "..."}]
  },
  "result": {
    "content": [{"type": "text", "text": "Successfully replaced 1 block(s) in hello.go."}],
    "details": {"diff": "...", "firstChangedLine": 7}
  },
  "isError": false
}
```

For `FilesChanged` we collect every `tool_execution_end` where:

- `toolName ∈ {edit, write}`
- `isError == false`

…and read `args.path`. The path is relative to `cmd.Dir` for `edit`,
absolute for `write` (depending on what the model passed). Other
toolNames observed but not relevant: `read`, `bash`, `grep`, `find`,
`ls`. The full set is documented in `pi --help`'s tool list.

## Mapping to HarnessResult fields

| HarnessResult field   | Source                                                                                   |
|-----------------------|------------------------------------------------------------------------------------------|
| `TokensIn`            | Σ `message.usage.input` over `message_end` events where `message.role == "assistant"`    |
| `TokensOut`           | Σ `message.usage.output` over same                                                       |
| `CostUSD`             | Σ `message.usage.cost.total` over same                                                   |
| `AgentTurnsTotal`     | count of `turn_start` events                                                             |
| `FilesChanged`        | unique `args.path` from `tool_execution_end` with `toolName ∈ {edit, write}` + `!isError` |
| `LatencyMs`           | wall-clock around `cmd.Run()`                                                            |
| `ModelEcho`           | the `--model` value passed in                                                            |
| `ProviderEcho`        | the `--provider` value passed in (or `message.provider` from any assistant `message_end`)|

## Gotchas seen in the probe

1. **Double counting**: `turn_end` and `message_end` both carry an
   identical `usage` object for assistant turns. Parse one OR the
   other, not both. We pick `message_end`.
2. **Role gating**: `message_end` is emitted for `user`,
   `assistant`, and `toolResult` roles. Only `assistant` has
   `usage` — gate on role before summing.
3. **`agent_end` includes the full message log**: tempting to use as
   a single-source-of-truth, but it's redundant with `message_end`
   sums and balloons the parser. Skip.
4. **`message_update` events are streaming chunks**: there can be
   many of them per message. They have no `usage`. Skip.
5. **Path style**: `edit` tool calls use `path` (relative to cwd);
   `write` tool calls may use absolute paths. Treat as opaque
   strings for the `FilesChanged` set.
6. **No `cost` field?**: when running an OpenRouter `:free` model,
   `cost.total` is `0`. If pi ever omits the `cost` object (e.g. for
   local models), the parser must treat absent as 0, not as panic.

## OpenRouter provider check

The probe ran with `--provider openrouter` and no models.json
configuration. The agent_end log confirmed `provider: "openrouter"` in
every assistant message. **No models.json file is required for the
Phase 7 grid runner.** If a future model needs a custom endpoint, see
`PI_CODING_AGENT_DIR` and the `pi config` TUI.
