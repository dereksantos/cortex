# Mechanic Evals — DAG Executor Acceptance Suite

5 deterministic fixtures that verify the DAG executor's mechanical
correctness. Each fixture is **network-free**, uses **mocked handler
costs**, and produces **byte-identical output** on re-runs.

## Status

All 5 fixtures **fail today** with a structured `error_code:
not_implemented` row in `.cortex/db/cell_results.jsonl`. They will
pass green when [`docs/dag-build-plan.md`](../../docs/dag-build-plan.md)
Stage 1 v0 lands the executor.

This is the TDD signal for the executor: when v0 is "done," all 5 fixtures
go green. If any regresses during later stages, the executor broke an
invariant the protocol depends on.

## The 5 fixtures

| ID | Verifies |
|---|---|
| `mechanic-1-budget-decay` | Budget arithmetic correct; no drift across N node calls |
| `mechanic-2-tree-reconstruction` | `parent_node_id` chains rebuild the same tree post-hoc |
| `mechanic-3-depth-cap` | Hard depth bound trips before infinite recursion |
| `mechanic-4-budget-exhaustion` | In-flight finishes; new spawns refused; no orphans |
| `mechanic-5-tree-shape-variation` | Tree shape varies based on inputs (not always same default) |

## Run

```bash
cortex eval --suite=mechanic
```

Each fixture produces one or more rows in
`.cortex/db/cell_results.jsonl`. While the executor is unimplemented,
expect rows with `error_code: not_implemented`. When the executor
ships, expect rows with `ok: true` matching each fixture's
`expected` block.

## Why mocked handlers

Per [`docs/prompts/eval-principles.md`](../../docs/prompts/eval-principles.md)
principle 4 (Reproducible) and 5 (Isolated): mechanic correctness
must be testable without LLM calls, network, or real-world timing.
Mocked `cost_consumed` values give the executor concrete numbers to
decay against; tests assert against exact post-decay values.

A fixture that depends on real LLM latency or real model output is
the wrong test for this suite — those belong in v2 / journeys /
legacy-cognition where output quality is the signal.
