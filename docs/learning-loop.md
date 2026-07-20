# The learning loop — background learning's smallest honest slice

> **Status: BUILT** (2026-07-19). Answers `docs/think-dream-eval.md`'s
> "Blocked — no product surface named" row by naming the consumer up front
> (below) and then building the smallest thing that could possibly clear
> that doc's gate. The gate itself is unchanged and is **not** duplicated
> here — see [`docs/think-dream-eval.md`](think-dream-eval.md) for the
> full Δ/ø design, the Go/no-go table, and the historical Think/Dream
> arc this supersedes in simplified form.

## The decision

Derek decided (2026-07-19): revive background learning, but only its
smallest honest slice — **the learning loop**, not a resurrection of the
old five-mode Think/Dream/Reflex/Reflect/Resolve machinery. Background,
automatic, bounded, receipted:

- **background** — runs off the coder's own turn loop, never on it.
- **automatic** — no user prompt required; a loop firing or a cron-shaped
  habit (`cortex learn`) triggers it.
- **bounded** — Study-like `Bounds` (iteration cap, output-token cap,
  read-budget cap) enforced by the same `runLoop` engine every other
  subagent runs on. No unbounded scan, no unbounded write.
- **receipted** — every pass either writes real notes through the real
  `internal/memory` store, or reports "nothing worth saving" — never a
  silent no-op and never an invented one.

`docs/think-dream-eval.md`'s bar **stands unchanged**: background must
beat foreground on NEEDLE-A at n≥3 with the precision floor (G3) and
bounded cost (G4) holding, and NEEDLE-B parity (G1) proving the scenario
itself is valid. Positioning upgrades (a default-on loop, a shorter cadence,
folding in cross-session connection) happen **only on green** — this doc
ships the mechanism and its gate test; running the gate and reading the
verdict is still explicitly owner review, exactly as the design doc states.

## The consumer (named up front)

The historical arc's lesson (`think-dream-eval.md` §"Prior art"): 18k lines
of retrieve/rerank/Think/Dream machinery shipped once with no product
surface asking for it, and was deleted nine days after being wired live.
This design does not repeat that mistake — **the consumer already exists
and needs zero new wiring**: the turn-start memory index injection
(`memoryIndexNote`, `docs/memory-tools.md`). Every note the learning loop
writes is a note in `.cortex/memory/`, indexed by the same `INDEX.md` the
next foreground session already reads at turn start. The loop's whole job
is depositing input for a surface that already exists.

## Design

### The `Learn` subagent profile

`internal/tools.Learn`, alongside `Study` and `Agent` (`internal/tools/tools.go`,
`docs/study-subagent.md` §1's `Subagent` shape): same engine (`runLoop`),
same door-guard dispatch, different toolset and a different trigger.

- **Tools**: `outline`, `grep`, `read_file`, `memory_read`, `memory_search`,
  `memory_write` — read-only on the filesystem **except** memory writes. No
  `bash`/`write_file`/`edit_file` (an analyst, not an implementer); no
  `memory_forget` (retracting a note stays the coder's call); no
  `recall`/`context_*`/`study`/`agent` (session-scoped coder-only tools,
  same exclusion `Study`/`Agent` already apply).
- **Bounds**: Study-like — `MaxIter` 12, `MaxTokens` 8192,
  `ReadBudgetBytes` 96000 — overridable via `subagents.learn` (the pattern
  `subagents.study`/`subagents.agent` established).
- **Model**: the study role's binding by default, exactly like `Study` — a
  background pass has no "coder's current model" to inherit (it can run
  with no coder session live at all).
- **Not coder-callable.** Unlike `Study`/`Agent`, `Learn` is deliberately
  **not** `Register()`'d and has no tool `Declaration` — never offered to
  the coder mid-conversation. Its only entry points are `cortex learn` and
  the loop scheduler's `kind:"learn"` firing, both calling `RunSubagent`
  directly.
- **The prompt** is shaped after the deleted `internal/cognition.DreamAnalysisPrompt`
  (salvaged for its category taxonomy, not its code — `git show
  5af5644:internal/cognition/dream.go`): **decisions** (a choice and its
  why), **patterns** (a reusable approach), **constraints** (something to
  avoid), **corrections** (a mistake not to repeat) — plus an explicit
  **NO_INSIGHT** escape. `memory-tools.md`'s "saving is rare" discipline
  carries over verbatim: finishing a pass with zero writes is the common,
  valid, expected outcome, not a failure.

### Seed: the journal window, not the whole history

Each pass seeds Learn with three things: the **memory index** (so it never
duplicates an existing note), a **capped digest** of `capture.event` journal
entries (`.cortex/journal/capture/`, one per foreground turn) since the last
learn cursor, and the extraction goal. The digest is hard-capped in bytes
regardless of backlog size — a first-ever pass over a long-lived project
must not hand the model an unbounded seed.

### The cursor: incremental, never re-mined

A small `journal.Cursor` (the same `<dir>/.cursor` machinery
`internal/journal`'s dormant `Indexer` already established) tracks the last
scanned journal offset, at its own directory
(`.cortex/journal/learn/.cursor`) rather than inside the `capture` class it
reads — Learn is one consumer of that class, not its indexer. The cursor
**only advances on a successful pass** (including a `NO_INSIGHT` one) — a
subagent-level error (unreachable backend, etc.) leaves it exactly where it
was, so that window is retried next time instead of silently dropped. Zero
new entries since the cursor is the cheapest possible outcome: `RunLearningPass`
returns immediately with no model call at all, never a wasted bounded one.

### The trigger surface — two entry points, one engine

Both call the same `RunLearningPass(ctx, cs, focus)` (`cmd/cortex/learn.go`):

1. **`cortex learn [--project <name>]`** — a one-shot headless run over the
   current (or named) project's journal since the last cursor. Always exits
   0; prints a short plain report ("N entries scanned, M notes written", or
   "nothing worth saving").
2. **The loop scheduler** — `internal/loops.Spec` gains a `Kind` field
   (`""`/`"turn"` = today's coder-turn loop, `"learn"` = this pass).
   `RunLoopFiring` (`cmd/cortex/loop_run.go`) branches to `runLearnFiring`
   for a `kind:"learn"` spec: same cadence floor, same three-strike
   auto-disable, same one-`loop.run`-event-per-firing contract as every
   other loop — just a different firing engine. No `cortex change` branch
   (Learn writes to `.cortex/memory`, which is git-ignored — there is
   nothing to land as a reviewable change) and no NEXT/DONE self-pacing
   marker (Learn's result is structured, not a free-text reply to parse).
   The scheduler and the served loops list render a learn loop like any
   other — `Kind` rides along on the existing `loops.Spec` JSON, so no
   web UI plumbing was needed beyond the field itself.

## Out of scope (this doc)

- **Cross-source / cross-project connection.** Learn reads one project's
  own journal. Connecting facts across sessions, across projects, or
  against external sources is a materially different capability (closer to
  the old Dream's multi-source sampling) — **slice 2**, its own doc, only
  if this slice's gate goes green and a consumer is named for it too.
- **No mechanical retrieval revival.** Learn does not re-introduce
  recency weighting, contradiction→retraction, or freshness injection —
  the model decides what's worth saving, exactly as `memory-tools.md`
  already bet. Learn only decides *what it gets to look at* (the journal
  window), never what's true or stale within it.
- **No DAG.** One bounded subagent call per pass, on the existing `runLoop`
  engine. No node graph, no spawn/decay budget, no new scheduling
  primitive beyond the loop spec `Kind` already in place.

## Tests

- **Δ (deterministic)**: `cmd/cortex/learning_loop_eval_test.go` — scripted-
  sender tests over the real memory store and real journal/cursor machinery:
  a canned `memory_write` lands as a real note and the index regenerates; the
  cursor advances and a re-run scans nothing (and makes no model call); a
  scripted call to `bash`/`write_file` is refused; the `NO_INSIGHT` path
  writes nothing and still advances the cursor; a subagent-level error
  leaves the cursor unadvanced; `MaxIter` bounds a model that never stops
  calling tools. Plus `internal/tools/learn_test.go` (the profile's shape —
  toolset, bounds, not-coder-callable) and targeted additions to
  `cmd/cortex/config_full_configurability_test.go` (the `subagents.learn`
  config section and the study-binding model-inherit special case reach the
  real profile, independently of `subagents.study`).
- **ø (agentic, live-gated)**: `cmd/cortex/learning_loop_live_test.go`
  (`CORTEX_LIVE_FLEET=1`) — the two-arm NEEDLE-A/NEEDLE-B/NOISE scenario
  above, G1–G4 as this doc's decision states, reported via a structured
  `t.Logf` scoreboard.

## Gate receipts (2026-07-19)

Run 1 (pre-fix): NO-GO — G2 0/2 background vs 0/3 foreground. Diagnosis
found two real causes, both harness-side: the seed digest truncated
prompts at 200 chars (the needle sat past the cut — the model never saw
it; fixed by transcript pull-through on truncation, Δ-tested) and the
Learn prompt carried foreground's save-reluctance (fixed, iteration 3;
live positive + negative repro both clean). G1/G3/G4 passed even here.

Run 2 (fixed harness, n=3, qwen3-coder-q3, chatterbox fleet, 251s):
**ALL FOUR GATES PASS.** G2: background 2/3 needle-A vs foreground 0/3
(strict beat). G1: needle-B parity 6/6. G3: 4 notes total vs limit 6 —
no spam. G4: 27.7s total learn wall-clock vs 180s budget. One gate run,
per docs/think-dream-eval.md's n>=3 bar; reproduction runs are cheap
and encouraged before external claims.

Per the 2026-07-19 decisions: this green is what upgrades the project's
positioning to continual-learning language — the term re-earned with a
receipt, not pre-claimed.
