# Benchmark analysis

Read-only scripts over the durable store (`$CORTEX_BENCH_HOME`, default
`~/.cortex/benchmarks`) that reproduce the numbers in `docs/bench-results/`.
They never write to the store.

| Script | Reads | Answers |
|---|---|---|
| `paired.py <batch> [--table]` | `exports/tasks.csv` (run `scripts/bench/dashboard-export.sh` first) | each arm's solves on the tasks every arm was scored on; for two arms, the tasks only one solved and a two-sided exact sign test |
| `scoring.py <batch-dir>… [--mini] [--cap N]` | each run's own files (`results.v1.jsonl`, harness `report.json` / `test_output.txt`, session transcripts) | for cortex's failures: scoring collisions with the official test patch, broken PASS_TO_PASS tests; how often each agent ran the project's own test suite (approximate, a text match on shell commands); runs that ended at the tool-call cap |

Runs labeled `invalid=<reason>` are skipped by both, as by `benchstore export`.
