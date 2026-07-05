# Context Architecture — a cache-friendly working set for `loop`

> **Purpose.** Define how `cmd/loop` assembles the wire context so a session
> can run indefinitely on a fixed window with **bounded per-turn prefill and
> maximal prompt-cache reuse** — no 80% compaction cliff, no lossy whole-history
> digest, no model call on the hot path.
>
> **Inputs.** The sketch in `internal/cache/cache.go`, the working-memory line
> ([`working-memory.md`](working-memory.md) — window = cache, journal = memory,
> eviction = demotion, hysteresis) and its proven instance
> ([`working-memory-study.md`](working-memory-study.md) P4 — an append-stable
> prefix + volatile tail gave 0 prefix breaks live), and
> [`memory-tools.md`](memory-tools.md) (durable memory is model-driven tools;
> this doc is only about the *session window*, not durable memory).
>
> **Status.** **IMPLEMENTED (P1–P4, 2026-07-03)** — built via the loop harness
> itself (Qwen3-Coder-Next doing the edits from piecewise specs). All phases
> live: index off the fold, `internal/cache` working set + two-zone wire
> assembly, transcript turn-stamping + resume replay, `recall(citation)`,
> outline fold. Deltas from this design, discovered in implementation:
> - **Citations are message-index ranges** (`@session/<id>#m<start>-<end>`,
>   resolved through `loadTranscript`), not file line ranges — line numbers
>   drift with non-message entries; message indexes are what resume replays.
> - **`Compact` is kept as an unreachable safety net**, not deleted: with the
>   tail bounded at W/2 and the outline at W/8, the 80% trigger can't fire in
>   normal operation, but it still guards pathological single-turn blowups.
> - **Turn spans are stamped** (`turn` field on transcript message entries)
>   rather than re-derived: salvage prompts make derivation ambiguous. Legacy
>   unstamped transcripts resume with history hydrated (demotes nothing).
> - **Live retention results (qwen3-coder-q3):** recall works when the outline
>   signals relevance (graded probe passes: truncated codeword recovered
>   unprompted after adding a grounding principle — "the outline is an index,
>   not the content; never guess at content it only points to" — which also
>   stopped an observed confabulation). Blind recall of facts with zero
>   visible trace remains beyond this model's initiative: it answers honestly
>   that it doesn't know. That's the accepted floor, per the design's bet.
> - The recall parser tolerates bracketed citations (`[@session/…]`) — models
>   paste them as rendered.
>
> Deltas from the 2026-07-05 self-review (all covered by the context eval,
> `cmd/loop/context_eval_test.go`):
> - **`PrefixEnd` — the pre-turn prefix region.** `wireMessages` originally
>   omitted `Messages[1:TailFrom]` wholesale, silently dropping content that
>   was *below the working set's base* and therefore never outlined: the
>   Compact summary, and the whole history of a legacy/invalid-stamp resume
>   (which sent only the system message + new input). The wire now carries
>   `Messages[1:PrefixEnd]` (= `WorkingSet.Base()`) unconditionally as part of
>   the append-stable prefix; only `[PrefixEnd, TailFrom)` — the region the
>   outline actually stands in for — is omitted.
> - **Replay accepts stamp sequences starting at any K.** Compact continues
>   the session's turn numbering, so a post-Compact transcript's first stamp
>   is not 1; requiring 1 sent every such transcript to the hydrated fallback.
>   `Clear` now also resets numbering (a cleared session is a fresh
>   conversation).
> - **Outline labels are demotion ordinals, not `len(outline)+1`.** Folds
>   shrink the outline slice, so length-based numbering regressed and produced
>   duplicate `t<n>` labels. Labels now come from the working set's monotonic
>   demoted count and match the transcript stamps on the replay path.
> - **The fold's citation invariant is enforced mechanically.** The fold goal
>   asks the summarizer to keep citations; `restoreCitations` now guarantees
>   it — any coordinate missing from the digest is appended verbatim, so
>   demotion stays lossless regardless of summarizer quality.
> - **`recall` is gated at `min(CurationBudgetTokens, W/3)`** — a recall
>   bigger than the tail's drain target would flood the hydrated tail and
>   immediately re-demote.
>
> **Owner.** `cmd/loop` (turn assembly) + `internal/cache` (the working-set
> model — the sketch file becomes the package).

---

## Why: the cache economics of today's layout

The provider prompt cache (llama.cpp LCP automatically; Anthropic via
`cache_control`; OpenRouter provider-dependent) reuses the **longest common
prefix** of consecutive requests. Anything that changes a byte mid-history
re-prefills everything after it. Audit of today's `cmd/loop`:

| Behavior | Cache effect |
|---|---|
| History is append-only (`cs.Append`); mid-loop iterations grow the tail | ✅ within-turn requests reuse the whole prior prefix |
| `EphemeralSystem` (memory index) folds onto the **last user message** (`wireMessages`) | ❌ breaks the prefix at the *previous* turn's user message **every turn** — turn N sent `userN+note`, turn N+1 sends `userN` bare. The whole previous turn re-prefills, and the index tokens are never cached. |
| `Compact` at 80%: whole transcript → one model-written digest, `Messages` wholesale-replaced | ❌ total cache miss, lossy (uniform summary, usually mid-task — the two weaknesses `working-memory.md` names), and a model call on the hot path |
| Between compactions the transcript grows monotonically | ❌ per-turn prefill cost grows linearly — on local silicon prefill time *is* the latency, so turns get slower until the cliff |

So today the session oscillates: linearly degrading turns → cliff → repeat,
with a permanent per-turn break from the index fold.

## Principles (from the `cache.go` sketch, made precise)

1. **User messages are immutable.** The user's words are the ground truth of
   intent; they are never summarized, paraphrased, or dropped from context.
2. **Never restructure context.** A byte that has entered the stable region is
   never edited in place. The only mutations are *append* (new turns, new
   outline lines) and *demotion at the frontier* (a whole turn leaves the
   hydrated tail and its outline form is appended to the prefix zone). This is
   the cache-friendliness invariant stated as a data-structure law.
3. **Deterministic citation in place of raw output.** Demotion is mechanical —
   no LLM. A tool result older than the tail is represented by a one-line
   citation naming its transcript coordinate, and can be re-fetched exactly.
   The raw output of the **last** tool batch is always present verbatim.
4. **The transcript is canonical; the window is derived.** The session JSONL
   keeps every message verbatim (it already does — `writeTranscript` records
   what `Append` sees). The working set is a pure function of (transcript,
   policy), so resume reconstructs it deterministically. Same CQRS invariant
   as [`journal.md`](journal.md).

## The two-zone wire layout

```
                  ONE REQUEST (window W)
┌───────────────────────────────────────────────────────────────┐
│ ZONE A — PREFIX (append-stable → prompt-cache HIT)            │
│ ┌───────────────────────────────────────────────────────────┐ │
│ │ [system]  SystemPrompt + AGENTS.md            fixed       │ │
│ ├───────────────────────────────────────────────────────────┤ │
│ │ [user]    SESSION OUTLINE                     append-only │ │
│ │   t1 · user:"fix the eval" · edit x.go [ok]       ≤ W/8   │ │
│ │        ⤷ "pinned temp"  [@session/…#t1]                   │ │
│ │   t2 · …                                                  │ │
│ │   t3 · …            ◀── grows ONLY at its own tail        │ │
│ ├───────────────────────────────────────────────────────────┤ │
│ │ [user]    MEMORY INDEX          changes on memory_write   │ │
│ └───────────────────────────────────────────────────────────┘ │
├───────────────────────────────────────────────────────────────┤
│ ZONE B — HYDRATED TAIL (volatile, low-wm ≈ W/3 … high ≈ W/2)  │
│   turn k-2 : user / assistant(tool_calls) / tool …  verbatim  │
│   turn k-1 : …                                      verbatim  │
│   turn k   : current turn, appends as runLoop iterates        │
├───────────────────────────────────────────────────────────────┤
│ OUTPUT RESERVE (MaxTokens)                                    │
└───────────────────────────────────────────────────────────────┘
```

- **Zone A only ever grows at its own tail.** Steady state, the LCP covers the
  system prompt, the entire outline so far, and the index — the bulk of a long
  session.
- **Zone B is the working set** in `working-memory.md`'s sense: the last n
  turns the model actually needs verbatim, bounded by a token budget. It
  re-prefills only when zone A grew (a demotion batch or a memory write) —
  otherwise turns are pure append and hit the full prefix.
- The memory index moves **out of the per-turn fold** into a fixed-position
  prefix message. Today's fold breaks the cache every turn; a fixed slot
  breaks it only when a note is written (rare). `EphemeralSystem` stays for
  genuinely per-turn ephemera, but the index no longer rides it.

### The lifecycle at a glance

```
              append              demote (mechanical, batched)       fold (LLM, rare)
 new turn ──────────▶ HYDRATED ──────────▶ OUTLINE ENTRY ──────────▶ coarse digest ¶
                        TAIL       tail>high-wm:   (citation kept)    (citations kept)
                         │         oldest whole
                         │ every   turns, drain
                         ▼ message to low-wm
              .cortex/sessions/<id>.jsonl        ◀── canonical, verbatim, always
                         ▲
                         └── recall(citation) ──▶ raw detail re-enters as a NEW
                                                  tool result at the tail (never
                                                  rehydrated in place)
```

Content only ever moves *rightward* (raw → outline → digest), each step keeps
the citation, and the transcript underneath never loses anything — so every
demotion is reversible at recall time.

### The outline entry (deterministic, no LLM)

One entry per demoted turn, rendered mechanically — the same shape
`captureTurn` already distills for the journal (reuse that renderer):

```
t12 · user: "make the study eval deterministic"          ← verbatim (principle 1)
      edit cmd/loop/study_eval.go · bash go test ./cmd/loop [ok]
      ⤷ "pinned temperature; reps now stable"            ← reply head
      [@session/20260701-143210#t12]                     ← the citation
```

- The **user message is kept verbatim** (truncated only for huge pastes, with
  a citation to the rest).
- Tool calls compress to `name target [ok|err]` — the raw spill (the ~90% of
  transcript volume) is what demotion actually removes.
- The assistant reply keeps its first line (verbatim if short).
- Harness notes (nudges, stuck hints, finalize prompts) are dropped from the
  outline entirely — they were traffic control, not content.
- The **citation** is a deterministic coordinate into
  `.cortex/sessions/<id>.jsonl`.

### Recall

A demoted detail the model needs again comes back as a **fresh tool result at
the tail** — never by rehydrating in place (principle 2). Two paths, both
existing machinery plus one trivial tool:

- `recall(citation)` — new, deterministic, no model: resolve the coordinate,
  return the cited message(s) raw, subject to the same size gate as
  `read_file` (oversized → redirect to `study`).
- `study(.cortex/sessions/<id>.jsonl, goal)` — already works for "what did we
  do about X three hours ago" when the model doesn't hold a citation.

The system prompt gets one principle line: *"Older turns appear as an outline
with citations; `recall(citation)` fetches the raw detail."*

## Budgets and hysteresis

The window `W` is partitioned once, at session init:

```
envelope   = measured(system + AGENTS.md)      fixed
outline    ≤ W/8                               grows by append, folded under pressure
index      ≤ memoryIndexCap (existing 4k chars)
tail       high-watermark ≈ W/2, low-watermark ≈ W/3
output     MaxTokens (existing reserve)
```

- **Demotion fires on the high-watermark and drains to the low-watermark** —
  a *batch* of oldest whole turns at once, so most turns touch nothing and the
  re-prefill cost (≤ tail budget) is paid every k turns, not every turn. This
  is the hysteresis invariant from `working-memory.md` §4, and the same
  pressure-triggered-never-scheduled rule the study P2 curation decision
  settled.
- **Demotion is turn-grained.** A turn = user → … → final assistant. Whole
  turns move, so an `assistant(tool_calls)`+`tool(result)` group is never
  split (the atomicity invariant), and pairing only ever exists inside the
  hydrated tail.
- **The outline itself is bounded.** When it crosses its cap, the *oldest*
  outline lines fold into one coarser paragraph via the existing `Summarize`
  chunk-and-fold — the only LLM call left in context management, rare, and a
  single planned prefix rewrite (one cache miss). Digest-on-digest decay is
  blunted because outline lines are already citation-grounded: the fold keeps
  the citations, so nothing becomes unreachable.
- Steady-state prompt size is bounded by
  `envelope + outline cap + index + tail high-watermark + output` — flat
  forever. `/compact` is repurposed to force a demotion+fold manually.

## Turn-time flow (hot path is mechanical)

```
1. tail tokens > high-watermark?
     → demote oldest whole turns to outline lines until ≤ low-watermark
       (append outline; drop their messages from the wire set; transcript untouched;
        write a `demote` event so the decision is journaled/auditable)
2. outline > cap?  → schedule a fold (may run via Summarize before send, or deferred)
3. send; runLoop appends as today — within-turn requests are pure prefix extension
```

No scoring model, no triage LLM on the hot path. `working-memory.md`'s
keep/compress/evict triage collapses to a **deterministic policy**: *keep* =
inside the tail watermark, *compress* = the mechanical outline entry, *evict* =
never (everything stays reachable via citation). The salience judgment the
triage node was going to make is deferred to the model itself at recall time —
the same pivot `memory-tools.md` made for durable memory, applied to the
window.

## What changes where

| Piece | Home | Status |
|---|---|---|
| Working-set model: item log, frontier, outline renderer, `Hydrate(budget)`, watermark policy | `internal/cache` (the sketch file becomes the package; stdlib-only, like `internal/agent`) | **new** |
| Turn assembly calls demote-then-send; `wireMessages` emits the two zones | `cmd/loop` (`Turn`, `transport.go`) | **change** |
| Memory index → fixed prefix slot | `cmd/loop/turn.go` / `transport.go` | **change** (small, cache-positive on its own) |
| Outline entry renderer | reuse `captureTurn`'s distillation | **reuse** |
| `recall(citation)` | `internal/tools` + a `ToolDeps` method | **new (trivial)** |
| Outline fold | existing `Summarize` | **reuse** |
| `Compact` | retired after P2 (kept as `/compact` = force demote+fold) | **retire** |
| Anthropic `cache_control` | breakpoint at end of zone A (today: system + last-user) | **change** |
| Transcript / resume | unchanged log; resume replays through the same policy | **reuse** |

## Cache cost per event

Where the prefix breaks and what it costs, in the two-zone layout:

```
 event                     │ break point          │ re-prefill cost
───────────────────────────┼───────────────────────┼─────────────────────
 ordinary turn             │ none (pure append)    │ new input only
 mid-loop tool iteration   │ none (pure append)    │ new messages only
 demotion (every ~k turns) │ outline frontier      │ ≤ tail budget (~W/2)
 memory_write              │ index slot            │ tail
 outline fold (very rare)  │ inside outline        │ outline tail + tail
 full-history miss         │ — never (Compact retired)
```

## Cache economics, before vs after

| | Today | Two-zone |
|---|---|---|
| Typical turn | break at previous user msg (index fold) → re-prefill last turn + index | full LCP hit; re-prefill = new input only |
| Every k turns | — | one demotion batch → re-prefill ≤ tail budget |
| memory_write turn | (same as typical) | break at index slot → re-prefill tail |
| At 80% fill | total miss + model-written lossy digest + mid-task cut | never happens; prompt size is flat |
| Prefill growth | linear per turn until cliff | bounded constant |
| Info loss | digest of *everything*, unrecoverable in-session | user words verbatim forever; tool spill demoted to citations, recoverable exactly |

## Phases (each shippable + measurable)

- **P1 — index off the fold.** Move `memoryIndexNote` from the per-turn
  `EphemeralSystem` fold to a fixed prefix message. One small change, ends the
  every-turn cache break. Metric: `LastPromptTokens` delta per turn on a warm
  local backend (prefill time), before/after.
- **P2 — the two-zone working set.** `internal/cache` model + demotion with
  watermarks + deterministic outline + wiring into `Turn`/`wireMessages`.
  `Compact` retired. Metric: per-turn prefill slope goes flat over a long
  scripted session; prompt size bounded.
- **P3 — recall.** `recall(citation)` tool + the system-prompt principle line.
  Metric: retention QA — answer questions whose raw detail was demoted
  (the model must cite-and-recall); recall round-trips per session.
- **P4 — outline fold.** `Summarize` over the oldest outline lines under cap
  pressure. Metric: outline bound holds over a very long session; citations
  survive the fold.

P1 is worth shipping alone. P2 is the architecture; P3 makes demotion safe to
be aggressive about; P4 only matters for genuinely long-haul sessions.

## Evals

The deterministic core is automated as **the context eval**
(`cmd/loop/context_eval_test.go`, plain `go test`): scripted sessions through
the real `Turn()` loop asserting, at every seam (demotion, fold — including a
deliberately lossy summarizer, Compact, Clear, resume), that (a) every message
is on the wire or reachable via a resolvable citation, (b) the wire stays
within the zone budgets, (c) ordinary turns re-prefill only the new suffix
under an LCP-cache model while demotion turns stay ≤ the zone budgets, and
(d) outline labels stay unique and monotonic. The live-model evals below
remain manual:

- **Prefill per turn** (the headline): tokens re-prefilled per turn (derivable
  from `LastPromptTokens` vs prior turn + backend cache stats where available),
  slope over a 100-turn scripted session on a 32k window.
- **Session length on a fixed window** before quality degrades — the
  `working-memory.md` "forever" metric, now with a flat cost curve.
- **Retention QA**: post-demotion questions about demoted turns; pass =
  correct answer via outline or recall.
- **Recall precision**: does `recall(citation)` return the right message
  (deterministic, so this is a unit test, not a model eval).

## Open questions

- **Tail sizing unit** — turns vs tokens. Tokens (chars/4 estimate, as
  everywhere else) with a whole-turn floor seems right; a single huge turn may
  exceed the low-watermark on its own.
- **Huge user pastes** — verbatim-forever conflicts with the outline budget.
  Proposed: head + citation above a size floor; the *typed* user intent is
  small and stays verbatim.
- **Outline zone role** — one `user` message vs `system`. `user` labeled
  "Session so far (older turns; recall(citation) for raw detail)" keeps the
  system prompt fixed and is what local chat templates handle most predictably.
- **Where the frontier lives on resume** — derive by replaying policy
  (preferred; keeps the transcript schema untouched) vs persisting a frontier
  event. Replay is deterministic given fixed watermarks; a policy-constant
  change between versions shifts the derived frontier, which is acceptable
  (it's a cache, not a record).
- **Does the outline dilute small-model attention** — same measure-don't-assume
  flag as the study findings prefix; the retention QA eval gates P2 becoming
  default.

## Possible follow-ups (out of scope for this design)

Noted, not designed — each only matters after P2 is live and measured:

- **Per-batch outline messages.** For message-granular caches (Anthropic
  `cache_control`), appending inside one growing outline message invalidates
  from the outline's *start*; appending each demotion batch as a new small
  zone-A message is append-only at message granularity too. Needs a
  render-time merge for strict-alternation chat templates. Irrelevant while
  the target is token-level LCP (llama.cpp).
- **`recall` scoping.** Keep `recall` out of the Study profile's toolset —
  coder-only vocabulary, same bounding discipline as the rest of study.
- **Pinning.** If retention QA shows mid-task demotion of assistant-authored
  plans hurting, add pinned turns (the `working-memory.md` anchor-turn idea)
  rather than salience scoring.
- **LLM-written outline entries.** The escape hatch if the mechanical outline
  proves below the usefulness floor on a ≥7b model (the model can't bridge
  outline line → recall). Pays determinism/replay for richness; only on eval
  evidence.
