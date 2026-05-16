Goal: Land the `internal/eval/benchmarks/` package — interface, registry,
dataset cache, CellResult schema extension, persistence wiring, and CLI
dispatch — so the four downstream benchmark loops (NIAH, LongMemEval,
SWE-bench, MTEB) have a clean substrate to plug into.

This is a STRICT PREREQUISITE for 02-niah.md, 03-longmemeval.md,
04-swebench.md, 05-mteb.md.

WHY THIS MATTERS: Cortex's eval framework is excellent for hand-authored
YAML scenarios but has no abstraction for dataset-driven benchmarks. Every
team measuring memory or retrieval or coding agents speaks in
LongMemEval/MTEB/SWE-bench numbers; without a shared substrate we either
re-implement the substrate inside each benchmark (drift) or stuff each
benchmark into the YAML scenario shape (loses LongMemEval scale, all
SWE-bench Docker scoring). The skeleton fixes that by making each
benchmark a small package that returns `Instance`s and emits `CellResult`s
through the existing `Persister`. Once it lands, the four downstream
benchmarks can be built in parallel.

INVESTIGATE FIRST (don't assume — read the code):
- `internal/eval/v2/cellresult.go` — the schema contract.
  `CellResultSchemaVersion = "1"`; the comment on lines 9-12 makes clear
  that adding an `omitempty` field is non-breaking. The new field
  `Benchmark string` belongs in the Grid-dimensions block (alongside
  `ScenarioID`, `SessionID`, `Harness`).
- `internal/eval/v2/persist_cell.go` — single entry `PersistCell()` writes
  journal → SQLite + JSONL. Find the `cell_results` table DDL and the
  index list. New column needs a non-breaking `ALTER TABLE` (`IF NOT EXISTS`
  pattern if SQLite supports it for your version, else gated by a column
  existence check). New index: `idx_cell_results_benchmark`.
- `internal/journal/eval.go` — `EvalCellResultPayload` mirrors `CellResult`
  field-for-field; the same `Benchmark` field needs to be added so the
  journal entry round-trips.
- `cmd/cortex/commands/eval.go` — current flag set + dispatch. Find where
  `--harness cortex` dispatches to `runCortexCodingHarness` and add a sibling
  branch for `--benchmark <name>`.
- `internal/eval/v2/spend.go` — daily / per-run cost ceilings. Benchmark
  runs must honor these; do NOT add a separate budgeting path.
- `pkg/llm/openrouter_with_key.go` and `pkg/secret/keychain.go` — how the
  OpenRouter key is resolved out-of-band. Benchmark runners that need a
  judge model reuse this; do not introduce a new key path.
- `~/.cortex/` layout — check whether there is already a convention for
  user-state directories under `~/.cortex/`. If yes, mirror it for
  `~/.cortex/benchmarks/<name>/`. If no, document the choice in
  `docs/benchmarks/overview.md`.

DEFINITION OF DONE:
1. New package `internal/eval/benchmarks/` with three files:
   - `benchmark.go` — `Benchmark` interface + supporting types:
     ```go
     type Benchmark interface {
         Name() string
         Load(ctx context.Context, opts LoadOpts) ([]Instance, error)
         Run(ctx context.Context, inst Instance, env Env) (*evalv2.CellResult, error)
     }
     type Instance struct { ID, Payload }    // payload is benchmark-defined
     type LoadOpts struct { Subset, Limit, Filter map[string]string }
     type Env struct {
         Provider      llm.Provider
         JudgeProvider llm.Provider    // optional
         Persister     *evalv2.Persister
         Workdir       string
         Verbose       bool
     }
     ```
   - `registry.go` — `Register(name string, ctor func() Benchmark)` + `Get(name)`;
     init-time registration pattern so each benchmark package can `init()`
     itself in. Returns `ErrUnknownBenchmark` cleanly.
   - `cache.go` — `EnsureCached(name, url, dest) (string, error)` for HF
     Hub fetches; respects `XDG_CACHE_HOME` if set, else
     `~/.cortex/benchmarks/<name>/`. Atomic write (temp file → rename).
     Idempotent; logs license/source URL to stderr on first fetch.
2. CellResult extension:
   - `internal/eval/v2/cellresult.go`: add `Benchmark string \`json:"benchmark,omitempty"\``
     to the grid-dimensions block. SchemaVersion stays at `"1"`.
   - `Validate()` does NOT require it (optional field).
   - `internal/journal/eval.go`: same field added to `EvalCellResultPayload`
     and the marshal/unmarshal roundtrip.
   - `internal/eval/v2/persist_cell.go`: SQLite DDL gains `benchmark TEXT`
     column + `idx_cell_results_benchmark` index. Migration uses
     `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` (or a column-existence
     probe if your SQLite version lacks `IF NOT EXISTS` for this clause).
     Backfill is not needed — existing rows have NULL/empty, which is fine.
3. CLI dispatch in `cmd/cortex/commands/eval.go`:
   - New flag `--benchmark <name>`. When set, look up the benchmark from
     the registry, parse benchmark-namespaced flags (`--subset`, `--limit`
     at minimum; reserve `--tasks`, `--length`, `--depth`, `--strategy` for
     downstream loops to wire), call `Load()` → for each instance call
     `Run()` → `PersistCell()`.
   - Existing flag paths (`--harness`, plain YAML scenario) keep working
     unchanged.
   - On unknown benchmark: clean error listing registered names.
4. Tests (standard library only, table-driven):
   - `internal/eval/benchmarks/benchmark_test.go` — interface roundtrip:
     register a synthetic dummy benchmark, load 3 instances, run each,
     assert each `CellResult` validates and has the `Benchmark` field set.
   - `internal/eval/benchmarks/cache_test.go` — atomic write semantics
     (no partial files on simulated interrupt), idempotence (second call
     returns cached path without refetching).
   - `internal/eval/v2/cellresult_test.go` (or wherever existing
     CellResult tests live) — `Benchmark` field roundtrips through JSON +
     `Validate()` (empty AND populated cases both pass).
   - `internal/eval/v2/persist_cell_test.go` — write a CellResult with
     `Benchmark="dummy"`, read back from SQLite, assert column populated;
     same for the JSONL projection; same for the journal entry round-trip.
5. Docs:
   - `docs/benchmarks/overview.md` — one page: what the package is, how to
     add a new benchmark (link to the four downstream prompt files),
     cache layout, where reports land.
6. End-to-end smoke:
   - Build the binary. Run `./cortex eval --benchmark <dummy-registered-for-test>`
     (or remove the dummy and confirm `./cortex eval --benchmark unknown`
     prints a clean error listing 0 registered benchmarks).
   - Verify a written CellResult appears in `.cortex/db/cell_results.jsonl`,
     the SQLite `cell_results` table, AND `.cortex/journal/eval/*.jsonl`
     with the `benchmark` field set.

CONSTRAINTS:
- Standard library testing only (table-driven `t.Run` subtests). Match
  the CLAUDE.md rule.
- No new external dependencies. HTTP fetches in `cache.go` use
  `net/http`; JSON parsing uses `encoding/json`. The HF Hub serves plain
  HTTPS, no SDK needed.
- The `Benchmark` interface's `Run` returns a fully-validated
  `*evalv2.CellResult`. The registry layer does NOT call `PersistCell()`
  — the CLI handler does, so individual benchmarks can be unit-tested
  without a real Persister.
- Do NOT introduce a `Result` wrapper type, a `Score` interface, or any
  metric abstraction layer in the skeleton. Each benchmark fills in
  whichever `TaskSuccessCriterion` fits. Premature abstraction here ties
  hands of downstream loops.
- Do NOT touch `internal/eval/v2/score_gol_frames.go` in this PR. The
  generic-test refactor it needs belongs in 04-swebench.md.
- The cache is per-user, not per-project (`~/.cortex/benchmarks/`).
  `.cortex/` inside a project is for that project's eval store; the
  benchmarks cache is global and survives `cortex init`.

DELIVERABLE: a branch `feat/benchmarks-skeleton` off main with the files
above + tests + docs page + a short end-of-session report:
- Confirm the four downstream loop briefs (`02-niah.md`, `03-longmemeval.md`,
  `04-swebench.md`, `05-mteb.md`) read as still-correct against what you
  actually built. If you changed any interface name or flag, update those
  briefs in the same PR.
- Honest assessment: "The interface is X; I'd revisit it if Y."
- Any open question that downstream loops will need to answer (e.g. "I
  left the embedder choice for MTEB undecided — see open question in
  05-mteb.md").
