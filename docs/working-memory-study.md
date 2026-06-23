# Working Memory, instance #1: the persistent study session

> **Purpose.** Turn multi-pass `study` from independent one-shot samples into a
> single accumulating investigation, by giving it a **curated findings prefix**:
> the distilled results of prior passes ride the front of each pass's prompt
> (stable, cached), and only this pass's new sample varies at the tail. This buys
> continuity (pass N builds on pass N-1), a cacheable growing prefix (the reason
> study can't cache today — see below), and findings-directed sampling.
>
> **Why study first.** Study is the cleanest bounded instance of the working-memory
> problem in [`working-memory.md`](working-memory.md): one model, one task,
> deterministic passes, a clear "finding" unit (the digest). Proving the
> curated-prefix pattern here de-risks it before the open-ended REPL/Discord case.
>
> **Status.** Design resolved (see Decisions). **P1–P4 all landed +
> unit-tested** on `working-memory/p1-findings-prefix`, with a synthesis
> continuity metric replacing the disjoint-blind relay signal. Live runs on a
> local 3b (see docs/eval-journal.md 2026-06-19):
> - **P4 (caching) — confirmed working:** 0 prefix breaks across passes; the
>   `[system][goal][findings]` prefix extends append-only (Focus + sample are
>   the volatile tail). cache_control for Anthropic is the only remaining
>   provider tail; local backends cache the LCP automatically.
> - **P1 — PROVEN at depth above a ~3b floor.** Multi-model probe + a 4-pass
>   gpt-5.4 run (eval-journal 2026-06-20): on-blind vs off gave coverage +23pts
>   (14→37), grounding +21pts (79→100), synth 7×, relays 0→6 — and WM *sustained*
>   the deepening loop (off stopped early at 14%). 3b shows nothing (capability
>   floor); 70b/coder/gpt-5.4 all gain, in model-dependent currency.
> - **P3 directed sampling — NEGATIVE.** on-directed underperformed on-blind on
>   every quality axis at equal coverage; blind disjoint draws beat lead-chasing.
>   No evidence it helps. Stays OFF.
>
> **Recommendation:** make the findings prefix (P1) + curation (P2) + cacheable
> prefix (P4) a default for capable study models (>~7b); leave directed sampling
> (P3) opt-in/off pending a better focus-selection policy. The fleet `study`
> model is llama-3.2-3b (below the floor) — bump it to a ≥7b for the gains to
> show.
>
> **Owner.** `internal/study/` (the pass loop + prompt assembly) + the
> working-memory triage nodes in `pkg/cognition/dag/ops/`.
>
> **Builds on.** [`working-memory.md`](working-memory.md) (the keep/compress/evict
> triage), and the prefix-cache work in `cmd/loop` (tail-injection + cache_control).

---

## The problem today

`StudyLoop` (`internal/study/study_loop.go`) runs N passes. Each pass calls
`StudyFile` with an accumulating `covered` set so it samples *new* regions, and
the loop collects `res.Digests` and `res.Citations`. But each pass's inference is
**independent**: `BuildInferPrompt` (`infer.go:72`) builds

```
[ inferSystemPrompt ]                         ← stable, ~300 tok
[ "Sampled regions of X:" + THIS pass's snippets ]   ← ~12K tok, unique per pass
[ goal ]
```

Two costs fall out of this shape:

1. **No continuity.** Pass 2 never sees pass 1's digest. Passes are snapshots, not
   an investigation — they can rediscover the same thing and can't synthesize
   across regions they didn't sample together.
2. **No cacheable prefix.** The bulk (the sample) is different every call and the
   stable envelope is tiny, so prefix caching saves ~1–2%. Study deliberately
   *doesn't* accumulate (to fit the window), which is exactly why it can't cache —
   caching and fit-the-window are at odds *until* we add curation.

## The design: curated findings prefix + new-sample tail

Reshape each pass's prompt so the stable, accumulating part leads and the variable
sample trails:

```
[ inferSystemPrompt ]                 ← stable
[ goal ]                              ← stable within a study run
[ curated findings so far ]           ← grows by append across passes → CACHED prefix
[ this pass's new sample ]            ← the only part that re-prefills
```

- The **findings block** is the *working set*: the distilled digests (and key
  citations/leads) from prior passes, bounded by a token budget carved from the
  window. It is cached across passes (it only grows by append between curations).
- The **sample** is the variable tail — unique per pass, re-prefilled each time,
  which is unavoidable and correct.

Pass 1 has no findings, so it's unchanged (full sample budget). The prefix appears
from pass 2 on and grows with the investigation.

## What it buys

| Win | Mechanism |
|---|---|
| **Continuity / synthesis** | Pass N reads pass N-1's findings → builds on them, dedups, connects regions never sampled together (the multi-hop story, inside study). |
| **Cacheable prefix** | `[system][goal][findings]` is append-stable across passes → backend prefix cache (LCP / DeepSeek-GLM auto, Anthropic via `cache_control`) reuses it; only the new sample re-prefills. |
| **Directed sampling** | Findings + `Deepen.Leads`/`Focus` steer the next pass toward open threads instead of blind disjoint draws. |

## Reuse vs new

| Piece | Where | Status |
|---|---|---|
| Accumulated digests / citations across passes | `StudyLoop` (`res.Digests`) | exists |
| Leads / Focus / Deepen affordances for directed sampling | `Deepen{Densify, Target{Focus}}`, `Lead` | exists |
| Prompt assembly seam | `BuildInferPrompt` (`infer.go:72`) | exists |
| keep/compress/evict triage over findings | working-memory triage node (`value.score` + `attend.compress`) | exists (compose) |
| `cache_control` on a stable prefix | `cmd/loop` Anthropic path | exists (port) |
| **Findings field on `InferInput` + prefix placement** | `infer.go` | **new** |
| **Window budget split** (findings vs sample) | `study.go` / `sampleAndInfer` | **new** |
| **Curation of the findings block** | study loop ↔ triage | **new** |

## Tensions (call them out — they decide the phases)

1. **Sample budget vs findings budget.** The window is fixed: `system + findings +
   sample + output ≤ window`. Every token of findings is a token less of sample.
   The split (e.g. findings ≤ 20% of the window) is the key tunable, and an eval
   question, not a guess.
2. **Curation churn vs cache stability.** Rewriting the findings block every pass
   (compress/evict) changes the prefix and breaks the cache — the same hysteresis
   problem `working-memory.md` names. Default to *append*; re-curate only under
   budget pressure, so most passes stay cache-warm.
3. **Small-model distraction.** A growing findings prefix could help synthesis or
   could dilute the model's attention on the new sample. Measure, don't assume —
   especially given the per-chunk fragment-size law already governs grounding.

## Phases (each shippable + evaluable)

- **P1 — accumulating findings prefix.** Add `InferInput.PriorFindings` as a
  slice of **structured units** (`{pass, digest, citations, leads}`, NOT a
  pre-joined string — so P2 curation has units to score/compress/evict);
  thread `StudyLoop`'s accumulated findings into each pass; `BuildInferPrompt`
  renders them *before* the sample. The findings budget is **growth-based,
  capped** (see Decisions): early passes give the window to the sample, later
  passes let findings claim more, up to a cap, with a sample floor so the new
  sample never starves. Append-only, no curation yet. Eval: does pass N
  reference pass N-1's findings; coverage/groundedness at equal total budget vs
  today.
- **P2 — curate the findings.** When the appended block crosses its budget
  high-watermark, run the keep/compress/evict triage (`value.score` the
  findings, `attend.compress` the rest) so long runs stay bounded. **Curation
  contract:** compression shortens the *prose* but preserves each finding's
  *citation anchors* verbatim, so the `citationRelayed` relay (`infer.go`) still
  lets a later pass cite through to the original lines — compressing away the
  `path:N-M` coordinates would decay a citable finding into a mere lead.
  Eviction demotes to the journal (recoverable via `vector_search`), it does not
  delete. Each curation is a one-time prefix rewrite (the single cache miss);
  between curations the block stays append-stable. Eval: retention quality (are
  dropped findings recoverable / unimportant), bound never exceeded.
- **P3 — findings-directed sampling.** Use accumulated findings + `Leads` to set
  the next pass's `Focus`, replacing blind disjoint draws. Eval: coverage-to-value
  (does directed sampling find more relevant regions per pass).
- **P4 — cache the prefix.** `cache_control` the `[system][goal][findings]` block
  for Anthropic (auto elsewhere) + hysteresis on curation so the prefix stays
  byte-stable between curations. Eval: cross-pass cache-hit rate, latency, cost.

P1 alone delivers continuity and most of the cache win; P2–P4 make it sustainable
and fast. Going straight to P4 collides budget-split, curation, and cache-stability
at once — crawl.

## Evals

Per `eval-strategy.md`, the headline metric is **groundedness/coverage per dollar
over a multi-pass run** vs today's independent-passes baseline. Supporting:

- **Continuity** — fraction of pass-N digests that build on / cite prior findings.
- **Coverage at equal budget** — does the findings prefix cost net coverage, and
  is the trade worth it (the P1 budget-split question).
- **Cross-pass cache-hit** — `sim_best` on the `[system][goal][findings]` prefix
  across passes (the direct read of whether the prefix is actually stable).
- **Latency / cost** — wall-clock and tokens per multi-pass run vs baseline.

## Decisions

- **Budget split — grow until capped.** The findings budget is not a fixed
  fraction. It grows with pass count up to a cap, so early passes spend the
  window on the sample (coverage-first, little to remember yet) and later passes
  let findings claim more:
  `findings_budget(n) = min(cap, base + rate·n, window − sample_floor − envelope)`.
  The `sample_floor` guarantees the new sample never starves; `cap` and `rate`
  are provisional constants the eval sweeps (like the 0.3 fill).
- **Findings granularity — full prior digests, appended (option A).** The block
  is the prior findings verbatim, append-only. This is faithful and
  cache-stable (the prefix only grows at the tail). The rejected alternative — a
  re-distilled running summary — compounds loss (a summary of summaries:
  "digest-on-digest decay", the risk `working-memory.md` flags) AND rewrites the
  whole block every pass (cache-hostile). The linear-growth cost of appending is
  deferred to P2 curation rather than paid continuously as quality decay.
- **Curation trigger — dynamic, not scheduled.** Fire on actual budget pressure
  (the block crossing its high-watermark), preferentially evicting/compressing
  what a redundancy/value signal marks low-novelty — never a fixed "every K
  passes" counter. A fixed schedule forces prefix rewrites (cache misses) on
  passes that didn't need them; a pressure-triggered predicate keeps the prefix
  byte-stable for as long as possible. This is the hysteresis the design wants.
  (P2.)
- **Does it help the small model at all** — the eval gates this before
  accumulating context becomes a default; no assumption baked in.

## Curation budget + targeted sampling (2026-06-20)

Two coupled additions after observing a REPL transcript where the coder studied
`main.go` (58% digest) and then `read_file`'d the whole 155 KB file anyway:

- **Fixed curation budget** (`curationBudgetTokens`, cmd/loop). `read_file`
  redirects to study above a *fixed* token threshold, NOT a fraction of the
  coder window. Sizing the trigger to the window let a big-window model read
  everything raw, defeating curation — the whole point is to spend the coder's
  context on distilled signal, not raw bytes it could technically hold.
- **Keyword focus — removed (2026-06-23).** A pass-1-only, env-gated
  (`CORTEX_STUDY_KEYWORDS`), never-default workaround for the goal not reaching
  the sampler: it extracted the goal's terms, scanned chunk content for them, and
  biased sampling toward matches ("search without an agent"). Removed because two
  other mechanisms cover its job with clearer purpose: **module-boundary scoping**
  (`scopeToRootModule`, structural + automatic — uses the build system's own
  markers, not content search) and **curator TARGET/directed sampling**
  (LLM-guided, per-pass). Three sampling mechanisms collapsed to two — the
  boundary layer (structural) and the curator layer (LLM-guided). The
  `Focus.Keywords` / `StudyRequest.KeywordFocus` fields, `keyword_focus.go`, and
  the env flag are all gone.
- **Reporter-not-critic prompt** (`inferSystemPrompt`). The study model is a
  grounded *reporter* — it surfaces structure/responsibilities/signals relevant
  to the goal, never verdicts. Keeps the small model on retrieval (its strength)
  and leaves judgment to the coder, even when the goal asks for an assessment.

### Parked: agentic study (search tools)

Considered giving the study model real search tools (grep/read-range/find-refs)
to drive its own exploration — "wide thin net, then dig in" under model control.
**Parked, not pursued:** small models (3–4B) have poor tool-calling discipline
(observed this session — xlam-1b won't call tools; eval-journal notes mistral-7b
failing the same way), so an agentic loop on the cheap study model is the regime
most likely to flail. Module-boundary scoping + curator targeting get most of the
targeting benefit *mechanically* (bounded, predictable, no tool-use loop).
Revisit agentic study only if/when a capable-enough study model is the binding —
and as a separate bet, not the default path.
