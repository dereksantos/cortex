# Benchmarks — overview

Cortex measures itself in two complementary ways:

1. **Hand-authored YAML scenarios** under `test/evals/` — fast, deterministic,
   cover bespoke patterns we care about (LoCoMo-style multi-hop, GoL coding,
   library-service multi-session).
2. **Dataset-driven benchmarks** under `internal/eval/benchmarks/` — talk to
   the same external yardsticks the rest of the field reads (LongMemEval,
   MTEB, SWE-bench Verified, NIAH/RULER smoke). Numbers from these are
   directly comparable to published results.

Both paths emit the same `CellResult` through the same `Persister` fan-out,
so analysis tooling (`.cortex/db/cell_results.jsonl`, the SQLite
`cell_results` table, `.cortex/journal/eval/`) treats them uniformly.

For the full "what dimension of a chat-REPL coding harness does each benchmark
cover, what's still a gap, and what's the roadmap to close them" picture, see
[`coverage-matrix.md`](coverage-matrix.md) — the living map of the 10
dimensions against wrapped + candidate + proxy benchmarks.

## Package layout

```
internal/eval/benchmarks/
├── benchmark.go      # Benchmark interface, Instance, LoadOpts, Env
├── registry.go       # Register/Get + ErrUnknownBenchmark
├── cache.go          # EnsureCached helper for HF/HTTPS dataset fetches
├── longmemeval/      # memory benchmark (LLM-judge scored) — loop 03
├── mteb/             # retrieval+rerank, mechanical NDCG/MRR  — loop 05 (NFCorpus wired; see mteb.md)
├── swebench/         # coding via in-process harness          — loop 04
└── niah/             # synthetic long-context smoke test       — loop 02
```

The four subpackages land in follow-up PRs; see `docs/prompts/benchmarks/`
for the per-loop briefs.

## Adding a new benchmark

1. Pick a name; reserve it in the brief for that loop.
2. Create `internal/eval/benchmarks/<name>/{loader.go, runner.go, ...}`.
3. Implement the `benchmarks.Benchmark` interface: `Name`, `Load`, `Run`.
4. From an `init()` in your package call
   `benchmarks.Register("<name>", func() benchmarks.Benchmark { return &Bench{} })`.
5. Have the package be imported transitively from the CLI: add a blank
   import in `cmd/cortex/commands/eval_benchmark.go` (or wherever the
   per-benchmark imports live in a follow-up).
6. Add `docs/benchmarks/<name>.md` with: source URL, license note,
   what's measured, what's deferred to Phase B, reproduction commands.

`Run` should:
- emit a fully-validated `CellResult` with `Benchmark` set to your name;
- use `ScenarioID = "<name>/<instance.ID>"`;
- write nothing to disk outside `env.Workdir` (the CLI handler owns the
  scratch dir) or `~/.cortex/benchmarks/<name>/` (the dataset cache);
- NOT call `env.Persister.PersistCell` itself — the CLI handler does, so
  unit tests can run `Run` against a nil persister.

## Cache layout

Dataset files land under the per-user cache root:

- `$XDG_CACHE_HOME/cortex/benchmarks/<name>/...` if set, else
- `~/.cortex/benchmarks/<name>/...`

Writes are atomic (temp file in same directory → rename), so a crash
mid-fetch leaves no partial files. Idempotence: a second call with the
same arguments returns the cached path without re-fetching.

First-time fetches log the source URL to stderr so the operator has an
audit trail of where the data came from.

## CLI

```
cortex eval --benchmark <name> [--subset <sub>] [--limit N] [...per-benchmark flags]
```

Shared flags (parsed by `cmd/cortex/commands/eval_benchmark.go`):

- `--benchmark NAME`   pick the benchmark
- `--subset NAME`      benchmark-defined subset (oracle | verified | NFCorpus | ...)
- `--limit N`          cap number of instances

Per-benchmark flags (parsed inside each `loader.go`) layer on top — see
the respective `docs/benchmarks/<name>.md`.

## Reports

Group existing report tooling by benchmark family via the SQLite
`benchmark` column or the JSONL field:

```bash
sqlite3 .cortex/db/evals_v2.db \
  "SELECT benchmark, COUNT(*), AVG(task_success) FROM cell_results
   WHERE benchmark != '' GROUP BY benchmark;"
```

`cortex eval --report-summary --report-scenario-prefix longmemeval/` works
because of the `ScenarioID = "<name>/<id>"` convention.

## What's NOT here

- `tools/lm-eval-cortex/` — an optional Python shim that would let Cortex
  register as a model with the canonical lm-evaluation-harness or
  SWE-bench harness. Deferred until Phase A produces signal.
- Open LLM Leaderboard v2 tasks (MMLU-Pro / GPQA / MUSR / MATH / IFEval /
  BBH). Those score the LLM, not the context broker; out of scope.
- MTEB classification / clustering / STS subtasks. Wrong layer for a
  retrieval-focused system; Phase B if ever wanted.
