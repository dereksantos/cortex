# Cortex

An interactive coding agent (`cmd/cortex`) for small and local models, with
working memory built in. The project was deliberately slimmed to center on
this one binary; the prior `cortex` CLI, eval framework, and Claude-Code
host integration were removed — see [`docs/archive.md`](docs/archive.md)
for what existed before and why it went.

> Direction docs are authoritative for scope. The **live** direction is
> [`docs/memory-tools.md`](docs/memory-tools.md): memory is **tools the model
> drives** (`memory_write/read/search/forget` + `study(journal)`) over free-form
> named notes + an injected index — NOT a mechanical retrieval/distill pipeline.
> It supersedes the mechanical memory line —
> [`docs/archive/memory-distillation.md`](docs/archive/memory-distillation.md),
> [`docs/archive/working-memory.md`](docs/archive/working-memory.md),
> [`docs/archive/working-memory-study.md`](docs/archive/working-memory-study.md) — which is kept
> for history. The harness hardening plan is
> [`docs/cortex-production-harness.md`](docs/cortex-production-harness.md).
>
> The **loop/study refactor is SHIPPED** (was specified by
> [`docs/engine-unification.md`](docs/engine-unification.md) +
> [`docs/study-subagent.md`](docs/study-subagent.md)): the two tool-iteration
> loops collapsed into one `runLoop` engine driven by a `Sender` + `AgentDispatcher`
> seam; study is now a bounded `Study` subagent on that engine using
> `outline`/`grep`/targeted `read_file` (no recursion). `navigator.go` +
> `internal/projectindex/` are gone; the tool-call vocabulary lives in
> `internal/agent`, the tool surface in `internal/tools`, the structural map in
> `internal/outline`. Everything below describes today's (post-refactor) code.

## What Cortex is

A single long-lived REPL process. The turn loop:

```
read input → run agentic tool calls → capture the turn → curate context → reply
```

Sessions accumulate across turns and persist as raw JSONL transcripts in
`.cortex/sessions/<id>.jsonl` (resumable). The agent reads `AGENTS.md` from
the repo root into its seed if present.

Three capabilities distinguish it:

1. **Working memory.** The window is a two-zone cache over the immutable
   transcript ([`docs/context-architecture.md`](docs/context-architecture.md)):
   an append-stable prefix (system + a deterministic outline of demoted turns +
   the memory index) and a watermarked hydrated tail (last turns verbatim,
   drains W/2→W/3). Old turns demote mechanically to outline lines with
   `@session/…#m…-…` citations; `recall(citation)` fetches the raw messages
   back; the outline folds via the summarizer only past W/8
   (`cmd/cortex/demote.go`, `internal/cache/`). Prompt size stays bounded
   forever — the old ~80% `Compact` (chunk-and-fold summarize,
   `cmd/cortex/summarize.go`, `/compact`) survives only as a safety net.
2. **Model-driven memory + per-turn capture.** The agent curates durable
   free-form notes through the `memory_write/read/search/forget` tools
   (`internal/memory`); the note index is injected at turn start
   (`memoryIndexNote`) so a fresh session knows what it can recall. Separately,
   `captureTurn()` records each turn (files edited, commands run, final answer)
   to the append-only journal — mechanical, no model — the record
   `study(.cortex/journal)` reads on demand. See
   [`docs/memory-tools.md`](docs/memory-tools.md).
3. **`study` — a bounded read-only subagent.** `study(path, goal)` runs the
   `Study` profile on the shared `runLoop` engine (`cmd/cortex/study.go`): seeded
   with a structural `outline` of the target plus the goal, it works a small
   bounded loop — `outline`, `grep`, `read_file` — to locate exactly the
   goal-relevant code, read those spans, and report a digest. It does not see the
   coder's conversation and cannot recurse. Engineered so narrow is the only
   option (`read_file` is a clamped span; whole-file reads above a small floor are
   refused → `outline` first) and the obvious one (`outline`/`grep` hand back exact
   line numbers). See [`docs/study-subagent.md`](docs/study-subagent.md).

## Commands

| Command | Purpose |
|---|---|
| `cortex` | Interactive REPL (default) |
| `cortex resume [id]` | Resume a prior session (default: latest) |
| `cortex turn [--session id] [--json] <input...>` | Headless single turn (drivers/scripts); session id → stderr |
| `cortex study <path> [goal...]` | One-off study (the `Study` subagent); prints the digest |
| `cortex change <start\|commit\|status>` | Git change lifecycle — one reviewable change at a time (local git only) |
| `cortex serve [--port <n>]` | Local HTTP/SSE adapter for the web UI (loopback-only, bearer-token authenticated) |
| `cortex scan [--json] [--root <path>] [--register]` | Scan configured roots and list discovered projects |
| `cortex project <add\|list\|remove>` | Manage the project registry |
| `cortex discord` | Discord adapter (token from `DISCORD_BOT_TOKEN`) |
| `cortex study-eval` | Study acceptance test (ø gate: goal-hit + clean-finalize + bounded; `CORTEX_STUDY_REPS` reps) |
| `cortex model [--json]` | Catalog code/study role bindings + what the backend serves; suggest a `models` config block from detected RAM |

REPL slash commands: `/compact`, `/clear`, `/sessions`, `/model [name]`,
`/quit`. Dispatch is in `cmd/cortex/main.go` (subcommands ~`:237`, slash
commands ~`:400`). Memory is model-driven — ask in natural language
("remember that …" / "forget the … note") and the agent calls the memory
tools; the old `/remember` and `/forget` slash commands were removed with the
mechanical capture/retract pipeline.

## The agent's tools

Registered in `internal/tools/tools.go` (`All` + dispatch in
`tools.Execute()`): `read_file`, `write_file`, `edit_file`, `study`, `agent`, `outline`,
`grep`, `bash`, `remove_path`, `web_search`, `fetch_url`, `recall` (resolves a session-outline citation
to the verbatim demoted messages; coder-only — not in the Study profile), and
the model-driven memory tools
`memory_write`, `memory_read`, `memory_search`, `memory_forget`
(`internal/memory`), and the context self-curation tools
`context_evict`, `context_merge`, `context_adjust_watermarks`.
(`project_index` was replaced by `outline` + `grep`.)

- `agent` is a general implementation subagent (`docs/agent-tool.md`):
  Study's read set plus `write_file`/`edit_file`/`bash`, depth cap 1, Risky
  shell treated as Blocked inside it. Runs as the coder's current model by
  default (optional per-call `model` arg); config gate `tools.enable_agent`.
- `read_file` refuses files over `CurationBudgetTokens` (16000) and
  redirects to `study`; large Go files return a declaration skeleton.
- `edit_file` is exact-match-first, whitespace-tolerant on retry; prefer it
  over `write_file` for edits.
- `bash` is gated by `internal/shellrisk`: Safe runs, Risky prompts (judged
  against `turnIntent`), Blocked refuses. Headless sessions treat Risky as
  Blocked.
- `remove_path` is workspace-confined (`.git`/`.cortex`/root refused);
  disabled by `tools.allow_delete: false`.
- `web_search` and `fetch_url` provide bounded, read-only public web access;
  `fetch_url` blocks local/private destinations and unsafe redirects. Both are
  coder-only and can be disabled with `tools.enable_web: false`.
- The context tools let the model curate its own working set on top of the
  mechanical demotion policy: evict or merge outline entries (merge installs
  one spanning `#m<first>-<last>` citation, so recall stays lossless) and
  shift the demotion watermarks (±W/4). `recall` takes an optional `budget`
  for a compact digest instead of the raw messages (this subsumed
  `context_summarize`). Curation persists across resume (per-turn session
  snapshot — decision 2026-07-18; the transcript remains the lossless
  record); per-tool gates `tools.enable_context_*`. See
  [`docs/context-window-modification-tools.md`](docs/context-window-modification-tools.md).

## Configuration

Layered, lowest→highest: `~/.cortex/config.json` (user) →
`./.cortex/config.json` (project, field-by-field override) → `CORTEX_BACKEND`
env. Loaded by `LoadConfig()` / `loadMergedConfig()` in `main.go`.

```json
{
  "backend": { "type": "openrouter", "endpoint": "...", "key_env": "OPENROUTER_API_KEY" },
  "models": {
    "code":  { "model": "...", "window": 131072 },
    "study": { "model": "..." }
  }
}
```

Roles: `code` (the agent) and `study` (the `Study` subagent + the summarizer +
the shell-risk classifier — all three build their sub-LLM call off the study
binding and pin reasoning effort off at the call site, docs/thinking-models.md).
`embed` stays parsed-but-reserved for a future semantic `memory_search`
(`CortexSession.resolveEmbedder` still resolves it). The configurable role
surface is exactly `code`/`study`/`embed` (`cmd/cortex/config.go`'s
`rolePolicies`) — a 2026-07-18 audit (`docs/completion-roadmap.md` E1) found
`hard-code`/`reason`/`fast`/`rerank`/`tools` genuinely dead and removed them;
an old config naming one of those still loads, sits inert, and prints one
stderr warning. The mechanical retrieve/rerank/Dream pipeline
(`pkg/cognition/dag`, `internal/cognition`) and the blind-sampling study
engine (`internal/study`) were deleted outright — see
[`docs/archive.md`](docs/archive.md).

**[`docs/configuration.md`](docs/configuration.md) is the authority** for
every `tools.*` gate, every env var, auth resolution (`key_env`/
`key_service`, no automatic provider-named fallback on this path), the
`backend.type` supported set, and the zero-config curated-fleet default —
kept in exactly one place so this section, the README, and the doc itself
can't drift apart.

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
go build ./cmd/cortex          # build the binary
go test ./...                # full suite
./scripts/check.sh           # fmt + vet + lint
```

## Key files

- `cmd/cortex/main.go` — REPL, `CortexSession` (composition root), `Turn`, dispatch, config
- `cmd/cortex/loop.go` — the `runLoop` engine + the `Sender`/`AgentDispatcher`/`Toolset`/`Bounds`/`Progress` seams + `requestFor`
- `cmd/cortex/study.go` — the `Study` subagent wiring (`RunSubagent`, `dispatcherFor`, `Outline`) + telemetry
- `cmd/cortex/summarize.go` — free-text summarizer (compaction + shell-output)
- `internal/agent/` — the shared tool-call vocabulary (`Tool`, `ToolCall`, `Bounds`); imports only the stdlib
- `internal/tools/` — the agent's tool surface (`Execute`, the tool decls, `grep`, the `Study` profile, `ConfinePath`/`TargetedRead`)
- `internal/outline/` — the structural map (`Outline`/`Render`; `go/ast` + regex tiers, breadth-first to budget)
- `internal/journal/` — append-only event log (incl. `study.result` telemetry)
- `internal/shellrisk/` — command risk classifier
- `pkg/llm/` — LLM providers
- `pkg/config/` — layered config
