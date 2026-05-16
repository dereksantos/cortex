# Benchmark Integration — Agent Loop Sequencing

This directory holds five self-contained agent-loop briefs that together
integrate canonical industry evals (LongMemEval, MTEB retrieval, SWE-bench
Verified, NIAH/RULER smoke) into Cortex. Each `.md` is a brief one Claude
Code agent loop can pick up and execute end-to-end against the repo, ending
with a branch + commits + report.

The full plan they were decomposed from lives at
`~/.claude/plans/i-want-to-integrate-joyful-oasis.md`.

## Sequencing DAG

```
                ┌──────────────────────────────┐
                │  01-skeleton.md              │
                │  registry + cache + CLI +    │  (strict prerequisite)
                │  CellResult.Benchmark field  │
                └──────────────┬───────────────┘
                               │
        ┌──────────────┬───────┴───────┬──────────────┐
        ▼              ▼               ▼              ▼
  02-niah.md     03-longmemeval.md  04-swebench.md  05-mteb.md
  (~half day)    (~2 days)          (~3-5 days)    (~2 days)
  synthetic      HF + judge         HF + harness   HF + storage
  no fetcher     LLM-judge scored   + Docker        mechanical NDCG/MRR
                                    + score refactor
```

**Strict prerequisite:** 01-skeleton must merge before any of 02–05 starts.
The skeleton defines the `Benchmark` interface, `Persister` schema migration,
and `--benchmark` CLI dispatch that all four downstream loops register
against.

**Parallel-safe:** 02, 03, 04, 05 can run in any order or simultaneously
after the skeleton lands. They own disjoint directories under
`internal/eval/benchmarks/<name>/` and append-only to `registry.go` and the
CLI flag set. If two run in parallel, expect a trivial rebase on
`internal/eval/benchmarks/registry.go` and `cmd/cortex/commands/eval.go`.

**Soft prep inside 04-swebench:** SWE-bench needs a small generic refactor
of `internal/eval/v2/score_gol_frames.go` → `score_repo_tests.go`. That
refactor lives inside the SWE-bench loop as its first commit (clearly
labeled `refactor(eval): extract generic score_repo_tests`). The other three
loops do NOT need it.

## Recommended execution order

If a single operator is running these sequentially, ordered by
signal-per-effort:

1. **01-skeleton** — unblocks everything; ~1 day
2. **02-niah** — fastest validation that the abstraction works end-to-end;
   half day; useful as a CI smoke test forever
3. **03-longmemeval** — first cross-comparable industry number on memory;
   exercises the judge path everyone else may eventually want
4. **04-swebench** — the showpiece; biggest LOC; needs Docker on the box
5. **05-mteb** — independent of the harness, exercises Reflex-layer only;
   answers a different question (embedder quality) than the others

If multiple agents run in parallel after skeleton lands, the order above is
also a reasonable risk ordering: the simpler ones land first and reveal any
hidden assumptions in the skeleton before the heavier loops invest in them.

## Branch model

```
main
 └─ feat/benchmarks-skeleton        (01-skeleton)
     ├─ feat/bench-niah             (02 — stacks on skeleton)
     ├─ feat/bench-longmemeval      (03)
     ├─ feat/bench-swebench         (04)
     └─ feat/bench-mteb             (05)
```

Each downstream branch rebases onto `main` after the skeleton PR merges.
Stacked PRs are an option if the skeleton hasn't merged yet but the
operator wants to start downstream work — note this in the PR description.

## Why these five and not others

Scope was set in the approved plan:

- **In:** LongMemEval (memory benchmark, MIT), MTEB retrieval+rerank
  (HuggingFace-ecosystem fit for Cortex's Reflex layer), SWE-bench Verified
  (coding via the new in-process harness), NIAH/RULER (synthetic smoke).
- **Out (deferred):** Open LLM Leaderboard v2 (MMLU-Pro/GPQA/MUSR/MATH/
  IFEval/BBH) — those score the LLM, not the broker. MTEB classification/
  clustering/STS — wrong layer for Cortex. LongMemEval `_s`/`_m` splits —
  Phase B after Oracle works. SWE-bench full (2,294) and SWE-bench Lite —
  Phase B; Verified is the standard reporting target in 2026.
- **Out (architectural deferral):** A `tools/lm-eval-cortex/` Python shim
  that would let Cortex register as a model with the canonical
  lm-evaluation-harness — adds Python to the build path; defer until Phase
  A produces signal.

## Common contract across all five loops

- **Branch off main** (or rebase onto it).
- **Standard library testing only** (table-driven `t.Run` subtests, no
  testify) — matches the CLAUDE.md constraint.
- **No backwards-compat shims** for `CellResult` schema changes — the
  skeleton adds one optional `omitempty` field; if a downstream loop needs
  more, it bumps `CellResultSchemaVersion` explicitly with rationale.
- **Every benchmark instance emits exactly one validated `CellResult`** via
  the existing `Persister.PersistCell()`. This is non-negotiable — the
  dual-projection (journal → SQLite + JSONL) is the analysis substrate the
  small-model-amplifier thesis is argued from.
- **`ScenarioID = "<benchmark>/<instance_id>"`** convention so existing
  report tooling (`--report-scenario-prefix`) keeps working.
- **End-of-loop deliverable:** a short report with at least one real number
  (pass rate, NDCG, latency, cost — whatever the benchmark measures), the
  shape of the failures, and what would be needed to scale beyond the
  smoke-test instance count.

## When to update this README

- Add a new benchmark → add its row to the DAG and the "in scope" list.
- Reorder dependencies → update the DAG diagram, not just the prose.
- Move a refactor between loops → note it in both the donor and recipient
  loop briefs.
- Mark a loop complete → add a one-line note at the bottom of the loop's
  brief: `Landed in PR #N (YYYY-MM-DD).` Do not delete the brief; future
  agents may need to read it to understand prior decisions.
