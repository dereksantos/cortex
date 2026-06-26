# Cortex / `loop`

An interactive coding agent (`cmd/loop`) for small and local models, with
working memory built in. The project was deliberately slimmed to center on
this one binary; the prior `cortex` CLI, eval framework, and Claude-Code
host integration were removed — see [`docs/archive.md`](docs/archive.md)
for what existed before and why it went.

> Direction docs are authoritative for scope. The live direction is the
> **working-memory** line: [`docs/working-memory.md`](docs/working-memory.md)
> (forever-session via continuous context curation) and
> [`docs/working-memory-study.md`](docs/working-memory-study.md)
> (study-as-working-memory). The harness hardening plan is
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
2. **Per-turn capture.** `captureTurn()` records files edited, commands run,
   and the final answer to the journal — mechanical, no model, non-blocking.
   `startDistill()` batches turns through the reasoner model for durable
   insights. `/remember <text>` is the highest-precision capture path.
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

REPL slash commands: `/compact`, `/clear`, `/remember`, `/sessions`,
`/model [name]`, `/quit`. Dispatch is in `cmd/loop/main.go` (subcommands
~`:3354`, slash commands ~`:3544`).

## The agent's tools

Registered in `cmd/loop/tools/tools.go` (`All` at ~`:212`, dispatch in
`ToolCall.Execute()` ~`:236`): `read_file`, `write_file`, `edit_file`,
`study`, `project_index`, `bash`, `remove_path`.

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

Roles: `code` (the agent), `study` (the navigator + the summarizer), and the
cognition roles for retrieval — `embed`, plus `fast` (drives Reflect rerank +
Dream). Auth is resolved at call time from `key_env` (env-var name) or
`key_service` (macOS keychain) — never written to disk.

**Retrieval embedder resolution** (`CortexSession.resolveEmbedder`): (1) the
`embed` role if configured/discovered — the fleet's `embedder`, or any
OpenAI-compatible `/embeddings` endpoint such as a free **Cloudflare Workers AI
bge-large** (1024-d, like the fleet) or Gemini; (2) otherwise the self-contained
**Hugot** pure-Go local embedder (384-d, no server/keys/network at steady
state) — the zero-config default, warmed in the background so its one-time model
download never blocks a turn. local-only is a goal, not a constraint: the cloud
option is allowed and ships captured content out. Switching embedders changes
the vector dim and orphans prior vectors until they re-embed (cheap — they
regenerate from capture).

Env knobs: `CORTEX_{BACKEND,HOME}`, `CORTEX_LOOP_STUDY_WINDOW`, `CORTEX_NAV_REPS`,
`CORTEX_LOOP_RENDER`, `NO_COLOR`, `DISCORD_{BOT_TOKEN,CHANNEL_ID,SESSION_ID}`,
`CORTEX_LOCAL_EMBED` (set falsey to skip the local Hugot default → text-only
retrieval), `CORTEX_HUGOT_ONNX` (pick the ONNX variant; default is an
arch-matched int8 build), `CORTEX_REFLEX_EMBED_TIMEOUT_MS` (per-query embed
budget, default 1000).

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
- `internal/study/` — structural boundary analysis + sampler (used by the DAG)
- `internal/journal/` — append-only event log
- `internal/shellrisk/` — command risk classifier
- `pkg/cognition/dag/` — DAG engine + op registry
- `pkg/llm/` — LLM providers
- `pkg/config/` — layered config
