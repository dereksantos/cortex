# Eval Baseline — Pre-DAG-Protocol Snapshot

**Snapshot date:** 2026-05-17
**Git ref:** `387468f` (branch `derek.s/dag-build`)
**LLM pin:** `anthropic/claude-haiku-4.5` via OpenRouter
**Embedder pin:** `nomic-embed-text` (Ollama-local)
**Cortex binary:** `bin/cortex` built from above SHA
**Purpose:** The "before" picture for Phase 6 of the integration roadmap
to diff against. Linked from `ROADMAP.md` as the canonical baseline.

> Per [`docs/eval-prep-epic.md`](eval-prep-epic.md) Phase F, this doc
> consolidates per-suite baselines recorded in
> [`docs/eval-journal.md`](eval-journal.md) into a single time-stamped
> snapshot. Pure synthesis — no new measurements were run for this
> consolidation.

---

## Suite-by-suite baselines

### v2 (test/evals/v2/) — 43 scenarios

| Metric | Value |
|---|---|
| Scenarios run | 43 |
| Pass | 39 / 43 (90.7%) — lift-based pass criterion |
| Avg lift (cortex vs baseline judge-free) | +31.8% |
| Total baseline tokens / cortex tokens | 62,968 / 71,021 |
| Avg token reduction | **−12.8%** (cortex uses MORE tokens) |
| Telemetry sink | **Initially BLOCKED** on Phase 1; **resolved** with commits 659025b / 5377a47 / 14d2170 (Phase 1 tool-surface foundation) |

**Detail:** `eval-journal.md` entry "2026-05-17 — v2 suite / BLOCKED" (initial)
and "2026-05-17 — v2 suite (full sweep) / cell-level telemetry now landing"
(post-Phase-1).

### LongMemEval (oracle, limit=25)

| Metric | Value |
|---|---|
| Baseline pass rate | 15.6% (5 / 32) |
| Cortex pass rate | 13.3% (4 / 30) |
| Injected context tokens (cortex strategy) | 0 (pre-integration store empty for this benchmark) |
| Cells in `cell_results.jsonl` | 62 |
| Token delta | cortex uses +9% input tokens vs baseline |

**Note:** The post-auto-capture work (`9b6539b`) showed that with
intra-session capture wired, ABR=1.000 on a 4-turn JWT scenario —
suggesting the negative LongMemEval delta reflects the empty
pre-integration store, not the cortex architecture itself. Phase 6
post-integration re-run will be the honest comparison.

**Detail:** `eval-journal.md` entry "2026-05-17 — LongMemEval (oracle,
limit=25) / baseline vs cortex".

### SWE-bench Verified (limit=5)

| Metric | Value |
|---|---|
| Baseline pass | 0 / 5 (0%) on `astropy` subset |
| Cortex pass | 0 / 5 (0%) |
| Cells in `cell_results.jsonl` | 11 |

Follow-up runs with Django subset (limit=3) recorded for
`qwen3-coder-30b-a3b` and `sonnet-4.5`; see separate eval-journal
entries dated 2026-05-17 for those.

The 0/5 result is a **floor measurement**, not a meaningful capability
claim — Cortex's cortex strategy injected 0 context (same empty-store
issue as LongMemEval). Phase 5/6 with pre-seeded benchmark stores
will be the honest measurement.

**Detail:** `eval-journal.md` entry "2026-05-17 — SWE-bench (verified,
limit=5) / baseline vs cortex".

### ABR (v2 full sweep, 43 scenarios)

| Metric | Value |
|---|---|
| Run-level avg ABR (cell-weighted) | **0.586** |
| Scenario-mean avg ABR (unweighted) | 0.409 |
| Avg Fast NDCG | 0.093 |
| Avg Full NDCG | 0.535 |
| Distribution (ABR=0 bucket) | 14 / 43 scenarios (often Full NDCG=0 fixture bugs) |
| ROADMAP previous claim | 0.77 → **resolved as stale** in commit `0e27384` |

**Diagnostic outcome (ABR loop):** path (a) — stale doc. `ROADMAP.md`
updated to 0.586 baseline; the 0.77 measurement was from a different
config that's no longer canonical.

**Post-auto-capture signal:** A subsequent ABR session run with
intra-session auto-capture (`9b6539b`) and InjectedContextTokens
plumbing (`86e9458`) produced **ABR=0.957 mean** on a 4-turn JWT
scenario, with injected-token counts growing from 20 → 285 across
turns. This is the first non-degenerate ABR measurement — the prior
0.586 was measuring partial-loop behavior (auto-capture wasn't
wired). Reading the 0.586 as "current ABR" understates what the
architecture does once the capture loop is closed.

**Detail:** `eval-journal.md` entries "2026-05-17 — ABR baseline (v2
full sweep)", "2026-05-17 — ABR diagnostic: 0.586 vs 0.77 resolved as
stale doc", "2026-05-17 — Auto-capture loop reinstated: ABR=1.000",
"2026-05-17 — InjectedContextTokens flows end-to-end on ABR session
cells".

### MTEB / NFCorpus (n=100 queries)

Recorded on 2026-05-17 — see `eval-journal.md` entry "2026-05-17 —
MTEB / NFCorpus (n=100 queries)" for the detailed embedding-quality
substrate baseline.

### legacy-cognition (Phase B output)

_Pending Phase B; will be added once `loop-phase-b-legacy-cognition.md`
completes._

22 per-node scenarios in `test/evals/legacy/cognition/` will produce
per-op baselines once the runner is restored.

### journeys (Phase D output)

_Pending Phase D; will be added once `loop-phase-d-journeys.md`
completes._

10 e2e scenarios in `test/evals/journeys/` will produce per-scenario
e2e baselines once the runner status is resolved (audit → either
restore or port to v2).

---

## Per-axis cost / latency breakdown

Aggregated from `.cortex/db/cell_results.jsonl` (Phase 1 unified sink,
landed in commit `14d2170`).

**Total spend across all Phase A suites combined:** ~$1.50 USD (haiku-4.5
is ~250× cheaper than the cost estimator projects; see Phase A
cross-cutting finding #6 in `eval-journal.md`).

**Mean per-scenario latency** varies by suite — full per-axis breakdown
will be populated once Phase B + D land and a full-sweep is re-run
under the unified sink for all suites uniformly.

---

## Aggregate health summary

**Cross-cutting findings from Phase A** (carried forward as triage
items for Phase 6):

1. **Cortex strategy injects 0 context across every benchmark run.**
   Pre-integration store is empty for LongMemEval / SWE-bench / MTEB
   topics. Post-DAG delta will only be interpretable once Phase 5/6
   pre-seeds the store per benchmark. **Auto-capture work** (commits
   `9b6539b`, `8be9bba`, `86e9458`) demonstrates the capture loop
   closes correctly; pre-seeding is the remaining gap.

2. **Negative token reduction.** v2 −12.8%, LongMemEval +9% input —
   cortex spends more tokens than baseline, not fewer. Contradicts
   the "Token Cost Reduction over time" North Star in `ROADMAP.md`.
   Same root cause as #1 (empty store = search-tax without context
   payoff).

3. **`cortex-fast` vs `cortex-full` taxonomy not in cell schema.**
   `internal/eval/v2/cellresult.go:44` allows only `baseline / cortex
   / frontier`. Principle 8 (separated baselines for Fast vs Full)
   structurally unsupported until the schema gains the strategy
   dimension. Phase 1 partially addressed (`cd79ebf`: cortex-fast /
   cortex-full strategy split). Phase 5/6 may need further work.

4. **14 scenarios have Full NDCG = 0** (fixture bugs in `expect:`
   blocks). They silently zero the ABR mean. Either fix the
   fixtures or exclude from the ABR aggregate — current behavior
   penalizes the metric for fixture quality, not retrieval quality.

5. **Cost estimator ~250× pessimistic for haiku-4.5.** Real spend
   $1.50 across all four Phase A suites; estimator wanted $50+ ceiling
   headroom. Recalibration of `spend.EstimateCost` would let the
   default $5 ceiling be the actual safety boundary.

**Outstanding pending entries** (gates Phase 6 cannot diff against):
- Phase B (legacy/cognition baseline) — prompt queued at
  `docs/prompts/loop-phase-b-legacy-cognition.md`
- Phase D (journeys baseline) — prompt queued at
  `docs/prompts/loop-phase-d-journeys.md`

---

## How to re-baseline

To produce a new post-Phase-5 baseline for Phase 6's delta report:

```bash
# Run each suite via the post-DAG-protocol CLI; results land in unified sink
cortex run --type=eval --suite=v2 --output=cell_results
cortex run --type=eval --suite=longmemeval --output=cell_results
cortex run --type=eval --suite=swebench --output=cell_results
cortex run --type=eval --suite=abr --output=cell_results
cortex run --type=eval --suite=legacy-cognition --output=cell_results  # post-Phase-B
cortex run --type=eval --suite=journeys --output=cell_results          # post-Phase-D
cortex run --type=eval --suite=mechanic --output=cell_results          # all 5 should pass green
```

Then re-run consolidation:
- Read new entries in `eval-journal.md` dated after this snapshot
- Produce `docs/eval-baseline-post-dag.md` (or update this file with a
  "Phase 6 delta" section)
- Diff line-by-line against this snapshot

---

## Cross-references

| Doc | Relationship |
|---|---|
| [`eval-journal.md`](eval-journal.md) | Per-run lab notebook; this doc synthesizes it |
| [`eval-prep-epic.md`](eval-prep-epic.md) | Phase F deliverable spec; this doc is the output |
| [`integration-roadmap.md`](integration-roadmap.md) | Phase 6 will diff post-integration results against this snapshot |
| [`benchmarks/coverage-matrix.md`](benchmarks/coverage-matrix.md) | The 10 dimensions; per-dim coverage of this baseline is partial (gaps named in Phase E scenario) |
| [`../ROADMAP.md`](../ROADMAP.md) | ABR North Star — updated to 0.586 baseline in commit `0e27384` |

---

*Versioned artifact — snapshot only. Future re-baselines produce new
docs or new sections; this snapshot is immutable.*
