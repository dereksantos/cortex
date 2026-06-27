# Cortex / `loop`

An interactive coding agent (`cmd/loop`) for small and local models, with
working memory built in. The project was deliberately slimmed to center on
this one binary; the prior `cortex` CLI, eval framework, and Claude-Code
host integration were removed — see [`docs/archive.md`](docs/archive.md)
for what existed before and why it went.

> Direction docs are authoritative for scope. The **live** direction is
> [`docs/memory-tools.md`](docs/memory-tools.md): memory is **tools the model
> drives** (`memory_write/read/search/forget` + `study(journal)`) over free-form
> named notes + an injected index — NOT a mechanical retrieval/distill pipeline.
> It supersedes the mechanical memory line —
> [`docs/memory-distillation.md`](docs/memory-distillation.md),
> [`docs/working-memory.md`](docs/working-memory.md),
> [`docs/working-memory-study.md`](docs/working-memory-study.md) — which is kept
> for history. The harness hardening plan is
> [`docs/loop-production-harness.md`](docs/loop-production-harness.md).

## What `loop` is

A single long-lived REPL process. The turn loop:

```
read input → run agentic tool calls → capture the turn → curate context → reply
```

Sessions accumulate across turns and persist as raw JSONL transcripts in
`.cortex/sessions/<id>.jsonl` (resumable). The agent reads `AGENTS.md` from
the repo root into its seed if present.

Three capabilities distinguish it:

1. **Working memory.** When the context window fills (~80%, `compactThreshold`,
   or on `/compact`), the conversation is folded into a digest by the
   sequential chunk-and-fold summarizer (`cmd/loop/summarize.go`) and the
   session continues from that instead of truncating.
2. **Model-driven memory + per-turn capture.** The agent curates durable
   free-form notes through the `memory_write/read/search/forget` tools
   (`internal/memory`); the note index is injected at turn start
   (`memoryIndexNote`) so a fresh session knows what it can recall. Separately,
   `captureTurn()` records each turn (files edited, commands run, final answer)
   to the append-only journal — mechanical, no model — the record
   `study(.cortex/journal)` reads on demand. See
   [`docs/memory-tools.md`](docs/memory-tools.md).
3. **Map-first `study`.** The study tool is a bounded read-only subagent
   (`cmd/loop/navigator.go`): it leads with a free structural map of the
   target, reads only the goal-relevant regions in tiny ranges, and can spawn
   a bounded sub-study for a neighbor file/dir.

## Commands

| Command | Purpose |
|---|---|
| `loop` | Interactive REPL (default) |
| `loop resume [id]` | Resume a prior session (default: latest) |
| `loop turn [--session id] [--json] <input...>` | Headless single turn (drivers/scripts); session id → stderr |
| `loop study <path> [goal...]` | One-off study (map-first navigator); prints the digest |
| `loop change <start\|commit\|status>` | Git change lifecycle — one reviewable change at a time (local git only) |
| `loop discord` | Discord adapter (token from `DISCORD_BOT_TOKEN`) |
| `loop study-eval` | Navigator acceptance test (pass/fail + latency; `CORTEX_NAV_REPS` reps) |

REPL slash commands: `/compact`, `/clear`, `/sessions`, `/model [name]`,
`/quit`. Dispatch is in `cmd/loop/main.go` (subcommands ~`:3354`, slash
commands ~`:3544`). Memory is model-driven — ask in natural language
("remember that …" / "forget the … note") and the agent calls the memory
tools; the old `/remember` and `/forget` slash commands were removed with the
mechanical capture/retract pipeline.

## The agent's tools

Registered in `cmd/loop/tools/tools.go` (`All` + dispatch in
`ToolCall.Execute()`): `read_file`, `write_file`, `edit_file`, `study`,
`project_index`, `bash`, `remove_path`, and the model-driven memory tools
`memory_write`, `memory_read`, `memory_search`, `memory_forget`
(`internal/memory`).

- `read_file` refuses files over `CurationBudgetTokens` (16000) and
  redirects to `study`; large Go files return a declaration skeleton.
- `edit_file` is exact-match-first, whitespace-tolerant on retry; prefer it
  over `write_file` for edits.
- `bash` is gated by `internal/shellrisk`: Safe runs, Risky prompts (judged
  against `turnIntent`), Blocked refuses. Headless sessions treat Risky as
  Blocked.
- `remove_path` is workspace-confined (`.git`/`.cortex`/root refused);
  disabled by `tools.allow_delete: false`.

## Configuration

Layered, lowest→highest: `~/.cortex/config.json` (user) →
`./.cortex/config.json` (project, field-by-field override) → `CORTEX_BACKEND`
env. Loaded by `LoadConfig()` / `loadMergedConfig()` in `main.go`.

```json
{
  "backend": { "type": "openrouter", "endpoint": "...", "key_env": "OPENROUTER_API_KEY" },
  "models": {
    "code":  { "model": "...", "window": 131072 },
    "study": { "model": "..." },
    "embed": { "endpoint": "https://api.cloudflare.com/client/v4/accounts/<id>/ai/v1",
               "model": "@cf/baai/bge-large-en-v1.5", "key_env": "CLOUDFLARE_API_TOKEN" }
  },
  "tools": { "allow_delete": true, "delete_root": "" }
}
```

Roles: `code` (the agent) and `study` (the navigator + the summarizer). Auth is
resolved at call time from `key_env` (env-var name) or `key_service` (macOS
keychain) — never written to disk. (`embed`/`fast` role bindings remain in
config reserved for a future semantic `memory_search`; the loop's hot path no
longer uses them — `memory_search` is keyword over the small note corpus. The
mechanical retrieve/rerank/Dream pipeline (`pkg/cognition/dag`,
`internal/cognition`) and the blind-sampling study engine (`internal/study`)
were deleted outright — see [`docs/archive.md`](docs/archive.md). The embedder
resolution helper `CortexSession.resolveEmbedder` is kept, reserved for that
future swap.)

Env knobs: `CORTEX_{BACKEND,HOME}`, `CORTEX_LOOP_STUDY_WINDOW`, `CORTEX_NAV_REPS`,
`CORTEX_LOOP_RENDER`, `NO_COLOR`, `DISCORD_{BOT_TOKEN,CHANNEL_ID,SESSION_ID}`,
`CORTEX_LOCAL_EMBED` (set falsey to skip the local Hugot embedder default),
`CORTEX_HUGOT_ONNX` (pick the ONNX variant; default is an arch-matched int8
build).

## Journal — source of truth

CQRS event-sourcing: the append-only JSONL journal (`.cortex/journal/<class>/`)
is canonical; storage is regeneratable from it. Per-segment flock makes
capture cross-process safe. See [`docs/journal.md`](docs/journal.md).
Invariants still enforced: **local-only by default**
(`journal.AssertLocalOnly` is a code-review tripwire for outbound paths),
**`.cortex/` in `.gitignore`**, **jq-readable plain JSONL**, closed
segments gzippable.

## Go patterns

**Error handling**: wrap with context — `fmt.Errorf("failed to X: %w", err)`.

**Naming**: constructors `NewXxx(cfg *config.Config)`; interfaces are nouns
(`Provider`, `Storage`), not `IProvider`.

**Package structure**: `cmd/` entry points, `internal/` private impl,
`pkg/` public API.

**LLM calls**: go through the `pkg/llm` provider interface (Anthropic,
Ollama, OpenRouter, OpenAI-compatible). There is exactly one LLM layer —
`pkg/llm`. (The old duplicate `internal/llm` was removed.)

## Constraints

**Testing**: standard library `testing` only.
- Assertions via `t.Errorf` / `t.Fatalf` / `t.Fatal` — no testify/assert.
- Table-driven tests with `t.Run` subtests.
- Setup/teardown via `defer` (e.g. `defer os.RemoveAll(tempDir)`).

**Checks**: `./scripts/check.sh [fmt|vet|lint|all]` runs gofmt + `go vet`
+ golangci-lint (the same gate CI runs). Keep `go build ./...`, `go vet`,
and the test suite green.

## Build & test

```bash
go build ./cmd/loop          # build the binary
go test ./...                # full suite
./scripts/check.sh           # fmt + vet + lint
```

## Key files

- `cmd/loop/main.go` — REPL, `CortexSession`, turn loop, dispatch, config
- `cmd/loop/tools/tools.go` — the agent's tool surface
- `cmd/loop/navigator.go` — the study tool (map-first read-only subagent)
- `cmd/loop/summarize.go` — free-text summarizer (compaction + shell-output)
- `internal/projectindex/` — the structural map (`go/ast` + outline tiers)
- `internal/journal/` — append-only event log
- `internal/shellrisk/` — command risk classifier
- `pkg/llm/` — LLM providers
- `pkg/config/` — layered config
