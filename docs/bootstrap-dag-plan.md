# Project-Bootstrap DAG (`cortex bootstrap`)

## Context

Cortex's `dream.insight` pipeline is reactive — it only fires when the daemon catches idle time, and only on whatever `DreamSource` happens to sample next. A fresh project that has never been "dreamed about" arrives at the REPL with zero indexed context, so turn 1 has nothing useful to retrieve.

This change adds a **project-bootstrap DAG** that runs once on first REPL invocation (and via a standalone `cortex bootstrap` command). It scans the project under principled security rules, samples chunks via a hierarchical fractal sampler, feeds each chunk through an LLM op (A/B between `maintain.extract_insight` and a new `maintain.extract_overview` — see §A/B), and writes `dream.insight` entries to the journal — looping until both coverage signals satisfy the target or the iteration budget is exhausted.

The bootstrap is **language-agnostic by default** (Tier 1: filesystem adjacency + fixed-window line chunking). A `BoundaryAnalyzer` interface leaves room for Tier 2 (regex import detection per language) and Tier 3 (`go/ast`, tree-sitter) without disturbing the controller, sampler, or DAG ops.

Bootstrap also seeds Slice 3 of the salience-budgets work: once `BootstrapState.CompletedAt != nil`, the SystemBoundaries fact-sheet read by self-aware `decide.next` grows a line ("Project bootstrap: complete (N insights, M% coverage; query via remember.vector_search)") so turn-1 planning can target retrieval instead of fanning out to read README.

User-locked decisions (from clarifying conversation + PR feedback):
- Sampler: **hierarchical multi-scale** (behind a `Sampler` interface; Lévy / RWR are future).
- Coverage metric: **80% of effective LOC + ≥80% of files with ≥1 chunk extracted** (both must satisfy to halt with "target").
- Chunk window size: **knob, default 400 lines / 40 overlap** — surfaced via `Config`, destined for calibration loop ownership per-(language, intent).
- Journal entries: **reuse `dream.insight`** (no new entry class).
- Extract op: **A/B `maintain.extract_insight` vs new `maintain.extract_overview`** before committing — picked winner becomes the controller default; `ExtractOp="auto"` routes per-language if the result is mixed.
- Run mode on first REPL: **background, status visible** via transient REPL banners.
- Surface: **both** standalone `cortex bootstrap` subcommand **and** auto-invocation from REPL on first run.

## Approach

### Knobs (calibration-owned eventually)

`Config.WindowLines`, `Config.WindowOverlap`, and `Config.ExtractOp` are exposed at the API boundary rather than baked into the analyzer. Defaults are sensible (400/40/auto), but the moment we compare quality across languages we'll want to tune per-(language, intent) — so the knobs ship on day one.

### Determinism contract (constraint, not aspiration)

These rules are binding for every file in `internal/bootstrap/`:

- **No `time.Now()` in the seed-derived path.** `RNGSeed` comes solely from `StateHash + cfg.Salt`.
- **No map iteration in any sampler hot path.** Sort to `[]string` (or sorted slice of structs) before any weighted draw.
- **`bufio.Scanner` default `ScanLines`** for line counting (handles CRLF + missing final newline).
- **`filepath.WalkDir` results are re-sorted lexically** before any output (defensive — `WalkDir` already does this, but downstream consumers should not depend on Go's implementation detail).
- Test fixtures MUST include a CRLF file, a no-final-newline file, and a file with mixed tabs/spaces, to lock the line-counting contract.

Determinism is verified by `internal/bootstrap/determinism_test.go`: run the full controller twice on the same fixture with the same seed; assert byte-identical `BootstrapState` and journal output (modulo timestamps which are masked in the comparator).

### New packages

**`internal/projectscan/`** — lifted from `internal/cognition/sources/project.go` (lines 391–674). Holds the `.gitignore` parser, `IsHardExcludedFile`/`IsHardExcludedDir`, extension blacklist, and matchers. Pure filesystem logic with no `ProjectSource` state dependency. `ProjectSource` is rewritten to embed `*projectscan.IgnoreSet`; public API unchanged.

**`internal/bootstrap/`** — the new controller + types + sampler + analyzer. Files:
- `types.go` — `Chunk`, `Edge`, `Module`, `BoundaryOutput`, `BoundaryAnalyzer`, `Sampler`, `BootstrapState`, `Config`.
- `analyzer_universal.go` — `UniversalAnalyzer` (Tier 1).
- `sampler_hierarchical.go` — `HierarchicalSampler`.
- `controller.go` — `BootstrapController.Run`, halt logic, state persistence, banner emission.
- `coverage.go` — effective-LOC accounting + comment heuristic.
- `extract_router.go` — `chooseExtractOp(chunk) → opName` for `ExtractOp="auto"` routing.
- `pidlock.go` — flock-backed `.cortex/bootstrap.pid` race protection.

### Core types

```go
// internal/bootstrap/types.go
type Chunk struct {
    ID         string  // sha256(relpath + ":" + line_start + ":" + line_end)[:16]
    Path       string
    RelPath    string
    LineStart  int     // 1-indexed
    LineEnd    int     // inclusive
    ByteOffset int64   // for fractal.ReadRegion
    ByteLength int
    EffLines   int     // non-blank + non-comment lines in chunk
    EstTokens  int     // chars / 4
    ModuleID   string
    Lang       string  // extension-derived hint
}

type BoundaryOutput struct {
    ProjectRoot   string
    Modules       []Module
    Chunks        []Chunk
    Edges         []Edge   // Tier 1: fs_dir only
    TotalLines    int      // raw line count (kept for diagnostics)
    EffTotalLines int      // primary-signal denominator
    TotalFiles    int      // secondary-signal denominator
    RNGSeed       int64
    StateHash     string   // sha256(sorted "relpath:size:mtime_unix")
}

type Config struct {
    ProjectRoot     string
    ContextDir      string  // .cortex/
    Provider        llm.Provider
    Storage         *storage.Storage
    TargetCoverage  float64 // default 0.80; applied to BOTH signals
    BudgetMax       int     // default 200 iterations
    BatchSize       int     // chunks per iteration, default 4
    WindowLines     int     // default 400
    WindowOverlap   int     // default 40
    ExtractOp       string  // "auto" (default) | "extract_insight" | "extract_overview"
    Salt            string  // optional, mixed into seed
    DryRun          bool
    Banner          func(string)
}

type BootstrapState struct {
    Version          int
    ProjectRoot      string
    StateHash        string
    RNGSeed          int64
    TargetCoverage   float64
    BudgetMax        int
    BatchSize        int
    StartedAt        time.Time
    CompletedAt      *time.Time
    Iteration        int
    CoveredChunkIDs  []string         // sorted on disk; rehydrated to map in memory
    CoveredEffLines  int              // primary numerator
    EffTotalLines    int              // primary denominator
    CoveredFiles     int              // secondary numerator
    TotalFiles       int              // secondary denominator
    InsightsEmitted  int
    ExtractOpUsed    map[string]int   // {"extract_insight": N, "extract_overview": M}
    Halted           string           // "" | "target" | "budget_loc" | "budget_files" | "canceled" | "error"
}
```

### `UniversalAnalyzer` (Tier 1)

`filepath.WalkDir` deterministically (lexical, defensively re-sorted). For each non-excluded text file ≤1MB:

- Count lines via `bufio.Scanner` (default `ScanLines`).
- Materialize chunks as `cfg.WindowLines`-line windows with `cfg.WindowOverlap`-line overlap, recording `ByteOffset`+`ByteLength` from a small newline-position index built during the line count.
- For each chunk, compute `EffLines` via per-language comment heuristic (see `coverage.go`).

**Module-marker rule (pinned for monorepo cases):**

Module = **nearest-ancestor directory** containing one of the marker files below. **Nearest-ancestor always wins.** Precedence between markers breaks ties **only when two markers fire at the same depth**. A nested `go.mod` at depth 4 wins over a root `package.json` at depth 0 for any file under the go.mod's tree.

Markers, in tie-break precedence order:
1. **Language-root markers**: `go.mod`, `package.json`, `pyproject.toml`, `setup.py`, `Cargo.toml`, `pom.xml`, `Gemfile`, `composer.json`, `mix.exs`, `pubspec.yaml`.
2. **Build-helper markers**: `Makefile`, `CMakeLists.txt`, `build.gradle`.

If no marker fires within the project root, the file's top-level directory under root becomes its module.

`RNGSeed = fnv64(StateHash + cfg.Salt)`. Edges (Tier 1): `fs_dir` between sibling and parent/child modules, weight 1.0.

**Cache invalidation policy** (boundary cache vs. file edits): each iteration's `sense.scan_project_boundaries` recomputes the freshly-stat `StateHash`. On mismatch with the cached value, the analyzer re-runs and the controller merges coverage by chunk ID — chunks with surviving `(path, line_start, line_end)` keep their `covered` flag; orphaned chunks are dropped. Cache key is `boundaries-<state_hash>-<window>-<overlap>.json` so changing knobs mid-run invalidates cleanly.

### Effective-LOC + comment heuristic (`coverage.go`)

Comment-line detection is per-language extension dispatch. Comments + blank lines are excluded from `EffLines`.

- `.go|.js|.ts|.jsx|.tsx|.rs|.java|.c|.cc|.cpp|.h|.hpp|.cs|.swift|.kt|.scala`: lines starting with `//` (after trim) OR inside `/* ... */` blocks (stateful scan).
- `.py|.rb|.sh|.bash|.zsh|.toml|.yaml|.yml|.conf|.ini|.tf`: lines starting with `#`.
- `.lua|.sql|.hs`: lines starting with `--`.
- `.md|.txt|.rst|<no-extension>`: no comment filter.
- Unknown extensions: non-blank-only fallback.

Rationale: 80% raw-LOC may include license headers + comment blocks; stopping there can leave 60% of meaningful code uncovered. Effective-LOC fixes the metric; the file-coverage secondary signal fixes the orthogonal "uniform spread" gap.

### `HierarchicalSampler` (day-one)

```go
type HierarchicalSampler struct {
    AntiCoverageWeight float64 // default 3.0
    SizeWeightExp      float64 // default 0.5
}
```

Per `Next(out, covered, k, rng)`:
1. Module-level weighted draw: `weight_m = pow(uncovered_eff_lines_m, SizeWeightExp) * (1 + AntiCoverageWeight * uncovered_fraction_m)`. Modules with `uncovered_eff_lines == 0` get weight 0.
2. Chunk-level uniform draw among module's uncovered chunks.
3. Repeat K times without replacement within this call; recompute module weights between picks.
4. Convert maps to sorted slices before weighted draw (see Determinism contract).

### New DAG ops

**`sense.scan_project_boundaries`** (`pkg/cognition/dag/ops/sense_scan_project_boundaries.go`):
- Inputs: `project_root` (string, required), `ignore_state_hash` (string, optional), `window_lines` (int), `window_overlap` (int).
- Outputs: `boundary_output`, `total_lines`, `eff_total_lines`, `chunk_count`, `module_count`, `cached` (bool).
- Cost: `dag.Cost{LatencyMS: 2000, Tokens: 0}`.
- Function: `dag.FuncSense`. NOT `Exposable`.
- Caches to `.cortex/bootstrap/boundaries-<state_hash>-<window>-<overlap>.json`.

**`attend.fractal_sample`** (`pkg/cognition/dag/ops/attend_fractal_sample.go`):
- Inputs: `boundary_output`, `covered` (`map[string]bool`), `k` (default 4), `rng_seed` (int64).
- Outputs: `chunk_ids` (`[]string`), `chunks` (hydrated `[]Chunk`).
- Cost: `dag.Cost{LatencyMS: 50, Tokens: 0}`.
- Function: `dag.FuncAttend`. NOT `Exposable`.

**`maintain.extract_overview`** (`pkg/cognition/dag/ops/maintain_extract_overview.go`) — **new, bootstrap-targeted:**
- Inputs: `content` (string, required), `source` (string, optional), `lang_hint` (string, optional), `file_role_hint` (string, optional).
- Outputs: `overview` (`Overview{Role, Exports[], Dependencies[], Summary, Importance}`).
- Cost hint: matches `extract_insight` (similar prompt + token shape).
- Function: `dag.FuncMaintain`. NOT `Exposable`.
- Prompt (`pkg/cognition/prompts/maintain_extract_overview.tmpl`):

  > "Read this file (or section) and answer: (1) What is its job in one sentence? (2) What does it expose to other parts of the project (public names, routes, schemas)? (3) What does it depend on (imports, services, files)? (4) Rate importance for a project-overview audience (0.0–1.0). Reply JSON only: `{role, exports, dependencies, summary, importance}`."

- Journal mapping (overview → `DreamInsightPayload`):
  - `Content` = `summary`
  - `Category` = `"overview:" + role`
  - `Tags` = sorted union of `exports`, `dependencies`, plus literal `"bootstrap"`
  - `Importance` = `int(importance * 10)`
  - `SourceName` = `"bootstrap"`; `SourceItemID` = `"bootstrap:" + relpath + ":" + chunk_id`

**`maintain.extract_insight`** — reuse as-is. Controller wraps each chunk's body as `content`; `source = "bootstrap:" + relpath + ":" + chunk_id`.

**No `value.coverage_check` op** — coverage check is `float64 >= float64`; making it an op adds trace noise for no win. Keep it a controller method.

### A/B: extract_insight vs extract_overview (BLOCKS controller integration)

`maintain.extract_insight` is calibrated for **session-event extraction** — conversation logs, tool results, corrections. Pointing it at a 400-line source file asks it to do a different job (architectural summary) than its prompt was tuned for. The bootstrap-specific `maintain.extract_overview` exists to match the bootstrap intent.

The A/B is a **hard gate** before step 8 (controller integration). No controller code lands until the eval has produced a recorded decision in `docs/eval-journal.md`.

- **Sample**: 12 chunks — 4 Cortex (Go) + 4 Python project + 4 TS project. Mix of source / config / test / doc.
- **Scoring rubric** (manual, two-pass):
  - Relevance to "what is this file?" → 0=irrelevant, 1=partial, 2=full.
  - Cost: prompt + completion tokens.
  - Stability: rerun at temp=0 three times, count unique outputs.
- **Decision rule**:
  - Overview wins or ties on quality at ≤1.2× cost → default = `extract_overview`.
  - Insight wins → default = `extract_insight`; `extract_overview` stays registered but unused.
  - Mixed by file-type → `ExtractOp="auto"` routes per-extension.
- Eval lives in `internal/bootstrap/extract_ab_test.go` (env-gated; not in CI). `-update` flag refreshes the panel.
- Decision + rationale + raw scores appended to `docs/eval-journal.md`.

### Controller loop

```go
// internal/bootstrap/controller.go
func (c *BootstrapController) Run(ctx context.Context) error {
    // build registry: RegisterDefaults + Scan + FractalSample + ExtractOverview
    // open dagtrace writer; new executor; acquire pidlock
    for !c.shouldHalt() {
        if err := ctx.Err(); err != nil { return err }
        chunks, err := c.iterate(ctx) // sense.scan + attend.fractal_sample
        if err != nil { return err }
        for _, ch := range chunks {
            body, err := fractal.ReadRegion(ch.Path, ch.ByteOffset, ch.ByteLength)
            if err != nil || strings.TrimSpace(body) == "" {
                c.markCovered(ch)
                continue
            }
            opName := c.chooseExtractOp(ch) // honors cfg.ExtractOp + auto-route
            insights := c.extract(ctx, opName, ch, body)
            for _, ins := range insights {
                c.emitDreamInsight(ch, ins)
                c.state.InsightsEmitted++
            }
            c.markCovered(ch)
        }
        c.state.Iteration++
        c.persistState()
        c.maybeBanner()
    }
    c.finalize()
    return nil
}
```

**Halt criteria (both AND'd):**

```go
func (c *BootstrapController) shouldHalt() (bool, string) {
    if c.state.Iteration >= c.cfg.BudgetMax {
        locFrac  := float64(c.state.CoveredEffLines)/float64(max(1, c.state.EffTotalLines))
        fileFrac := float64(c.state.CoveredFiles)/float64(max(1, c.state.TotalFiles))
        if locFrac < fileFrac { return true, "budget_loc" }
        return true, "budget_files"
    }
    if c.ctxErr() != nil { return true, "canceled" }
    locOK  := float64(c.state.CoveredEffLines)/float64(max(1, c.state.EffTotalLines))  >= c.cfg.TargetCoverage
    fileOK := float64(c.state.CoveredFiles)/float64(max(1, c.state.TotalFiles))         >= c.cfg.TargetCoverage
    if locOK && fileOK { return true, "target" }
    return false, ""
}
```

First iteration writes a meta `dream.insight` describing the bootstrap run (seed, sampler name, window knobs, chunk + line totals, ExtractOp choice) so the journal alone is replay-sufficient.

### State persistence + first-run sentinel

`.cortex/bootstrap_state.json`. Atomic write via temp + rename. Cadence: after each iteration. Crash recovery: on next start, load state, verify `StateHash` matches a fresh scan; on mismatch, log warning and start fresh.

This file doubles as the **first-run sentinel** — its existence with non-nil `CompletedAt` means "done." No separate `.cortex/meta.json` needed.

### Race protection (`pidlock.go`)

`.cortex/bootstrap.pid` with `syscall.Flock(LOCK_EX | LOCK_NB)` at controller start. If lock acquisition fails, controller logs `"bootstrap already running (pid <N>); skipping"` and returns nil (not an error — concurrent invocation is expected when REPL spawns hit a manual `cortex bootstrap`). PID file removed in `finalize()` (even on error path via `defer`).

Integration test `pidlock_test.go` spawns two goroutines into the same `.cortex/`; asserts exactly one acquires, the other returns nil with no journal writes.

### `cortex bootstrap` subcommand

New file: `cmd/cortex/commands/bootstrap.go`. Flags:
- `--force` — re-run even if completed.
- `--budget` (int, default 200).
- `--target-coverage` (float, default 0.80) — applied to BOTH signals.
- `--batch` (int, default 4).
- `--window-lines` (int, default 400).
- `--window-overlap` (int, default 40).
- `--extract-op` (string, default "auto") — `auto` | `extract_insight` | `extract_overview`.
- `--salt` (string).
- `--background` — detach; writes pid to `.cortex/bootstrap.pid`.
- `--dry-run` — sampler + analyzer only; skip LLM + journal.
- `--provider` — inherits same resolution as other commands.

Routing in `cmd/cortex/main.go`: add `"bootstrap"` to the same `case` arm as `init`/`install`/`uninstall`/`projects` (lines 91–94).

### REPL first-run hook

In `cmd/cortex/commands/repl.go` after `newREPLState` returns (~line 569) and before `printREPLBanner(state)` at line 319:

```go
if run, why := shouldRunBootstrap(filepath.Join(state.workdir, ".cortex")); run {
    state.bootstrapCtx, state.bootstrapCancel = context.WithCancel(context.Background())
    go bootstrap.RunInBackground(state.bootstrapCtx, bootstrap.Config{
        ProjectRoot:    state.workdir,
        ContextDir:     filepath.Join(state.workdir, ".cortex"),
        Provider:       state.cortex.Provider(),
        Storage:        state.store,
        TargetCoverage: 0.80,
        BudgetMax:      200,
        BatchSize:      4,
        WindowLines:    400,
        WindowOverlap:  40,
        ExtractOp:      "auto",
        Banner:         state.emitTransientBanner,
    })
    fmt.Printf("cortex: bootstrap started (%s) — progress surfaces as banners\n", why)
}
```

Add `bootstrapCtx context.Context` + `bootstrapCancel context.CancelFunc` fields on `replState`; cancel on `state.close()`.

Banner emitter (clears the `~ ` prompt line, prints, reprints prompt):

```go
func (s *replState) emitTransientBanner(line string) {
    fmt.Printf("\r\x1b[2K[bootstrap] %s\n~ ", line)
}
```

Milestones: every 10% gain on either coverage signal; halt; error. NOT every chunk.

### SystemBoundaries dovetail (Slice 3 of salience-budgets)

Once `BootstrapState.CompletedAt != nil`, the REPL session boot path appends to the SystemBoundaries fact-sheet:

```
Project bootstrap: complete (N insights, X% effective-LOC, Y% file-coverage; query via remember.vector_search).
```

One-line addition once `BootstrapState` is loadable from the session boot path. Self-aware `decide.next` reads this and can plan retrieval-against-store rather than fan-out file reads on turn 1. (Locate the SystemBoundaries construction site during step 11 — likely under `internal/cognition/` or the Slice 2 decide.next module.)

### Sensitive-file deny list — three layers of defense

The existing list in `internal/cognition/sources/project.go:427–478` covers obvious cases. The lift extends it with layered defense rather than relying on naming conventions alone:

**Layer 1 — Name-based (extends existing):**
- Any basename matching `(?i).*(secret|credential|token).*` unless ends in `.example|.template|.sample`.
- Explicit prefixes: `.aws/credentials`, `.kube/config`, `.gnupg/`, `.ssh/`, `.config/gcloud/`.
- Production-variant dotenv: `.env.production.local`, `.env.staging.local`, `.env.*.local`.
- Service account JSON: `(?i)service-account.*\.json`, `(?i)gcp.*key.*\.json`.

**Layer 2 — Extension regex blacklist:**
- `(?i)\.(pem|key|p12|pfx|kdbx|gpg|asc|pgp|enc|jks|keystore)$`. Compiled once at `IgnoreSet` construction.

**Layer 3 — Magic-byte content sniff:**
- For files passing layers 1+2, open and read first 200 bytes.
- If `-----BEGIN` substring present (case-insensitive), reject regardless of name/extension.
- Covers PGP private keys, RSA/EC/DSA keys, X.509 certs, encrypted blobs that slip past naming conventions.
- Cost: one syscall + 200-byte read per file at scan time.

`AssertLocalOnly` does NOT save us — once chunk bytes reach the LLM, they've crossed the journal-path boundary it guards. Filter at scan source, with multiple layers. The deny list is destined to drift; the magic-byte sniff is the durable layer.

Document the policy in `internal/projectscan/doc.go`. Each layer has a dedicated regression test (see Verification).

### Cost projection (Cortex repo, ~50K LOC, default window 400)

- 50K / 400 = 125 chunks; 80% × 125 = 100 extract calls.
- Calibrated cost: 18s wall, 400 tokens per call (`maintain_extract_insight.go:61`).
- Sequential: ~30 min. Batch=4 with parallel executor: ~7–8 min.
- Tokens: ~40K total. Ollama free; Haiku single-digit cents; larger models more.
- Bootstrap inherits the REPL's configured provider — no surprise switch.

Per-call cost is bounded; total cost scales with project size. Document in `cortex bootstrap --help` so users understand what `--target-coverage=0.9` does to wall time.

## Critical files

**Touch sites (new + modified):**
- `internal/projectscan/` *(new package — lifted from `internal/cognition/sources/project.go:391-674`)*
- `internal/bootstrap/` *(new package)*
  - `types.go`, `analyzer_universal.go`, `sampler_hierarchical.go`, `controller.go`, `state.go`, `coverage.go`, `extract_router.go`, `pidlock.go`
- `pkg/cognition/dag/ops/sense_scan_project_boundaries.go` *(new)*
- `pkg/cognition/dag/ops/attend_fractal_sample.go` *(new)*
- `pkg/cognition/dag/ops/maintain_extract_overview.go` *(new)*
- `pkg/cognition/prompts/maintain_extract_overview.tmpl` *(new)*
- `cmd/cortex/commands/bootstrap.go` *(new)*
- `cmd/cortex/main.go` — add `"bootstrap"` to switch arm at lines 91–94.
- `cmd/cortex/commands/repl.go` — add first-run check + goroutine spawn after line 569; add `bootstrapCtx`/`bootstrapCancel` fields; add `emitTransientBanner` method; cancel on close.
- `internal/cognition/sources/project.go` — rewrite to embed `*projectscan.IgnoreSet`; preserve public API used by **both** Dream sampling AND the `observation.project_file` observer (find observer path during step 1; run its tests too).
- *(step 11)* SystemBoundaries fact-sheet construction site — one-line append after BootstrapState is loadable.

**Read-only references (reused as-is):**
- `pkg/cognition/dag/ops/maintain_extract_insight.go:37–54` — existing op, called per chunk under A/B.
- `pkg/cognition/dag/ops/defaults.go` — `RegisterDefaults` pattern for new ops.
- `pkg/cognition/dag/executor.go` + `registry.go` + `budget.go` — DAG plumbing.
- `internal/cognition/fractal/regions.go` — `ReadRegion(path, offset, length)` for chunk bytes.
- `internal/journal/dream.go:24–67` — `DreamInsightPayload` + `NewDreamInsightEntry`.
- `internal/journal/writer.go:151–204` — `Writer.Append`.
- `internal/journal/privacy.go:27–37` — `AssertLocalOnly` (call on every new journal write path).
- `internal/eval/dagtrace/writer.go` — trace callback for `.cortex/db/dag_traces.jsonl`.

## Implementation sequence

Each step independently reviewable. Dependencies between steps are explicit.

1. **Lift `projectscan`** (no behavior change). PR landing requires:
   - Sensitive-file defense layers 1+2+3 implemented in the lifted code (name regex + extension regex + magic-byte sniff).
   - Regression tests pass for **both** call paths: `ProjectSource` (Dream sampling) AND the `observation.project_file` observer.
   - Per-layer unit tests (see Verification — sensitive-filter tests).
2. **Add `internal/bootstrap/` types + `UniversalAnalyzer`** with unit tests. Knobs (`WindowLines`, `WindowOverlap`) wired through `Config`. Determinism contract enforced (CRLF + missing-newline + tabs/spaces in fixtures).
3. **Add `internal/bootstrap/coverage.go`** with effective-LOC + per-language comment heuristic. Unit-test per-extension behavior.
4. **Add `HierarchicalSampler`** with determinism + anti-coverage unit tests.
5. **Add `sense.scan_project_boundaries` + `attend.fractal_sample` ops** with unit tests against analyzer/sampler.
6. **Add `maintain.extract_overview` op** + prompt template + parser. Unit tests on canned model output. Both ops register; controller can call either.
7. **Run extract A/B eval** (`internal/bootstrap/extract_ab_test.go`) on the 12-chunk panel. Record decision + rationale + raw scores in `docs/eval-journal.md`. **This step BLOCKS step 8.**
8. **Add `BootstrapController.Run`** + `pidlock.go` + integration test (mock LLM, small fixture project). Use the A/B-decided default for `ExtractOp`. Race-protection integration test spawns two goroutines and asserts exactly one writes.
9. **Add `cortex bootstrap` subcommand** + main.go routing. Manual dogfood on Cortex itself with both `--extract-op` settings; verify `cortex ingest` post-bootstrap produces storage row count ≥ journal entry count.
10. **Wire REPL first-run detection** + banner. Manual: delete state file, run `cortex`, confirm async behavior + that two parallel `cortex` invocations result in one bootstrap run.
11. **Polish + dovetails**:
    - `--force`, `--background` + detached pid file.
    - **SystemBoundaries integration**: append the bootstrap-status line after `BootstrapState.CompletedAt != nil` is loadable from session boot.
    - Doc updates: `docs/bootstrap-dag-plan.md` (this file becomes the spec — first action post-approval), brief cross-reference in `docs/dag-build-plan.md` and `docs/eval-strategy.md`.

## Verification

**Unit tests:**
- `internal/bootstrap/sampler_hierarchical_test.go` — same seed → same chunk-ID sequence over 50 `Next()` calls; module-0 fully covered → never re-drawn.
- `internal/bootstrap/analyzer_universal_test.go` — `testdata/` fixture: `.env` (excluded), gitignored `tmp/`, two module markers at different depths (`go.mod` at root + nested `package.json` at depth 2 — nearest-ancestor wins), a `Makefile` next to a `go.mod` at the same depth (language-root precedence wins), 50-line / 1000-line / CRLF / no-final-newline / mixed-indent files. Assert: sensitive paths absent, modules assigned per depth-then-precedence rule, total + effective line counts correct, `StateHash` deterministic, knob-driven window sizes honored (rerun with `WindowLines=200` and check chunk count doubles).
- `internal/bootstrap/coverage_test.go` — per-extension comment heuristic: Go `//` + `/* */`, Python `#`, Markdown no-filter, unknown-ext non-blank-only.
- `internal/bootstrap/determinism_test.go` — full controller twice on same fixture + seed → byte-identical `BootstrapState` (modulo masked timestamps) and journal output.
- `internal/bootstrap/pidlock_test.go` — two goroutines into same `.cortex/`; exactly one acquires; second returns nil with no journal writes.
- `internal/bootstrap/extract_router_test.go` — `ExtractOp="auto"` routes per-extension correctly; explicit settings honored.
- `internal/projectscan/ignore_test.go` — port existing `.gitignore` tests from `internal/cognition/sources/_test.go`. **Plus three dedicated sensitive-filter regression tests:**
  - Layer 1: file named `okay.txt` containing nothing sensitive → allowed; file named `aws-credentials.txt` → rejected; file named `secret.example` → allowed (template exemption).
  - Layer 2: file named `mykey.pem` with text content → rejected by extension regex.
  - Layer 3: file named `notes.txt` whose first bytes are `-----BEGIN RSA PRIVATE KEY-----\n...` → rejected by magic-byte sniff.

**A/B eval test:**
- `internal/bootstrap/extract_ab_test.go` (env-gated; not in CI) — runs both ops on the panel, prints scoring table; `-update` regenerates the panel. Output appended to `docs/eval-journal.md` via a make target.

**Integration tests:**
- `internal/bootstrap/controller_integration_test.go` — `testdata/sample_project/` (10 files mixed `.go`/`.py`/`.md`, ~200 effective lines), mock LLM. Run `TargetCoverage=0.5, BudgetMax=20, BatchSize=2`. Assert: `CompletedAt != nil`, `Halted == "target"`, both signals ≥ 50%, ≥1 `dream.insight` entry in `.cortex/journal/dream/`, re-run is a no-op without `--force`.
- `internal/bootstrap/ingest_keepup_test.go` — after a controller run, invoke `cortex journal ingest` (or its in-process equivalent) and assert `storage.InsightCount() >= journalInsightCount`. Locks the journal-volume contract.

**Manual dogfood (Cortex itself):**

```bash
go build -o bin/cortex ./cmd/cortex
./bin/cortex bootstrap --target-coverage=0.2 --budget=30 --batch=4 --salt=dogfood --extract-op=extract_overview
# expect: state.json populated, both signals ≥20%, dream insights in journal
cat .cortex/journal/dream/*.jsonl \
  | jq 'select((.payload|fromjson).source_name == "bootstrap") | (.payload|fromjson)' | head
./bin/cortex            # bare REPL should NOT re-run (sentinel present)
./bin/cortex bootstrap --force --extract-op=extract_insight   # rerun with the other op
```

## Residual risks (not fully mitigated by the plan)

Only one item is genuinely residual — every other concern is addressed as a plan requirement above.

1. **Sensitive-file pattern drift.** The three-layer defense (name regex + extension regex + magic-byte sniff) catches the obvious shapes plus the `-----BEGIN` family. New exfil patterns will ship over time (e.g., new key formats without the BEGIN preamble, vendor-specific config files). The magic-byte sniff is the durable layer but cannot detect every secret. **Ongoing mitigation**: track newly-observed sensitive patterns in `docs/eval-journal.md`; quarterly review of `internal/projectscan/sensitive.go`; consider entropy-based detection (Shannon entropy > N on first 1KB) in a later iteration if drift becomes acute.
