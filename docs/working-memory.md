# Working Memory — Continuous Context Curation for Forever Sessions

> **Purpose.** Let a session run indefinitely on a fixed (even small)
> context window by treating the window as a *managed working set*, not
> an append-only log. A salience pass continuously scores what's in
> context and a triage step decides, per item, whether to keep it
> verbatim, compress it, or evict it to the journal — directing the
> compaction LLM instead of summarizing everything uniformly at a size
> threshold. Evicted items are recalled on demand via retrieval, so
> nothing is lost; the session only ever *feels* infinite.
>
> **Status.** Design. Phase 1 (boundary compaction) is a small wiring
> change on top of the existing `decide.route_message` node and
> `Compact`; Phases 2-3 build the working-set machinery.
>
> **Owner.** `cmd/loop/` (foreground turn loop + Think/Dream hosting) +
> `pkg/cognition/dag/ops/` (scoring + triage + compress nodes).
>
> **Builds on.** [`salience-budgets.md`](salience-budgets.md) (the
> compressor and its token/intent contract), [`journal.md`](journal.md)
> (the durable store eviction demotes to), and the Think/Dream
> background modes in `CLAUDE.md`.

---

## Why

`salience-budgets.md` already states the problem precisely (observation
#2): tool-calling APIs are stateless, so every request re-sends the
whole transcript, and an inner agent loop "eats 75k cumulative input
tokens by turn 8, paying linearly per turn for uncompressed prior tool
outputs." Long sessions don't fail because any single turn is large —
they fail because the transcript grows monotonically and the model's
window is fixed.

Today's mitigation is **size-triggered compaction**: at 80% of the
window, `Compact` distills the whole transcript into one digest and
restarts. Two weaknesses:

1. **The cut is arbitrary.** 80%-full usually lands *mid-task*, so the
   digest has to summarize work in progress — the worst input for a
   summarizer.
2. **It's uniform.** Everything is compressed equally, including
   high-value decisions that should have stayed verbatim and stale tool
   output that should have been dropped entirely.

We want context that is **continuously revised** — kept lean at all
times, trimmed at the right seams, with retention decided by salience
rather than recency or a blunt size trigger.

## The reframe: the window is a cache, the journal is memory

"Forever session" cannot mean an ever-growing window. It means:

| Layer | Role | Already exists |
|---|---|---|
| **Working set** | The budget-bounded, salience-ranked subset the model sees this turn | the live `Request.Messages` |
| **Journal** | Immutable record of every turn / tool result / decision | `.cortex/journal/` |
| **Retrieval** | Recall an evicted item when a later turn needs it | `remember.vector_search` |

The key move: **eviction is demotion, not deletion.** Dropping an item
from the working set writes nothing away — the item is already in the
journal (and captured for vector search). If a later turn references it,
retrieval pulls it back into the working set. The session feels infinite
because the window is just the hot cache; cold history is one
`vector_search` away.

This is Cortex's own `capture → retrieve → inject` pipeline pointed at
the **session's own history** rather than at project knowledge. The
mechanism is the thesis; this applies it to a new corpus.

## Architecture

### Item-level context model

Today `Request.Messages` is a flat slice the harness only ever appends
to or wholesale-replaces. Working memory needs items to be
*individually* scorable and evictable, so each carries metadata:

- **kind** — system / user / assistant-prose / tool-call-pair / digest.
- **turn** — when it entered (for recency + pinning the last N).
- **pinned** — never evict (system seed, the active task's anchor turns).
- **atomic group** — an `assistant(tool_calls)` and its `tool(result)`
  messages are one unit; the curator moves/evicts them together or not
  at all (splitting them breaks the wire format — the same hazard the
  current compactor guards against).

### The curation loop

```
            ┌─────────────── score ───────────────┐
 working    │ value.score / attend.rerank          │
   set ────▶│ "how relevant is each item to the    │
            │  current focus?"                      │
            └──────────────────┬───────────────────┘
                               ▼
            ┌──────────── triage (NEW) ────────────┐
            │ given a token budget, bucket each     │
            │ item: keep verbatim | compress | evict│
            └───────┬───────────────┬───────────────┘
            keep    │      compress │        evict
                    ▼               ▼               ▼
              (unchanged)   attend.compress    demote → journal
                            (budget+intent)    (recall via
                                               vector_search later)
```

- **Score** — rank items by relevance to the *current focus*. Focus is
  the recent turns plus the active task description (the same `goal` the
  `decide.route_message` classifier already tracks).
- **Triage** — the "classifier that decides what to keep and directs the
  compaction LLM." A `decide`-shaped policy node that, under a token
  budget, assigns each item to keep / compress / evict. High-salience
  detail stays verbatim; mid-salience gets compressed; low-salience,
  superseded, or stale items are evicted.
- **Compress** — only the compress bucket goes through
  `attend.compress`, which already takes a token budget and a salience
  intent string (the "direct the compaction LLM" contract from
  `salience-budgets.md`).
- **Evict** — drop from the working set; it remains in the journal and
  is recalled by `remember.vector_search` if a later turn needs it.

### What's reuse vs new

| Piece | Node / mechanism | Status |
|---|---|---|
| Per-item relevance scoring | `value.score`, `attend.rerank` | exists |
| Budgeted, intent-directed compression | `attend.compress` | exists |
| Recall of evicted items | `remember.vector_search` | exists |
| Durable store eviction demotes to | journal | exists |
| Task-boundary signal | `decide.route_message` | exists (this branch) |
| Background execution | Think (active) / Dream (idle) | exists |
| **Item-level working-set model** (metadata, pinning, atomic groups) | — | **new** |
| **Keep/compress/evict triage policy** | a new `decide` node | **new** |
| **The continuous loop driver + hysteresis** | `cmd/loop` Think hook | **new** |

### Where it runs

The curator is background work by nature, and Cortex already has the
budget model for it: **Think** runs during active periods on spare
cycles (its budget *decays* with activity), **Dream** runs during idle
on a budget that *grows*. So:

- **Think** does cheap incremental curation between turns — score the
  newest items, evict obvious deadweight, keep the working set from
  drifting up.
- **Dream** does deeper reorganization while idle — re-score the whole
  set, merge redundant digests, pre-warm likely recalls.

Running in the background keeps curation off the foreground turn's
latency path; the model call uses the cheap `study`-tier provider.

## Safety & invariants

An autonomous context-rewriter is only acceptable if it can't silently
lose work or corrupt the wire format. The invariants:

1. **Never split an atomic tool-call group.** Evict/compress the
   `assistant(tool_calls)`+`tool(result)` unit whole, or not at all.
2. **Pin the last N turns and the system seed.** Recent context and the
   anchor of the active task are never evicted, so the model never loses
   the thread it's actively using.
3. **Eviction is recoverable.** Every evicted item is in the journal and
   vector-searchable; a turn that references it triggers recall. This is
   what lets the triage be aggressive without being lossy.
4. **Hysteresis.** Do not re-curate every turn. The provider caches the
   prompt *prefix* (OpenRouter/Anthropic); reshuffling context each turn
   blows the cache and *raises* cost. Re-curate only on budget pressure
   or a task boundary, and prefer appending a digest over rewriting the
   prefix when possible. Cost lever, not just coherence.
5. **Conservative triage.** Bias toward keep; require high confidence to
   evict. A wrong eviction mid-iteration is the failure mode to avoid —
   recall mitigates it but a round-trip isn't free.
6. **Every decision is journaled.** Keep/compress/evict choices write to
   the journal so a session is replayable and the curator is auditable.
   This is the bounded-emergence + structured-eval discipline applied to
   context management: the curator runs under a budget and leaves a
   trace.

## Build phases

Each phase ships and is measurable on its own; each de-risks the next.

- **Phase 0 — done.** Size-triggered `Compact` at 80%.
- **Phase 1 — boundary compaction.** Use `decide.route_message` to pick
  the *seam*: when a turn starts a new task, run the existing `Compact`
  at that boundary (cleaner digest than a mid-task 80% cut). Tiny wiring
  change; no working-set model yet. Opt-in behind a `boundary_compaction`
  flag.
- **Phase 2 — selective compaction.** At compaction time, `value.score`
  the items and apply keep/compress/evict instead of summarizing
  uniformly. This is the triage core, still triggered episodically
  (boundary or size). Introduces the item-level model and the triage
  node.
- **Phase 3 — continuous + background.** Run the triage in Think/Dream
  with retrieval-recall, pinning, and hysteresis. This is the "forever
  session, behind the scenes": the working set is curated continuously
  and the window never grows unbounded.

Going straight to Phase 3 collides the coherence, cost, and trust risks
all at once. Phase 2 alone already stretches sessions substantially.

## Evals

Per the eval discipline (`eval-strategy.md`), each phase needs a metric:

- **Session length on a fixed small window.** Turns sustainable on a
  32k/64k model before quality degrades — the headline "forever" claim.
  Target: slope, not an absolute.
- **Retention quality.** After curation, can the model still answer
  questions whose answers were in evicted/compressed turns? Measures
  whether the triage kept/recalled the right things.
- **Recall-after-evict.** When an evicted item is needed, does
  `vector_search` bring it back? False-evict recovery rate.
- **Cost & cache.** Tokens/turn and prompt-cache hit rate vs the
  size-triggered baseline — confirms hysteresis is doing its job and the
  curator isn't a net cost regression.

## Open questions

- **Triage granularity** — per message, per turn, or per semantic span?
  Atomic tool-groups force at least turn-ish granularity.
- **Focus definition** — is "current focus" just the last N turns, the
  `route_message` goal, or a learned topic vector?
- **Digest accumulation** — repeated compaction layers digest-on-digest;
  does that degrade? May need periodic full re-distillation (a Dream
  job).
- **Interaction with `decide.route_message`'s reset path** — in Discord a
  new task resets the session; in the REPL it should curate, not reset.
  Same signal, host-specific action. Keep that split explicit.
