# Simplification Audit — Align to the Cortex Harness

> Living doc. Move items between sections as decisions land or scans return
> new evidence. This is a punch list, not a design doc.

## North star

Cortex is its own coding harness. It must cover the 10 dimensions in
[`docs/benchmarks/coverage-matrix.md`](benchmarks/coverage-matrix.md), built on:

- **Memory** — event-capture journal → DAG-driven indexing
- **Coding UX** — `cortex code` / `cortex repl` / `cortex run`
- **Background cognition** — Think and Dream as DAG types
- **Evals** — the standard suite already wrapped (SWE-bench, NIAH, LongMemEval,
  MTEB) and the integration wave (τ-bench, MINT, BFCL, AgentDojo)

Goal: a harness that gets emergent capability from both small and large models.

**Cut criterion:** anything that doesn't serve the cortex harness covering the
10 dimensions. Cross-harness comparison, alternative-harness adapters, and
external-tool probes are out of scope.

---

## Verification

This work is done when all three hold:

1. **Test suites pass.** `go test ./...` is green at the end of every PR.
2. **Evals run and produce real numbers.** No stubs, no "needs fixture seed,"
   no runners that print to stdout instead of writing `CellResult` rows.
3. **No evals left behind.** Every existing eval has been migrated to the
   shared format, quality-assessed against
   [`docs/prompts/eval-principles.md`](prompts/eval-principles.md), and
   integrated alongside SWE-bench / NIAH / LongMemEval / MTEB through the
   same runner + sink. Evals can be retired only with explicit rationale.

---

## Decisions locked in (this session)

| #   | Decision                                                                 |
| --- | ------------------------------------------------------------------------ |
| D1  | Cortex is the only harness. Aider + OpenCode + **pidev** adapters go.    |
| D2  | `--harness` flag / `Harness*` enum is removed; cortex is implicit.       |
| D3  | (rolled into D1 — pidev resolved as out)                                 |
| D4  | Focus = cortex covering the 10 coverage-matrix dimensions.               |
| D5  | All eval runners emit the unified `cell_results.jsonl` format. Convert non-v2 runners; don't delete them yet. |
| D6  | No evals left behind. Every existing eval is migrated to v2 format (or wrapped-benchmark form), quality-assessed against the 9 principles, and integrated into the same runner + sink as SWE-bench / NIAH / LongMemEval / MTEB. |
| D7  | CLI surface collapses to a smaller tiered set. ~40 commands → ~15.       |
| D8  | The 5 staged DAG ops are unified into the default chain (no separate "RegisterStaged"). |
| D9  | Drop MCP server for now. Dimension 10 (extensibility) gets revisited later. |
| D10 | Legacy `Cortex.Retrieve()` pipeline converts to DAG types; `internal/cognition/` removed once each mode has parity. |

---

## Cut — locked (do now)

### A. Alternative-harness code (D1, D2) — *partial; see Done*

Remaining for full D2 completion:

- `--harness` flag parsing in `cmd/cortex/commands/eval.go:57,72-74` —
  flag has only one meaningful value (`cortex`) now; collapse the dispatch
  so the cortex coding harness is the default when `-s` + `-m` are present.
- "CORTEX HARNESS MODE" comment and gate in `cmd/cortex/commands/eval.go:244-251`
  follow from that.

### B. Cross-harness comparison infrastructure (D1) — *partial; see Done*

Remaining:

- `internal/eval/v2/library_service_inject.go` (329 LOC) + `_test.go` (596 LOC) —
  purpose is injecting cortex context into a *different* harness's prompt;
  vestigial once cortex is the only harness. Before deleting, verify no
  within-cortex memory-on/off A/B uses the `Injector` interface. If it does,
  keep `NoOpInjector` + `CortexInjector` as a within-cortex toggle and drop
  multi-harness wiring (see M).

### D. Unify eval-runner output to `cell_results.jsonl` (D5) — *done (see Done)*

Remaining cleanup (sequenced under F): drop the env-var dispatch around the
journey runner (`CORTEX_JOURNEYS_WITH_SEED`, `CORTEX_JOURNEYS_EXECUTE`) when
the CLI surface collapse lands.

### E. Migrate every existing eval — quality-assess, integrate (D6)

No evals left behind. Each scenario below goes through three steps:

1. **Format migration** — translate to v2 YAML (or wrapped-benchmark form if
   it's a better fit) so the v2 runner / benchmark dispatcher picks it up.
2. **Quality assessment** — score the eval itself against the 9 principles in
   [`docs/prompts/eval-principles.md`](prompts/eval-principles.md): black box,
   no coaching, versioned, reproducible, isolated, structured output,
   LLM-judged variance reported, separated baselines, CLI-first gap-closing.
   Mark each as pass / improve / retire (with rationale for any retire).
3. **Integration** — runs through the same pipeline as SWE-bench / NIAH /
   LongMemEval / MTEB, writing `CellResult` rows. Surviving evals get mapped
   to the dimension they serve in
   [`docs/benchmarks/coverage-matrix.md`](benchmarks/coverage-matrix.md).

Scenarios to process:

- `test/evals/v2/` (49) — already v2-format. Sweep for quality assessment;
  most likely pass.
- `test/evals/coding/` (6) — already on the v2 runner via `coding_runner`.
  Quality-assess.
- `test/evals/library-service/` — scenarios are harness-agnostic; under
  cortex-only they run on the cortex harness. Re-attach to v2 sink; quality-
  assess.
- `test/evals/mechanic/` (5 DAG fixtures) — migrate to v2 YAML.
- `test/evals/journeys/` (10 multi-step) — migrate; sequence-of-steps may
  need a new v2 scenario shape if v2 doesn't already model it.
- `test/evals/legacy/cognition/` (22 reflex/reflect/resolve/think/dream) —
  migrate. The "needs_fixture_seed" entries either get a seed or are retired
  with rationale.
- `test/evals/corpus/` (1) — assess intent; migrate or retire with rationale.
- `test/evals/e2e/` (1) — assess intent; migrate or retire with rationale.
- `test/evals/smoke/` (1) — assess intent; migrate or retire with rationale.
- `test/evals/projects/` (11 empty subdirs) — assess original intent. If
  planned scenarios that never landed, either land them or retire the
  directory with rationale recorded.

After E + D, `internal/eval/{legacy,journey,mechanic}/` can be removed —
every scenario they ran is now under v2 (or wrapped-benchmark form), with
`CellResult` output, and the coverage-matrix mapping is explicit.

### F. CLI surface collapse (D7)

40 registered commands → target ~15. Routing is fragile (5 multi-case
switches in `cmd/cortex/main.go`). Target set:

| Tier | Command | Notes |
|---|---|---|
| harness | `cortex code` | Coding harness (keep) |
| harness | `cortex repl` | Interactive coding (keep) |
| harness | `cortex run` | Generic DAG runner (keep) |
| harness | `cortex search` | Collapses `recent`, `insights`, `entities`, `graph` via `--type` |
| memory | `cortex capture` | Record an event (keep) |
| memory | `cortex forget` | Remove an entry (keep) |
| memory | `cortex journal` | Subcommands: ingest/rebuild/replay/verify/show/tail/migrate (keep — already correct shape) |
| eval | `cortex eval` | Subcommands: `grid`, `suite`, `benchmark` — promote from internal dispatch to real subcommands |
| eval | `cortex measure` | Measurement primitives (keep) |
| eval | `cortex calibrate` | Token calibration (keep; absorb `measure --calibrate`) |
| lifecycle | `cortex daemon` | Background processor + dashboard (keep) |
| lifecycle | `cortex init` | Init `.cortex/` (keep) |
| lifecycle | `cortex install` / `uninstall` | Hook wiring (keep iff Claude-Code hooks stay) |
| lifecycle | `cortex status` | Collapses `info`, `stats`, `overview` |
| dev | `cortex tools` | Emit tools.json (keep) |
| dev | `cortex version` / `help` | Standard |

Cuts and consolidations:

- **Drop entirely**:
  - ~~`process` — back-compat for `ingest && analyze`; both still exposed~~ — *done.*
  - ~~`mcp` — per D9~~ — *done under J.*
  - ~~`feed`, `analyze` — verify whether these are subsumed by `ingest` /
    `capture`; if so, drop. Likely yes.~~ — *verified, NOT subsumed.* `analyze`
    runs post-hoc LLM analysis on already-stored events (independent of the
    queue→DB drain that `ingest` does). `feed` does manual knowledge
    seeding from files/directories (independent of `capture`'s event-based
    ingest). Keep both.
  - `search-vector` — internal benchmark utility; move under `cortex eval`
    or a hidden `_internal` namespace
  - `dream-debug` — replace with `cortex run --type=dream --debug`
  - `test` — verify what it does; if dev-only, move to `scripts/`
- **Collapse into `search`** via `--type` flag:
  - `recent`, `insights`, `entities`, `graph`
- **Collapse into `status`**:
  - `info`, `stats`, `overview`
- **Collapse into `maintenance` subcommand** (or under `daemon`):
  - `prune`, `reembed`, `embed`
- **Claude-Code hook commands** — `session-start`, `inject-context`, `stop`,
  `cli`. With cortex as its own harness, the Claude-Code integration story is
  no longer the primary path. Keep iff we still want Claude Code to consume
  cortex memory; otherwise drop. Flag for explicit user decision.
- **Multi-project registry** — `projects`. Keep iff multi-project is still in
  scope; otherwise drop. Flag for explicit user decision.
- **Watch / TUI** — `watch` + `internal/tui/`. Keep iff dimension 6 (in-flight
  observability) wants a TUI surface; otherwise it's optional UX. Coupled to
  N (Investigate).

Routing fix:

- Standardize the `journal`-style subcommand dispatch for `eval` (promote
  `eval grid|suite|benchmark` from internal switch to real subcommands).
- ~~Add a test that asserts every registered command resolves to a `case` in
  `main.go`~~ — *done; see Done. Both directions checked (registered →
  routed, routed → registered).*
- Implement `DescribeFlags` on remaining commands so the surface is
  machine-readable (only 4 of 40 do it today).

~~`measure --calibrate` duplicates the standalone `cortex calibrate` command —
delete the flag; keep the command.~~ — *not a duplicate.* The standalone
`cortex calibrate` is **DAG cost-hint** calibration (reads
`.cortex/db/dag_traces.jsonl`, writes per-op p50 cost snapshots).
`cortex measure --calibrate` is **measure-subsystem** calibration (records
prompt→output token pairs for the prompt-quality model in
`internal/measure`). Different subsystems; keep both.

### H. Stale-doc rewrite

Archive-move portion is done (see Done). Remaining: update or rewrite the
docs that are still relevant but framed around the old cross-harness grid:

- `docs/integration-roadmap.md`
- `docs/tool-surface.md`
- `docs/learning-harness.md`
- `docs/product.md`
- `README.md` — Aider mention in "Multi-Agent / CI Setup" section is stale

### I. Test plans tied to removed harnesses — *done (see Done)*

### J. Drop MCP server (D9) — *done (see Done)*

### K. Unify the 5 staged DAG ops (D8)

These ops are registered in `pkg/cognition/dag/ops/defaults.go:48-127` but
never spawned by the default turn chain. Wire each into the default chain
(do not create a separate `RegisterStaged()`):

- `value.score`
- `value.detect_contradiction`
- `decide.should_capture`
- `model.predict_next`
- `decide.plan`

Their templates in `pkg/cognition/prompts/` (`value_score.tmpl`,
`value_detect_contradiction.tmpl`, `decide_should_capture.tmpl`,
`model_predict_next.tmpl`) are already in place. Wiring is the missing
piece — figure out the right edge in `cmd/cortex/commands/run.go:buildTurnChain`
for each, ensure budget/depth caps still hold, and remove any "stage 2/3"
comments left behind.

### L. Convert legacy cognition to DAG types; remove legacy (D10)

`internal/cognition/cortex.go` runs the original Reflex→Reflect→Resolve
pipeline with Think/Dream/Digest hooks. Convert each mode to a DAG type or
op, then delete the legacy pipeline. Order:

1. **Reflex** — mechanical retrieval. Already substantially covered by
   `remember.vector_search` (existing DAG op). Verify behavior parity, then
   route `Cortex.Retrieve()` callers through DAG `turn`-type seeds.
2. **Reflect** — agentic reranking. Covered by `attend.rerank`. Wire in
   contradiction-resolution (overlaps with K's `value.detect_contradiction`).
3. **Resolve** — inject/wait/queue decision. Covered by `decide.inject`.
   Verify parity on the "wait" and "queue" paths.
4. **Think** — currently called from `Cortex.Retrieve()` for active work.
   Convert to a DAG type (`--type=think` already exists in `run.go`); ensure
   the daemon's idle/active triggers spawn DAG runs instead of calling
   `Think.MaybeThink()`.
5. **Dream** — same shape as Think but idle-budgeted. Convert similarly.
   Fractal source weighting under `internal/cognition/fractal/` stays
   load-bearing as a Dream-internal data structure; it just gets called from
   a DAG handler instead of `Dream.Run()`.
6. **Digest** — post-Dream consolidation. Either fold into the Dream DAG run
   (a `maintain.consolidate` op) or keep as a small DAG type. Either way,
   `internal/cognition/digest.go` goes.

When each mode has DAG-native parity, delete the corresponding file in
`internal/cognition/`. Final removal target: the entire `internal/cognition/`
package, except `fractal/` if still needed (verify).

Gating: every step needs an eval cell (against the existing `cell_results.jsonl`
sink) confirming parity vs. the legacy mode before that mode's legacy
implementation is removed.

---

## Investigate before cutting

### M. Within-cortex baseline mechanism

The 9 eval principles ([`docs/prompts/eval-principles.md`](prompts/eval-principles.md))
include "separated baselines." Under cortex-only, baseline = cortex without
memory injection; cortex condition = cortex with memory. Decide whether
`library_service.go`'s `Condition{Baseline,Cortex,Frontier}` survives as a
within-cortex memory-on/off toggle (in which case `NoOpInjector` +
`CortexInjector` stay) or whether baseline/frontier collapse into a `--model`
axis and the Condition enum is dropped.

### N. Observability surfaces vs. dimension 6

Dimension 6 (in-flight observability) needs an event-stream CLI surface
(`cortex code --events` NDJSON per the matrix). Decide whether `internal/web/`
(dashboard, 215 LOC, used by daemon) and `internal/tui/` (94 LOC, used by
`cortex watch`) feed that proxy. If not, both are optional and `watch` is a
candidate to drop under F.

### O. Cursor adapter

`integrations/cursor/` has a real adapter + design doc but no end-to-end
test. Same dimension-10 surface as MCP. User dropped MCP under D9; Cursor
likely follows but wasn't explicitly named. Default: cut with MCP unless
there's a near-term non-MCP use.

---

## Keep — load-bearing for the focus

Confirmed load-bearing; do not touch:

- `pkg/cognition/dag/` — the engine
- `pkg/cognition/prompts/` — DAG op templates (the consumed ones; K wires the
  remaining four)
- `pkg/cognition/dag/calibrate.go`, `rollover.go` — production paths
- `internal/harness/`, `internal/harness/dagnode/` — coding harness
- `cmd/cortex/commands/code.go`, `repl.go`, `run.go` — coding UX
- `internal/eval/v2/` — eval runner + sink writer (consolidation hub)
- `internal/eval/v2/library_service_cortex_harness.go` — the cortex harness
  being evaluated
- `internal/eval/benchmarks/{swebench,niah,longmemeval,mteb}/` — wrapped
  benchmarks already producing `CellResult` rows
- `internal/eval/dagtrace/` — DAG telemetry sink
- `internal/journal/` — event-capture truth
- `internal/storage/` — derived state, regeneratable from journal
- `internal/capture/`, `internal/processor/` — capture + projection
- `internal/cognition/` — **transitional**: load-bearing until each mode has a
  DAG-native replacement under L. Remove progressively as ops land; don't cut
  ahead of parity.
- `internal/cognition/fractal/` — Dream's source weighting (re-evaluate at L.5)
- `internal/measure/` — measurement primitives used by production and eval
- `pkg/llm/` — model providers (small + large)
- `pkg/events/`, `pkg/config/`, `pkg/registry/`, `pkg/secret/`, `pkg/system/`,
  `pkg/cliout/`, `pkg/models/` — infrastructure
- `Formula/cortex.rb` — homebrew formula
- `scripts/build-all.sh`, `install.sh`, `release.sh`, `check.sh` — release/CI
- `docker-compose.yaml`, `grafana/` — local Grafana dev setup (optional but
  current)

---

## Order of operations

Each step lands as one PR. Every PR ends green: `go test ./...` passes and
any eval touched by the step produces real `CellResult` numbers (the
Verification bar). Cuts ordered to minimize churn: trivial / no-risk first,
then aider-class deletions, then unification, then the legacy-cognition
conversion (largest, most-gated).

1. ~~**Root hygiene** (G)~~ — *done in this commit.*
2. ~~**Probe binaries and one-off CLIs** (C)~~ — *done in this commit.*
3. ~~**Stale-doc archive move** (H, partial)~~ — *done in this commit.*
   Rewrite of still-relevant docs (integration-roadmap, tool-surface,
   learning-harness, product, README) remains pending.
4. ~~**Aider + OpenCode + pidev deletion** (A, partial)~~ — *done in this
   commit.* `--harness` flag and dispatch refactor in `eval.go` is the
   remaining slice; sequenced as its own commit.
5. **Cross-harness comparison infra** (B) — depends on step 4 + M decision.
6. ~~**Drop MCP** (J)~~ — *done in this commit.*
7. ~~**Unify eval-runner output to `cell_results.jsonl`** (D)~~ — *done in
   this commit.* All three suites (mechanic, legacy-cognition, journeys)
   now write through `evalv2.PersistCell`.
8. **Migrate + quality-assess + integrate every eval** (E) — once D is in,
   walk every scenario: format-migrate, score against the 9 principles
   (pass / improve / retire-with-rationale), confirm `CellResult` output,
   map to a coverage-matrix dimension. The legacy runners come out in the
   final cleanup PR once nothing references them.
9. ~~**Test plans tied to removed harnesses** (I)~~ — *done in this commit.*
10. **CLI surface collapse** (F) — sequence as a few PRs:
    a. Routing test + back-compat-alias deletions (`process`, `mcp` already
       gone after J, `measure --calibrate`).
    b. View collapses (`recent`/`insights`/`entities`/`graph` → `search --type`).
    c. Status collapses (`info`/`stats`/`overview` → `status`).
    d. Hook commands + `projects` decision (needs user input first — flagged in F).
11. **Unify staged DAG ops** (K) — one PR per op, each gated on no regression
    in v2 cell results.
12. **Within-cortex baseline decision** (M) — author the ADR; refactor if needed.
13. **Convert legacy cognition to DAG types** (L) — six steps (Reflex →
    Reflect → Resolve → Think → Dream → Digest). Each gated on parity vs.
    legacy via cell-result evals. Final PR deletes `internal/cognition/`.
14. **Observability decision** (N) — informs whether `watch` + `internal/tui/` +
    `internal/web/` stay.
15. **Cursor adapter** (O) — likely follows MCP; verify and cut or keep.

---

## Done

### G. Root hygiene

- Deleted `tests/` directory (single empty `__init__.py`, Python skeleton).
- Deleted root probe binaries `cortex-or-probe`, `cortex-pidev-probe` from
  the working tree (already covered by `/cortex-*` in `.gitignore`).
- Added `.aider.*` to `.gitignore` for aider session artifacts.
- `tools.json` stays tracked — it's a tested frozen snapshot
  (`cmd/cortex/commands/manifest_test.go` asserts it matches the generator).
  The original audit's "it drifts" claim was wrong; the test prevents drift.
- `daemon_state.json` / `session.json` already gitignored; left in place
  (they're runtime working files).

### C. Probe binaries and one-off CLIs

- Deleted `cmd/cortex-or-probe/` — OpenRouter shape now internalized in
  `pkg/llm/openrouter.go`.
- Deleted `cmd/library-eval/` — pre-v2 research one-off, superseded by
  v2 + `internal/eval/benchmarks/`.
- Deleted `cmd/mteb-rerank-smoke/` — superseded by
  `internal/eval/benchmarks/mteb/`.
- Verified with `go build ./...` and `go test ./...` both green; no other
  package imported these mains.

### I. Test plans tied to removed harnesses

Archived 3 aider-only documents to `docs/archive/library-service/`:

- `plans/05-aider-harness.md`
- `plans/05-aider-harness-prompt.md`
- `runs/2026-05-04-qwen1.5b-aider-floor.md`

Verified the other docs the audit had flagged are *not* aider-specific:

- `plans/02-session-runner.md` is about Claude-CLI session running, not
  aider — keep.
- `runs/2026-05-04-haiku-vs-sonnet-3way.md` and `2026-05-04-haiku-hooks-active.md`
  use ClaudeCLIHarness with Cortex hooks; cortex-relevant data — keep.

### A (partial). Alternative-harness adapters deleted

Deleted ~2,471 LOC of alternative-harness code:

- `internal/eval/v2/library_service_aider_harness.go` + `_test.go`
- `internal/eval/v2/library_service_opencode_harness.go` + `_test.go`
- `internal/eval/v2/library_service_pidev_harness.go` + `_test.go`
- Constants `HarnessAider`, `HarnessOpenCode`, `HarnessPiDev` removed from
  `internal/eval/v2/cellresult.go`. Validation `switch` and harness-name
  doc comments updated.
- `cortex eval grid` no longer takes `--harnesses`; always runs the cortex
  harness. `buildGridHarnesses()` removed; help text updated.
- Test fixtures that used `HarnessAider` as a stand-in harness name
  (cellresult_test, persist_cell_test, grid_test, journal/eval_test,
  processor_test) now use `HarnessCortex` / `"cortex"`.

Verified `go build ./...` and `go test ./...` both green.

`--harness` flag in `cortex eval` (not `eval grid`) still exists — it has
only one meaningful value now (`cortex`). Removing it is the remaining
slice of A; left under Cut so the dispatch refactor is its own commit.

### D. Eval-runner output unified to `cell_results.jsonl`

All three suite runners now write `CellResult` rows through the shared
v2 persister so their output flows through the same sink as v2 +
SWE-bench / NIAH / LongMemEval / MTEB.

- `runMechanicSuite` (`cmd/cortex/commands/eval_suite.go`) — emits one
  cell per fixture. Provider=`local`, model=`mock-dag-executor`,
  strategy=`baseline`.
- `runLegacyCognitionSuite` — emits one cell per
  (scenario × mode × test). Skipped (`needs_fixture_seed`) cells go in
  with `TaskSuccess=false` and the skip reason in `Notes`.
- `runJourneysSuite` validation-only path and `--with-seed` path both
  emit one cell per scenario. The `--execute` path already had its own
  sink. With this commit all three journey modes produce real numbers.

Helper functions `persistMechanicCells`, `persistLegacyCells`,
`persistJourneyValidationCells` opened-`evalv2.NewPersister()` once per
suite invocation; persister errors are non-fatal (logged to stderr,
suite still reports).

Verified `go build ./...` and `go test ./...` both green.

### B (partial). `--agentic` mode + AgenticEvaluator dropped

User confirmed cortex is the only harness; Claude-CLI tool-usage
comparator (the "agentic" eval mode) was cross-harness infrastructure.

- Deleted `internal/eval/v2/agentic.go` (436 LOC — `AgenticEvaluator`,
  `AgenticResults`, `AgenticScenarioResult`, related helpers).
- Removed `--agentic` flag, `--claude-binary` flag, `agenticMode` /
  `claudeBinary` vars in `cmd/cortex/commands/eval.go`.
- Removed the AGENTIC MODE block (~70 LOC) in `Execute()`.
- Removed the agentic branch in the `--summary` trend path.
- Removed `Persister.PersistAgentic`, `GetAgenticTrend`,
  `AgenticTrendPoint`, and the `agentic_eval_runs` SQLite schema in
  `internal/eval/v2/persist.go`.
- Removed `ReportAgentic`, `ReportAgenticJSON`, `ReportAgenticTrend`,
  and `reductionBar` helpers in `internal/eval/v2/report.go`.
- Removed help-text lines + examples in `eval.go`.
- Regenerated `tools.json` (38 commands).

Note: `internal/measure/agentic.go` (the prompt-quality scorer's
LLM-judged "agentic" half) is **unrelated** and stays. `Agentic Benefit
Ratio` (ABR) is the project's defined metric — name preserved.

Verified `go build ./...` and `go test ./...` both green.

### F.a (slice). `process` command dropped

- Deleted `ProcessCommand` from `cmd/cortex/commands/ingest.go` (struct,
  Register, Name/Description/Execute — ~85 LOC).
- Removed `"process"` from the routing case in `cmd/cortex/main.go` and
  from the cortex-function classifier in `pkg/cliout/telemetry.go`.
- Removed the `process` help line and example in `cmd/cortex/main.go`.
- Regenerated `tools.json` (38 commands, was 39).

Also noted under F: `cortex measure --calibrate` is **not** a duplicate of
the standalone `cortex calibrate`. They calibrate different subsystems
(prompt-quality model vs. DAG op cost hints). Both kept.

### F (slice). Routing test added

`cmd/cortex/main_routing_test.go` parses `main.go` via go/ast and
extracts every `case "<name>":` arm in the routing switch. Two
assertions, both green:

- `TestEveryRegisteredCommandIsRouted` — every `commands.Register(&X{})`
  has a matching case arm. Catches "registered a new command but forgot
  to wire it" silently.
- `TestEveryRoutedCommandIsRegistered` — every routed name has a real
  registered Command struct. Catches stale cases left over after
  command deletions.

Meta commands that are inline-handled (`help`, `-h`, `--help`,
`version`) are exempt via an explicit allowlist.

### J. MCP server dropped

Per D9 — dimension 10 (extensibility) gets revisited later.

- Deleted `internal/mcp/server.go` and removed the `internal/mcp/` package.
- Deleted `cmd/cortex/commands/mcp.go` + `mcp_test.go`.
- Removed `mcp` case from `cmd/cortex/main.go` (request routing + help text).
- Removed `mcpServers` blocks from `cmd/cortex/commands/setup.go` (3 places:
  Claude settings, plugin.json, Cursor settings). `cortex install` no longer
  auto-registers cortex as an MCP server.
- Regenerated `tools.json` (39 commands, was 40).
- Verified `go build ./...` and `go test ./...` both green.

### H (archive). Stale docs moved to `docs/archive/`

Moved 11 cross-harness / phase-7 docs to `docs/archive/`:

- `phase7-cortex-regression-diagnostic.md`
- `phase7-crossharness-lift.md`
- `phase7-divergence-finding.md`
- `eval-harness-phase7-prompt.md`
- `eval-harness-loop.md`
- `eval-resume-prompt.md`
- `opencode-tiers.md`, `opencode-probe.json`
- `pidev-events.md`, `pidev-probe.json`
- `eval-findings-2026-05-10.md`

Cross-references between archived docs use bare filenames and resolve
relative to their new location. Updated the one external link in
`docs/prompts/loop-phase-d-journeys.md` to point at the archive path.

Doc rewrites for still-relevant pages remain pending — see H above.

---

## How to update this doc

- When a cut is made, move the item from "Cut" to a "## Done" section with
  the PR link.
- When investigation resolves an item under "Investigate," promote it to
  "Cut" or move it to "Keep" with the reason.
- New simplification candidates from future scans go under "Cut — locked"
  with a confidence note, or "Investigate" if uncertain.
- The "Keep" section should shrink as items become obviously load-bearing
  and stop being interesting to track.
