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
> **Status.** Design.
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

- **P1 — accumulating findings prefix.** Add `InferInput.PriorFindings`; thread
  `StudyLoop`'s accumulated digests into each pass (capped at a fixed budget
  split); `BuildInferPrompt` places it *before* the sample. Continuity + a
  cacheable prefix, no curation yet. Eval: does pass N reference pass N-1's
  findings; coverage/groundedness at equal total budget vs today.
- **P2 — curate the findings.** When the findings block crosses its budget, run
  the keep/compress/evict triage (`value.score` the digests, `attend.compress` the
  rest) so long runs stay bounded. Eval: retention quality (are dropped findings
  recoverable / unimportant), bound never exceeded.
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

## Open questions

- **Budget split** — what fraction of the window should findings get? Static, or
  grow with pass count until a cap?
- **Curation trigger** — pass count, findings-token pressure, or a redundancy
  signal? (Hysteresis: as late as possible to protect the cache.)
- **Findings granularity** — full prior digests, or a re-distilled running summary?
  Digest-on-digest decay is the same risk `working-memory.md` flags.
- **Does it help the small model at all** — or does directed, accumulating context
  only pay off above a model-size threshold? The eval gates this before it becomes
  a default.
