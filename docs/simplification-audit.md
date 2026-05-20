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
| D11 | Claude-Code slash commands and hook commands are dropped. `integrations/claude/` is gutted. Cortex is its own harness. |
| D12 | Cursor adapter (`integrations/cursor/`) is dropped completely. |
| D13 | Multi-project registry (`cortex projects` + `~/.cortex/projects.json`) is kept. |
| D14 | `watch` command + `internal/tui/` are dropped. The daemon dashboard at `:9090` and `cortex status` cover the surface. |
| D15 | L is reframed as **emit-only**: legacy cognition stays in `internal/cognition/` for now; the gate is that each mode emits `CellResult` rows. Actual DAG-native migration is deferred to a future audit. |
| D16 | E quality-assessment applies the 9 principles per scenario with rationale recorded in this doc; user reviews the verdict list at the end. |
| D17 | Dimension-6 event-stream (`cortex code --events NDJSON`) is deferred to coverage-matrix Stage 3. |
| D18 | `docs/codex-assessment.md` stays tracked (accidental include in `b7a19bd`). |
| D19 | Commits stay local on `main` until the audit is done; push happens once at the end. |

---

## Cut — locked (do now)

### A. Alternative-harness code (D1, D2) — *done (see Done)*

### B. Cross-harness comparison infrastructure (D1) — *done (see Done)*

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
- ~~**Collapse into `search`** via `--type` flag:
  - `recent`, `insights`, `entities`, `graph`~~ — *done.*
  `cortex search --type=recent|insights|entities|graph` now routes to
  helper functions; the four standalone command structs are gone.
  Net ~340 LOC delta in `cmd/cortex/commands/query.go` (mostly relocation
  into helpers + `--type` dispatch); tools.json: 35 → 31 commands.
- ~~**Collapse into `status`**: `info`, `stats`, `overview`~~ — *done.*
  Added `--expand` flag that runs a small-LLM generation of a 3-5 line
  summary, with a richer mechanical fallback when no local model is
  available.
- **Collapse into `maintenance` subcommand** (or under `daemon`):
  - `prune`, `reembed`, `embed`
- ~~**Claude-Code hook commands** — `session-start`, `inject-context`, `stop`,
  `cli`~~ — *done.* Per D11 the four hook commands plus
  `integrations/claude/` are gone; `install`/`uninstall` collapse to
  cortex-only setup (no editor / hook wiring); `cortex capture`'s
  stdin path now expects native `events.FromJSON` only.
- **Multi-project registry** — `projects`. Keep iff multi-project is still in
  scope; otherwise drop. Flag for explicit user decision.
- ~~**Watch / TUI** — `watch` + `internal/tui/`~~ — *done.* Per D14
  both are gone; daemon dashboard at `:9090` plus `cortex status`
  cover the observability surface.

Routing fix:

- ~~Standardize the `journal`-style subcommand dispatch for `eval` (promote
  `eval grid|suite|benchmark` from internal switch to real subcommands).~~
  — *done.* `cortex eval` now dispatches `grid` / `suite NAME` /
  `benchmark NAME` as positional subcommands, mirroring `cortex journal`.
  The legacy `--suite=NAME` / `--benchmark=NAME` flag forms still
  resolve (no break for in-flight scripts) but the subcommand form is
  the documented canonical surface.
- ~~Add a test that asserts every registered command resolves to a `case` in
  `main.go`~~ — *done; see Done. Both directions checked (registered →
  routed, routed → registered).*
- ~~Implement `DescribeFlags` on remaining commands so the surface is
  machine-readable (only 4 of 40 do it today).~~ — *done.* Every
  flag-bearing command now implements `DescribeFlags`. See Done.

~~`measure --calibrate` duplicates the standalone `cortex calibrate` command —
delete the flag; keep the command.~~ — *not a duplicate.* The standalone
`cortex calibrate` is **DAG cost-hint** calibration (reads
`.cortex/db/dag_traces.jsonl`, writes per-op p50 cost snapshots).
`cortex measure --calibrate` is **measure-subsystem** calibration (records
prompt→output token pairs for the prompt-quality model in
`internal/measure`). Different subsystems; keep both.

### H. Stale-doc rewrite

Archive-move portion is done (see Done). ~~Remaining: update or rewrite
the docs that are still relevant but framed around the old cross-harness
grid:~~ — *done.* All five docs reframed for cortex-only direction; see
Done > H (rewrite).

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

### M. Within-cortex baseline mechanism — *resolved (see Done)*

Decision: `ContextStrategy` is the only baseline axis. The `Condition`
enum, the `Injector` interface, and the multi-harness wrapper machinery
are gone. Frontier is just "the cortex harness with a frontier-tier
model" — a `--model` choice, not a separate code path.

### N. Observability surfaces vs. dimension 6

Dimension 6 (in-flight observability) needs an event-stream CLI surface
(`cortex code --events` NDJSON per the matrix). Decide whether `internal/web/`
(dashboard, 215 LOC, used by daemon) and `internal/tui/` (94 LOC, used by
`cortex watch`) feed that proxy. If not, both are optional and `watch` is a
candidate to drop under F.

### O. Cursor adapter — *done.* Per D12 the entire
`integrations/cursor/` directory is deleted; cortex is its own
harness. See Done.

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
4. ~~**Aider + OpenCode + pidev deletion** (A)~~ — *done.* Includes the
   follow-on slice that dropped `--harness`, the default-generic flow,
   and the legacy `Evaluator` type (~1,020 LOC).
5. ~~**Cross-harness comparison infra** (B) + **baseline mechanism** (M)~~ —
   *done.* Collapsed into the ContextStrategy axis; Injector machinery
   gone (~925 LOC).
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
    ~~b. View collapses (`recent`/`insights`/`entities`/`graph` → `search --type`).~~ — *done.*
    c. Status collapses (`info`/`stats`/`overview` → `status`).
    ~~d. Hook commands + `projects` decision (needs user input first — flagged in F).~~ — *hook commands done.* `projects` kept under D13.
11. **Unify staged DAG ops** (K) — one PR per op, each gated on no regression
    in v2 cell results.
12. ~~**Within-cortex baseline decision** (M)~~ — *resolved alongside B.*
13. **Convert legacy cognition to DAG types** (L) — six steps (Reflex →
    Reflect → Resolve → Think → Dream → Digest). Each gated on parity vs.
    legacy via cell-result evals. Final PR deletes `internal/cognition/`.
14. **Observability decision** (N) — informs whether `watch` + `internal/tui/` +
    `internal/web/` stay.
~~15. **Cursor adapter** (O) — likely follows MCP; verify and cut or keep.~~ — *done.*

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

### F.c. `status` collapse + `--expand` LLM summary

Per the design call: `cortex status` is the single inspect-state entry
point. The three sibling commands collapse into flags; a new
`--expand` flag asks a small local LLM to render a 3-5 line summary
with a mechanical fallback.

New surface:

```
cortex status                  short status-line (default, sub-ms)
cortex status --format=claude  Claude Code stdin variant (unchanged)
cortex status --system         was `cortex info`     (system + Ollama + model recs)
cortex status --memory         was `cortex overview` (memory dashboard)
cortex status --json           was `cortex stats`    (raw JSON)
cortex status --expand         small-LLM summary (Ollama, falls back to mechanical render)
```

Implementation:

- `InfoCommand`, `OverviewCommand`, `StatsCommand` deleted from
  `cmd/cortex/commands/debug.go`. Their `Execute` bodies became
  helpers (`displaySystemInfo`, `displayMemoryOverview`,
  `displayStatsJSON`) called from `StatusCommand.Execute`.
- `StatusCommand.Execute` now parses the new flags and dispatches;
  flag mutex rejects combos with a clear error. Default (no flag)
  preserves the existing fast template render.
- `--expand` path:
  - Composes a `statusContext` from system info, Ollama state, project
    init state, daemon state, storage stats, and recent activity.
  - Loads `pkg/cognition/prompts/status_summary.tmpl` (new template,
    version 1, op `status.summary`) and renders the prompt.
  - Asks an Ollama small model (auto-picks `qwen2.5:0.5b` /
    `qwen2.5-coder:0.5b` / similar from the installed set; configured
    `OllamaModel` wins when present) under an 8s timeout.
  - On LLM unavailable or error: falls through to a mechanical
    render of the same data. The data is the valuable context
    regardless of who renders it.
- Removed `info`, `stats`, `overview` from `cmd/cortex/main.go`'s
  routing case + help text. Added `cortex status` examples for the new
  flags.
- Routing test catches drift; tools.json regenerated (38 → 35 commands).

Verified `go build ./...` and `go test ./...` both green.

Deferred under L (legacy cognition → DAG): the daemon-side variant
where state changes trigger background small-LLM generation and write
the one-liner into `daemon_state.json`. That makes the default
`cortex status` content LLM-generated *without* paying the LLM cost in
the call path — implemented as a `maintain.status_summary` DAG node.

### B + M. Injector machinery deleted, Condition collapsed into ContextStrategy

Resolved M (the within-cortex baseline question) and completed B in one
pass.

Decision: under cortex-only (D1), the cross-harness Injector seam
(`Injector` interface, `NoOpInjector`, `CortexInjector`) is gone. The
"baseline / cortex / frontier" distinction collapses onto the existing
`ContextStrategy` axis from `cellresult.go`. Frontier is just "cortex
harness with a frontier-tier model" — a `--model` choice, not a code
path.

Removed (~925 LOC):

- `internal/eval/v2/library_service_inject.go` (329 LOC) — `Injector`
  interface, `NoOpInjector`, `CortexInjector`, `WithVerbose`,
  `resolveCortexBinary`, `newCortexStateDir`.
- `internal/eval/v2/library_service_inject_test.go` (596 LOC).
- `LibraryServiceCondition` type + `ConditionBaseline / Cortex / Frontier`
  constants in `library_service.go`.
- `RunWithInjector` and `RunCortexWithHarness` (no longer have a reason
  to exist).
- `LibraryServiceRun.CortexStateDir` field (no Injector → no state dir
  setup).

Renamed:

- `LibraryServiceRun.Condition` → `LibraryServiceRun.Strategy` (string;
  takes the v2 `Strategy*` constants).
- Updated `Run()` / `RunWithHarness()` / `runSessions()` /
  `setupLibraryWorkdir()` signatures to take `strategy string` instead
  of a Condition value.
- `CompareRuns` report now prints `strategy=` instead of `condition=`.

Preserved:

- `CompareRuns` tests moved to `library_service_compare_test.go` (the
  inject_test.go file held them but they aren't injector-specific).
- `ClaudeCLIHarness` is still the library-service runner's harness for
  now. Migrating library-service to drive `CortexHarness` directly
  belongs to audit E (eval migration) and would let the strategy axis
  actually drive different behavior again.

Verified `go build ./...` and `go test ./...` both green.

### A finish. `--harness` flag + default-generic flow dropped

Per the design call: cortex is implicit when `-s scenario.yaml -m model`
are present; the `--harness` flag is gone; the default-generic-eval
flow (a pure-text Q&A eval that bypassed the harness) is gone.

Surface changes in `cmd/cortex/commands/eval.go`:

- Removed flags: `--harness`, `-p/--provider`, `--compare-provider`,
  `--compare-model`, `--dry-run`.
- Removed `providerName`, `dryRun`, `harnessName`, `compareProviderName`,
  `compareModelOverride` variables.
- New dispatch (in order): `--measure` redirect → `--benchmark` →
  `--suite` → cortex coding harness (when `-s + -m` set) → helpful
  usage error.
- Removed ~340 LOC of STANDARD MODE provider setup, judge wiring,
  compare-provider wiring, evaluator construction, and post-run legacy
  aggregation.
- Removed `canonicalProviderName` (only used by the deleted flow).
- Help text rewritten to reflect cortex-only dispatch.

Internal cleanup in `internal/eval/v2/`:

- Deleted `eval.go` (682 LOC): the legacy `Evaluator` type, `cliCortex`
  subprocess wrapper, `buildPrompt`, `parseSearchResults`,
  `newScenarioCellRunID`, `truncateVerbose`, and the `New(provider)`
  constructor that powered the dropped STANDARD MODE.
- Moved `Timestamp()` to `persist.go` (next to its only caller).
- `Results` / `ScenarioResult` / `TestResult` types stayed (defined in
  `measure.go`; still used by `Persister.Persist` / `GetLatest` /
  `GetTrend` which power `--summary` and `--abr-trend`).

Net: ~1,020 LOC removed across `cmd/cortex/commands/eval.go` +
`internal/eval/v2/eval.go`. tools.json regenerated.

Verified `go build ./...` and `go test ./...` both green.

### B (slice). `--measure` mode moved → `cortex measure --self-eval`

The Promptability-vs-quality correlation eval was a self-test of the
measure subsystem, not a coding-harness eval. Moved next to the thing
it validates:

- Added `--self-eval [-s SCEN] [-d DIR]` to `cortex measure`. Routes to
  the existing `evalv2.MeasureEvaluator` with the standalone command's
  provider/model/output plumbing. Requires `-p` (the scorer needs an
  LLM to compare against).
- In `cortex eval`, the `--measure` flag now returns a redirect error
  pointing at `cortex measure --self-eval` so existing scripts get a
  clear pointer rather than a silent fall-through.
- Help text updated in both commands.
- `MeasureEvaluator` and friends stay in `internal/eval/v2/` because
  they depend on the v2 persister + scenario loaders; moving the
  package was out of scope.

Verified `go build ./...` and `go test ./...` both green; tools.json
regenerated.

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

### K (slice 5). `decide.plan` early planner stub

`decide.plan` is now registered in `defaults.go` and wired into the
turn chain immediately after `sense.prompt` (n1a). The plan node
spawns `represent.embed` regardless of plan quality — even when
the provider is missing the fallback emits a single
"complexity=moderate" subtask containing the verbatim prompt, so
the chain always reaches the rest of the cognitive ops.

Manual sanity: `cortex run --type=turn --prompt="hello"` reports
≥12 nodes; n1a (decide.plan) appears between sense.prompt and
represent.embed. All tests green; `defaults_test.go` expectation
bumped 13 → 14 ops.

### K (slice 4). `model.predict_next` between inject and coding_turn

`decide.inject` now spawns `model.predict_next` (n5a) which then
spawns `decide.coding_turn` (n6). Pure trace contribution today;
future slices can use the predictions to warm caches before
`decide.coding_turn` fires.

Manual sanity: `cortex run --type=turn --prompt="hello"` reports
11 nodes executed. All tests green.

### K (slice 3). `decide.should_capture` gating `maintain.capture`

`maintain.extract_insight` now spawns `decide.should_capture` (n7a)
with the turn content as the `event` payload. `should_capture`
inspects the event and spawns `maintain.capture` only when
`capture=true`. capture=false short-circuits the chain (no journal
write), which matches the audit's "gating" intent.

Manual sanity: `cortex run --type=turn --prompt="hello"` reports
10 nodes; should_capture (n7a) runs but the gate's fallback decides
not to capture for placeholder content, so maintain.capture does
not spawn. All tests green.

### K (slice 1+2). `value.score` and `value.detect_contradiction` wired

`attend.rerank` now spawns `value.score` (n4a) on the top reranked
candidate; `value.score` spawns `value.detect_contradiction` (n4b)
on the same candidate against the remaining priors; the contradiction
node spawns `decide.inject` (n5). Both ops are non-fatal — the chain
always continues, capturing fallback data in the trace.

Manual sanity: `cortex run --type=turn --prompt="hello"` reports
10 nodes executed (was 8). All tests green.

### F (manifest). `DescribeFlags` fill-out

`DescribeFlags` now lands on every flag-bearing registered command,
so `tools.json` carries the full per-flag surface (name, type,
default, usage). Implementations added to:

- `analyze`, `capture`, `feed`, `ingest` (in `ingest.go`)
- `calibrate`
- `code`
- `daemon`
- `eval`
- `init`, `uninstall` (in `setup.go`)
- `measure`
- `prune`
- `reembed`
- `run`
- `status` (in `debug.go`)

`forget`, `projects`, `dream-debug`, `journal`, `repl`, `tools`,
`test`, `install` either take no flags or already had `DescribeFlags`.
`tools.json` regenerated; manifest test stays green.

Verified `go build ./...` and `go test ./...` both green.

### F (routing). `eval` subcommand promotion

`cortex eval` now dispatches three positional subcommands at the top
of `Execute`, mirroring `cortex journal <sub>`:

- `cortex eval grid <args>` → `executeGrid`
- `cortex eval suite NAME` → `runSuite`
- `cortex eval benchmark NAME [opts]` → `runBenchmark`

The deprecated flag forms (`--suite=NAME`, `--benchmark=NAME`) still
resolve so scripts don't break, but help text leads with the
subcommand form. tools.json regenerated; routing test stayed green
throughout.

Verified `go build ./...` and `go test ./...` both green.

### H (rewrite). Cortex-only doc reframe

Five docs reframed under the cortex-as-its-own-harness direction:

- `README.md` — Quick Start collapses to `cortex init/daemon/code`;
  Slash Commands section gone; `Multi-Agent / CI Setup` no longer
  mentions Claude Code; CLI Commands section rebuilt to point at
  the new `cortex search --type=…` views; cursor "design-only"
  limitation removed; project structure tree drops `integrations/`.
- `docs/product.md` — full rewrite. The "Integration with Claude
  Code" section, Slash Commands section, and Claude-hook-wiring
  block all deleted. Commands Reference reflects the post-audit
  surface (no `recent`/`insights`/`entities`/`graph`/`watch`,
  no `session-start`/`inject-context`/`stop`/`cli`). Data Flow
  diagram updated to journal-first.
- `docs/learning-harness.md` — abstract + section 1 + section 3 +
  section 4 + section 5 reframed; "captures from any MCP-enabled
  client" replaced; Aider reference [13] reworked; cross-harness
  open question removed; ABR figure updated to the 0.586
  pre-DAG-protocol baseline.
- `docs/tool-surface.md` — opening paragraph reframed (cortex CLI
  is the entry point); "Current gaps" updated to point at the
  audit's `DescribeFlags` slice.
- `docs/integration-roadmap.md` — section 4 ("What Cortex actually
  is") reframed; Phase 2 MCP-server item replaced with a "deferred
  per D9" note; phase summary table updated.

Verified `go build ./...` and `go test ./...` both green.

### O. Cursor adapter removal

Per D12 `integrations/cursor/` is gone in its entirety (`adapter.go`,
`adapter_test.go`, `README.md`) — ~700 LOC removed. The cursor import
was already pruned out of `ingest.go` under F.d, so the directory
delete needed no other code changes.

Verified `go build ./...` and `go test ./...` both green.

### F.d (slice). `watch` + `internal/tui/` removal

Per D14 the `cortex watch` TUI dashboard goes — the daemon at `:9090`
plus `cortex status` cover the surface.

- Deleted `cmd/cortex/commands/watch.go`, `watch_state.go`, and their
  `_test.go` peers — ~1,500 LOC.
- Deleted `internal/tui/` (`ansi`, `box`, `panels`, `spinner`, `text`,
  `tui`, `tui_test`) — ~1,000 LOC.
- `cmd/cortex/main.go` — `watch` case removed; help text trimmed.
- `pkg/cliout/telemetry.go` — observability classifier drops `watch`.
- `tools.json` regenerated (27 → 26 commands).

Verified `go build ./...` and `go test ./...` both green.

### F.d (slice). Slash-command + hook teardown

Per D11 the Claude-Code slash + hook story is gone. Cortex is its own
harness; if anyone wants Claude Code to consume cortex memory they
should call the CLI directly.

- Deleted `cmd/cortex/commands/session.go` (the four hook commands
  `session-start`, `inject-context`, `stop`, `cli`, plus their
  helpers `tryLogError`, `isFirstPromptInSession`, `writeRetrievalStats`,
  `executeOverview`, `executeInfo`) — ~540 LOC.
- Deleted `integrations/claude/` entirely (`adapter.go` +
  `adapter_test.go`) — ~440 LOC.
- `cmd/cortex/commands/setup.go` rewritten end-to-end (~1190 → ~370 LOC).
  `InstallCommand` is now thin — ensure `.cortex/` exists, register the
  project, surface LLM availability. `UninstallCommand` only manages
  `.cortex/` data (purge requires `--purge`). All the Claude-Code-specific
  helpers (`createClaudeSettings`, `setupClaudeCode`, `createSlashCommand`,
  `createCortexCommand`, `createPluginJSON`, `removeCortexFromSettings`,
  `cleanCortexHooks`, `cortexBinPath`, `isDirEmpty`) are gone.
- `cmd/cortex/commands/ingest.go` (CaptureCommand) drops the
  `--source claude|cursor` dispatch; stdin payloads are now parsed
  via native `events.FromJSON` only. The cursor import goes too so
  O can land cleanly next.
- `cmd/cortex/main.go` — `session-start|inject-context|stop|cli` case
  block deleted; help text trimmed.
- `pkg/cliout/telemetry.go` — lifecycle classifier no longer lists
  the dropped verbs.
- `tools.json` regenerated (31 → 27 commands).

Net delta: ~2,800 LOC removed (mostly setup.go gutting + claude adapter
deletion + session.go deletion).

Verified `go build ./...` and `go test ./...` both green.

### F.b. View collapses → `cortex search --type`

`recent` / `insights` / `entities` / `graph` are gone as standalone
commands. The `SearchCommand` now takes `--type=recent|insights|entities|graph`
and dispatches to four helper functions (`displayRecentView`,
`displayInsightsView`, `displayEntitiesView`, `displayGraphView`) that
hold the same display logic the deleted `Execute` bodies did.

- `cmd/cortex/commands/query.go` — `RecentCommand`, `InsightsCommand`,
  `EntitiesCommand`, `GraphCommand` and their four `Register` calls
  removed; `SearchCommand.Execute` now dispatches on `--type`.
  `DescribeFlags` updated with the new flag.
- `cmd/cortex/main.go` — `recent|insights|entities|graph` removed from
  the routing case; help text + examples rewritten to point at
  `cortex search --type=…`.
- `cmd/cortex/commands/session.go` — the `cli`-subcommand `insights`
  branch now calls `displayInsightsView` directly (the wrapper struct
  was the only out-of-package consumer; F.d removes session.go
  entirely next).
- `pkg/cliout/telemetry.go` — `Attend` classifier no longer lists the
  removed verb names.
- `cmd/cortex/commands/manifest_test.go` — `search` flag-set
  expectation updated to include `type`.
- `tools.json` regenerated (35 → 31 commands).

Per D11 no slash-command compat is preserved.

Verified `go build ./...` and `go test ./...` both green.

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
