# Handoff — 2026-06-26 — memory as tools (P1 in progress)

Continue here in a fresh session. Branch `loop/wire-cognition-fleet`, **10
commits, NOT pushed**, gate + full suite green at the last checkpoint.

## TL;DR

The session started by *wiring up* a mechanical memory/retrieval pipeline, kept
hitting its failure modes (stale memory served as fact; memory ≈ transcript),
and ended with a **pivot**: memory should be **tools the model drives**, not a
mechanical retrieval/distill pipeline. The live design is
[`docs/memory-tools.md`](memory-tools.md). **P1 is underway** — the note store
(`internal/memory`) is built and committed; tools + index injection are next.

## The live direction (read this first)

[`docs/memory-tools.md`](memory-tools.md): memory = a small tool surface over
free-form named notes.
- Tools: `memory_write(name, content)`, `memory_read(name)`,
  `memory_search(query)`, `memory_forget(name)`, plus `study(journal)` (reuse the
  navigator on the raw journal for depth).
- Storage: `.cortex/memory/<name>.md` (frontmatter `created`/`updated`) +
  `INDEX.md`. The model picks names; update-or-create; no duplicates.
- The **one mechanical seam**: inject `INDEX.md` at turn start so the model knows
  what it can recall. Everything else is the model's call.
- System-prompt guidance (principles, not recipes): you have notes; read the
  relevant ones; write a durable note when you learn something; notes are
  timestamped — verify if stale; `study(journal)` for raw detail.

Supersedes the mechanical memory line (`docs/memory-distillation.md`,
`docs/working-memory*.md`) — kept for history.

## Decisions locked

- **(a) memory_search = keyword for P1** (corpus is the model's own notes + the
  index is injected anyway; reversible behind the tool — swap to embeddings later
  only if P4 evals show recall misses).
- **(b) fold `/remember` `/forget` into the model tools** — user asks in natural
  language ("remember X" / "forget the 936 thing") → model calls the tool. Remove
  the slash commands.
- **The bet:** "just tools" assumes the local model self-manages memory; the
  index injection is the hedge. Add a stronger nudge only if evals prove the need.

## P1 status

DONE (commit `287d949`):
- `internal/memory/store.go` + tests — `Write/Read/List/Search/Forget/Index`,
  safe name normalization (traversal-proof), frontmatter timestamps, `INDEX.md`
  regeneration.

NEXT (P1 remaining):
1. **Memory tools** (task: "P1: memory tools"). Add `memory_write/read/search/
   forget` to `cmd/loop/tools/tools.go`: declarations (`newTool`, see `StudyTool`
   ~`:175`), add to `All` (~`:212`), dispatch in `ToolCall.Execute` (~`:239`).
   Wire to the store via the `ToolDeps` interface (~`:31`) — add methods like
   `MemoryWrite/Read/Search/Forget`, implement on `CortexSession`, no-op on
   `headlessDeps`. Construct the store in `EnableRetrieval` (or session init).
2. **Index injection + prompt** (task: "P1: index injection"). Inject
   `store.Index()` into the turn context (size-capped) — likely alongside or in
   place of the current `EphemeralSystem` retrieval injection. Add the
   memory-principles block to the seed system prompt (`systemMessage`, ~`:977`).
3. **Remove `/remember` `/forget`** slash commands + their handlers
   (`cs.remember`, `cs.forget`, `forgetTerms`, `termOverlap`, dispatch ~`:3640`).

Then P2 (`study(journal)` — point the navigator at `.cortex/journal`/`sessions`),
P3 (rip out the mechanical pipeline — see below), P4 (tool-native no-mock evals).

## P3 — the mechanical machinery to REMOVE (much built earlier this session)

Once tools work, delete from the hot path (keep journal + embedder + navigator):
- `cmd/loop/main.go`: `retrieve()`, `formatRetrieved`/`formatRetrievedAt`/
  `relAge` (freshness injection), the `EphemeralSystem` retrieval wiring in
  `Turn`, `noteTurn`/`distillPending` (auto-distill), `recordRetrieval`.
- `internal/cognition`: Reflex/Reflect/Resolve usage; `weightByProvenance`
  (recency ranking), `applyRetractions` (contradiction→retraction). The package
  may shrink to just what the DAG still needs.
- `EnableRetrieval` rebinds to: build the memory `Store` + embedder (for
  `memory_search` if/when semantic) + capturer (journal record only). Drop the
  `intcog.New` cognition wiring.

These were real, tested commits (`65d2e0d`, `187a482`, `102e171`, `ffc65b6`) —
they work, they're just superseded by the pivot. Don't be surprised to be
deleting recent green code.

## Key findings / gotchas

- **Doc-count can't demonstrate staleness.** The live eval
  (`cmd/loop/memory_freshness_live_test.go`, UNTRACKED) proved the model
  *re-derives* file counts via `project_index`/`find` and is never misled by a
  stale "936" memory. Staleness only bites for **non-re-derivable** facts
  (decisions, preferences, history). Re-target P4 evals accordingly. (That file
  is WIP — keep or delete; it's green for the wrong reason.)
- **`936 docs` confabulation** was glm-4.7-flash at temp 0 inventing a fake
  codebase summary on `hi`; the (now-superseded) capture→retrieve loop then
  re-served it. Tools + the model reading timestamps should dissolve this.
- **Embedder/navigator/journal are the durable substrate** — they survive the
  pivot. Embedder: fleet `embedder` (1024-d) or local Hugot (int8, pure-Go);
  rerank/dream were routed to `fast`=qwen3-4b because the fleet `reranker` is a
  cross-encoder (no chat) and `reasoner-npu` returns only reasoning_content. See
  [[project_cognition_wired_into_loop]] (auto-memory).

## Entry points

- `internal/memory/store.go` — the note store (done).
- `cmd/loop/tools/tools.go` — tool registry (`All`, `Execute`, `ToolDeps`).
- `cmd/loop/main.go` — `EnableRetrieval` (~`:2108`), `Turn` (~`:3175`),
  `systemMessage` (~`:977`), slash dispatch (~`:3640`).
- `docs/memory-tools.md` — the design.

## Constraints

- Local-only is a **goal, not a constraint** (Derek, this session) — cloud
  embedder allowed.
- Never push / PR without explicit consent. Branch has 10 unpushed commits.
- Keep `go build ./...`, `go test ./...`, `./scripts/check.sh all` green.
- Standard-lib testing only; wrap errors with `%w`; principles-not-recipes in
  runtime prompts.
