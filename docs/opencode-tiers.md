# opencode CLI — event-stream schema (probe results)

Source: `docs/opencode-probe.json` (one run of
`opencode run --format json --dir <scratch> "<prompt>"` against
`openrouter/openai/gpt-oss-20b:free`, opencode 1.14.46, 2026-05-10).

Used by `internal/eval/v2/library_service_opencode_harness.go` (Phase 7
TODO 2) to parse a HarnessResult out of stdout.

## Invocation

```
opencode run \
    --model openrouter/openai/gpt-oss-20b:free \
    --dir   <workdir> \
    --format json \
    "<prompt>"
```

- `--dir` is sufficient for the model to discover, read, and edit files
  in the workdir. **No `--file` / `--add` flag is needed** — the probe
  used `--dir` only and the model successfully globbed → read → edited
  `hello.go`. (Contrast: aider requires explicit `--file` on each path.)
- The OpenRouter API key must be exposed as `OPENROUTER_API_KEY` in the
  child env; the project's underscore form `OPEN_ROUTER_API_KEY` is
  not auto-detected by the opencode SDK.
- Exit code: 0 on a successful session. Non-zero on hard failures (auth,
  model unavailable, CLI error). The stream itself doesn't emit a
  "session_end" or "exit_reason" event — `cmd.Wait()` is the source of
  truth for completion.
- Permissions: opencode's `run` subcommand is non-interactive and did
  not require `--dangerously-skip-permissions` for `edit`/`write`/`bash`
  tool calls against a scratch dir in our probe. (Behavior may differ
  inside a git repo; revisit if smoke tests hang.)

## Output format

`--format json` emits **newline-delimited JSON** — one event per line on
stdout. Stderr is empty on a clean run.

## Event envelope

Every event has the same top-level shape:

```json
{
  "type": "<event_type>",
  "timestamp": 1778448547865,
  "sessionID": "ses_...",
  "part": { ... }
}
```

`part` is the event-specific payload. The keys we parse all live there.

## Event types observed

| `type`        | Emitted when                                                    |
|---------------|-----------------------------------------------------------------|
| `step_start`  | A new reasoning/tool step begins. `part.type = "step-start"`.   |
| `tool_use`    | A tool was called. `part.tool` names it.                        |
| `text`        | Model-generated commentary. `part.text` carries the string.     |
| `step_finish` | A step closed. Carries per-step token + cost rollup.            |

Other types may exist (e.g. for streaming errors, agent handoffs); these
are the four observed in a one-shot edit session. Unknown event types
should be tolerated (skipped) by the parser, not fatal.

## Token + cost rollup

`step_finish.part` carries:

```json
{
  "reason": "tool-calls",      // or "stop", "length", "error"
  "tokens": {
    "total":      9396,
    "input":      9277,
    "output":     28,
    "reasoning":  11,
    "cache": { "write": 0, "read": 80 }
  },
  "cost": 0
}
```

- **Per-step**, not cumulative. The harness must SUM
  `part.tokens.input` / `.output` / `part.cost` across all
  `step_finish` events in the stream to get session totals.
- `tokens.input` includes the cache-read portion; the cache split lives
  under `tokens.cache.read`. For the eval `tokens_in` column we report
  the raw `input` figure (matches how aider's litellm output rolls up).
- `cost` is a number (USD float), `0` on `:free` models. Some providers
  may return `null` here — treat absent/non-number as 0.
- The **final** step may end without a matching `step_finish` event
  (the stream just stops on the last `text`). The probe's last 2 events
  were `step_start` + `text` with no closing `step_finish`. Parser must
  not require a step_finish per step_start.

## File edits

The model's file edits come through as `tool_use` events:

```json
{
  "type": "tool_use",
  "part": {
    "type": "tool",
    "tool": "edit",
    "state": {
      "status": "completed",
      "input":  { "filePath": "/abs/path/to/file.go",
                  "oldString": "...", "newString": "...", "replaceAll": false },
      "output": "Edit applied successfully.",
      "metadata": { "diff": "...", "filediff": { "additions": 1, "deletions": 1 } }
    }
  }
}
```

For `tokens_in > 0` and `files_changed` population we collect every
`tool_use` where:

- `part.tool ∈ {edit, write}`
- `part.state.status == "completed"`

…and read `part.state.input.filePath`. Other tools observed but
not relevant for `files_changed`: `glob`, `read`, `bash`, `grep`,
`webfetch`, `skill`, `task`, `todowrite`, `invalid`.

A model that picks a bogus tool name (e.g. `apply_patch`) surfaces as
`tool: "invalid"` with `state.status == "completed"` and an error
message in `state.output`. These do NOT count as edits.

## Mapping to HarnessResult fields

| HarnessResult field   | Source                                                                              |
|-----------------------|-------------------------------------------------------------------------------------|
| `TokensIn`            | sum of `part.tokens.input` over `step_finish` events                                |
| `TokensOut`           | sum of `part.tokens.output` over `step_finish` events                               |
| `CostUSD`             | sum of `part.cost` over `step_finish` events                                        |
| `AgentTurnsTotal`     | count of `step_start` events (or `step_finish`, whichever is higher)                |
| `FilesChanged`        | unique `part.state.input.filePath` from completed `edit`/`write` `tool_use` events  |
| `LatencyMs`           | wall-clock time around `cmd.Run()` (NOT derived from event timestamps)              |
| `ModelEcho`           | the `--model` value passed in                                                       |
| `ProviderEcho`        | leading segment of `--model` before the first `/` (e.g. `openrouter`)               |

## Gotchas seen in the probe

1. **Stream may end mid-step**: the last event was a `text`, not a
   `step_finish`. Don't assume `step_finish` closes every `step_start`.
2. **Per-step token totals**: must sum, not pick the last value. The
   final `step_finish` reports only the last step's tokens.
3. **Path doubling on `read`**: an early `read` call by the model
   passed a workdir-relative path and opencode joined it to `--dir`,
   producing a doubled path. This is a model behavior, surfaced as
   `state.status == "error"`. The harness should treat tool errors as
   non-fatal — the model recovered and the session still succeeded.
4. **`invalid` tool**: when the model hallucinates a tool name (e.g.
   `apply_patch`), opencode surfaces it as `tool: "invalid"`. Don't
   count these as edits.
5. **Cost field type**: float on free tier (we saw `0`). Treat
   non-numeric / missing values as 0 rather than failing the parse.
