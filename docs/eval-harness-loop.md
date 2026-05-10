# Eval-Harness Loop Prompt

> **You are the eval-harness builder.** This file is your entire context — read
> it fresh every tick. Do **one** TODO, commit, then either schedule the next
> tick (60s) or stop on a stop-condition.

---

## Goal

Wire cortex's existing v2 eval framework so every scenario can be evaluated
across a grid of:

| Dimension | Values |
|---|---|
| **Harness** | `aider` · `opencode` · `pi_dev` · `claude_cli` (existing) |
| **Provider** | `openrouter` (new) · `ollama` · `anthropic` (existing) |
| **Model tier** | small · medium · large (configured per provider) |
| **ContextStrategy** | `baseline` · `cortex` · `frontier` |

Each grid cell emits exactly one `eval.CellResult` (defined in
`internal/eval/v2/cellresult.go`). Aggregations (ABR, lift, cost-per-success)
are downstream of those rows.

The thesis being measured: **`(small_model + cortex)` reaches the quality of
`(large_model + baseline)` at lower `cost_usd`** — the small-model amplifier.

---

## Hard constraints (do not violate)

1. **`internal/eval/v2/cellresult.go` is a contract.** Do not rename JSON
   tags, remove fields, or reorder enum constants without explicit user
   signoff. Adding a new optional field with `omitempty` is allowed and keeps
   `SchemaVersion = "1"`. Anything else: stop and ask.

2. **Never log `OPEN_ROUTER_API_KEY`, `ANTHROPIC_API_KEY`, or any other
   secret.** Redact in error messages too. (Note: this project uses
   `OPEN_ROUTER_API_KEY` with the underscore form — that's the user's
   actual env-var name. Aider/litellm internally expects the canonical
   `OPENROUTER_API_KEY`; the Aider harness must re-export from our form.)

3. **No mocks of the LLM in tests that exercise a real harness.** Use the
   `MockProvider` in `pkg/llm/mock.go` only for unit-level path tests, not
   for harness round-trips. Harness tests must use a real (Ollama is fine
   for offline) backend or be marked `t.Skip` when the dependency is absent.

4. **Standard library `testing` only.** No testify / no assert libraries.
   Table-driven tests with `t.Run`. Setup/teardown via `defer`.

5. **Existing `Harness` interface (`library_service.go:76`) stays
   compatible.** `RunSession(ctx, prompt, workdir) error` is in use by
   `ClaudeCLIHarness` and `AiderHarness`. Add a richer return path *in
   addition* (e.g., `RunSessionWithResult`) — do not break the existing
   one.

6. **Real OpenRouter calls cost real money.** Steps that issue paid calls
   require an explicit gate: a `--allow-openrouter-spend` flag or
   `CORTEX_EVAL_ALLOW_SPEND=1` env var. Free-tier `:free` models are exempt
   *only after* TODO 1 has measured the actual free-tier limits.

7. **Don't push, don't open PRs, don't run `cortex daemon`.** Local commits
   on the current branch only.

---

## Current state (where things live)

- **Schema:** `internal/eval/v2/cellresult.go` (Go source of truth) +
  `internal/eval/v2/cellresult_test.go` (shape lock)
- **Existing harnesses:** `internal/eval/v2/library_service_aider_harness.go`,
  `library_service_*` for ClaudeCLIHarness
- **Existing Harness interface:** `internal/eval/v2/library_service.go:76`
- **LLM providers:** `pkg/llm/{anthropic,ollama,claude_cli,hugot,mock}.go`,
  interface in `pkg/llm/provider.go`
- **Persister:** `internal/eval/v2/persist.go` (SQLite at
  `.cortex/db/evals_v2.db`)
- **Scenarios:** `test/evals/scenarios/`, `test/evals/library-service/`,
  `test/evals/journeys/`, `test/evals/v2/`, `test/evals/corpus/`
- **Eval CLI:** `cmd/cortex/` (look for `eval` subcommand wiring)
- **OpenRouter docs (loop should consult):** quickstart at
  `https://openrouter.ai/docs/quickstart`, OpenAI-compatible endpoint
  `POST https://openrouter.ai/api/v1/chat/completions` with
  `Authorization: Bearer $OPEN_ROUTER_API_KEY`
- **Aider OpenRouter:** Aider already supports `--model openrouter/<x>`
  via litellm + `OPENROUTER_API_KEY` env var (litellm hardcodes that
  canonical name). The Aider harness must re-export
  `OPENROUTER_API_KEY="$OPEN_ROUTER_API_KEY"` before invoking aider —
  no other code change required for Aider routing to OpenRouter.
- **opencode CLI:** `opencode run --model <provider/model> --dir <wd>
  --format json "<prompt>"` (event-stream JSON output)
- **pi.dev CLI:** `pi -p "<prompt>"` print mode, `pi --mode json` event
  stream, `--provider`, `--model`, `--api-key` flags. Custom providers via
  `~/.pi/agent/models.json`.

---

## Iteration protocol (every tick)

1. **Read this file end-to-end.** Don't skip.
2. **Read `git log -5 --oneline`** to see what previous ticks landed.
3. **Read `git status`** to confirm a clean tree. If dirty, the prior tick
   crashed mid-step — reconcile (commit, revert, or stop) before doing more.
4. **Pick the lowest-numbered un-checked TODO** from the list below.
5. **Implement just that step.** No scope creep. If the step seems to
   require touching more than 3 files outside its scope, stop and ask.
6. **Run the gate:**
   ```
   go build ./...
   go test ./internal/eval/v2/... -count=1
   go test ./pkg/llm/... -count=1
   ```
   Plus any test files specifically created for this step.
7. **If green:** commit with subject `eval-harness: <step short title>`,
   then edit *this file* to flip the box from `[ ]` to `[x]`, commit the
   doc edit as `docs(eval-harness-loop): mark step N done`.
8. **If red:** print the failing output, do not commit, stop the loop.
9. **Schedule next tick at 60s** (no I/O wait makes longer delays
   wasteful). Use the same prompt path.

---

## Ordered TODOs

> Each step is one tick. Don't merge steps. If a step is bigger than it
> looks, split it inline and add the sub-steps as new checkboxes.

### Phase 1 — Foundations

- [x] **1. Probe OpenRouter free tier and lock down cost field.**
  - Write a small one-shot program (e.g.,
    `cmd/cortex-or-probe/main.go` — throwaway, do not wire into the main
    `cortex` binary) that POSTs one chat completion to a `:free` model
    (e.g. `meta-llama/llama-3.1-8b-instruct:free`) using `$OPEN_ROUTER_API_KEY`.
  - Capture the full response JSON to `docs/openrouter-probe.json`.
  - Write `docs/openrouter-tiers.md` documenting:
    free-tier daily/per-min cap (verify experimentally if not in docs);
    response field exposing per-call USD cost (typically
    `usage.cost` or via `/api/v1/generation` lookup);
    recommended small/medium/large model IDs available today.
  - **Done:** probe runs locally and emits both files; loop now knows the
    cost-extraction code path for step 2.

- [ ] **2. Add `pkg/llm/openrouter.go` (Provider implementation).**
  - Implement `pkg/llm.Provider` interface for OpenRouter.
  - Endpoint: `https://openrouter.ai/api/v1/chat/completions`
  - Auth: `Authorization: Bearer ${OPEN_ROUTER_API_KEY}`
  - Model string format: pass through verbatim (`anthropic/claude-3-5-haiku`,
    `meta-llama/llama-3.1-70b-instruct`, etc.). No prefix translation.
  - Parse response: extract content, prompt/completion tokens, **cost_usd**
    (use the field discovered in step 1).
  - Add `pkg/llm/openrouter_test.go`: unit tests using `httptest.Server`
    only — never hit the real endpoint in `go test`.
  - **Done:** `go test ./pkg/llm/... -count=1` green; manual smoke
    `cortex` build with `OPEN_ROUTER_API_KEY` unset must not panic.

### Phase 2 — Harness telemetry seam

- [ ] **3. Add `HarnessResult` + `RunSessionWithResult` (additive, non-breaking).**
  - Define `HarnessResult` struct in `internal/eval/v2/harness.go`:
    fields = TokensIn, TokensOut, CostUSD, AgentTurnsTotal, FilesChanged,
    LatencyMs, ProviderEcho, ModelEcho. (Subset of CellResult — runner
    fills the rest.)
  - Add an *optional* extension method on the existing `Harness` interface
    via a separate interface:
    ```go
    type ResultfulHarness interface {
        Harness
        RunSessionWithResult(ctx context.Context, prompt, workdir string) (HarnessResult, error)
    }
    ```
    Runner type-asserts and falls back to bare `RunSession` for legacy
    paths.
  - **Done:** existing `library_service_*_test.go` still green; new
    interface defined but no implementation yet.

- [ ] **4. Implement `RunSessionWithResult` for `AiderHarness`.**
  - Aider with `--no-stream` writes a final summary line. Capture stdout
    (replace `io.Discard`), parse for token/cost lines (Aider exposes
    `Tokens: 1,234 sent, 567 received. Cost: $0.0012` or similar — verify
    against actual output by running once locally against Ollama).
  - When the model is `openrouter/...`, also fetch
    `https://openrouter.ai/api/v1/generation?id=<gen_id>` for authoritative
    cost (Aider may not surface OpenRouter's exact cost field).
  - **Done:** new test that runs Aider against `ollama/qwen2.5-coder:1.5b`
    and asserts a populated `HarnessResult` (skip when Ollama unreachable).

### Phase 3 — New harnesses

- [ ] **5. Add `OpenCodeHarness` (`internal/eval/v2/library_service_opencode_harness.go`).**
  - Mirror `AiderHarness` structure: binary resolution
    (`$OPENCODE_BINARY` → PATH), CLI invocation
    `opencode run --model <model> --dir <workdir> --format json
    "<prompt>"`, JSON event-stream parser for tokens/turns.
  - Implements both `Harness` and `ResultfulHarness`.
  - **Done:** `go test ./internal/eval/v2/...` green; new test file
    `library_service_opencode_harness_test.go` with t.Skip when
    `opencode` not on PATH.

- [ ] **6. Add `PiDevHarness` (`internal/eval/v2/library_service_pidev_harness.go`).**
  - CLI invocation: `pi --mode json --provider openrouter --model <x>
    -p "<prompt>"` from `cmd.Dir = workdir`. Parse newline-delimited JSON
    events for tokens, turns, file edits.
  - Custom-provider config (`~/.pi/agent/models.json`) is the user's job
    to set up — the harness should fail loudly with a clear error message
    pointing at the docs if pi can't reach OpenRouter.
  - **Done:** parallel to step 5.

### Phase 4 — Persistence and runner

- [ ] **7. Persist `CellResult` rows.**
  - Add a new SQLite table `cell_results` in `internal/eval/v2/persist.go`
    with one column per CellResult JSON tag.
  - Add `Persister.PersistCell(ctx, *CellResult) error` (calls
    `r.Validate()` first; never insert invalid rows).
  - Migration: append-only, follow existing `ALTER TABLE` pattern.
  - **Done:** persistence test with table-driven cases for valid + invalid
    rows.

- [ ] **8. Grid runner.**
  - New file `internal/eval/v2/grid.go`. Function:
    `RunGrid(ctx, scenarios []*Scenario, harnesses []Harness, models []string,
    strategies []ContextStrategy) ([]CellResult, error)`.
  - Cartesian product → one CellResult per cell. Persist each as it
    completes (don't buffer the whole grid).
  - Concurrency: serial by default. Add a `--parallel N` knob in the CLI
    later, not now.
  - **Done:** unit test using a fake harness that returns canned
    HarnessResults for 2×2×2 = 8 cells.

- [ ] **9. CLI surface.**
  - New subcommand `cortex eval grid --scenarios <dir> --harnesses
    aider,opencode,pi_dev --models <list> --strategies baseline,cortex`.
  - Reads `OPEN_ROUTER_API_KEY` from env. Refuses to run if any selected
    harness binary is missing (clear error).
  - **Done:** `go build ./cmd/cortex/...` green; `cortex eval grid --help`
    shows the new flags.

### Phase 5 — Spend safety + smoke

- [ ] **10. Cost ceiling — multi-tier guard for the $20 budget.**
  - Implement three independent ceilings, all read from env with defaults:
    - `CORTEX_EVAL_RUN_USD_CEILING` (default `$5.00`) — abort the current
      grid run when running spend would exceed this. One grid run = one
      `RunGrid()` call.
    - `CORTEX_EVAL_DAILY_USD_CEILING` (default `$8.00`) — abort if
      cumulative spend across all grid runs in a calendar day (UTC)
      would exceed this. Persisted to `.cortex/db/evals_v2.db` (new
      table `daily_spend(date TEXT PRIMARY KEY, usd REAL)`).
    - `CORTEX_EVAL_LIFETIME_USD_CEILING` (default `$18.00`) — soft global
      stop, leaves $2 buffer against the user's $20 OpenRouter top-up.
      Persisted to the same DB.
  - **Spend estimation:** before issuing a cell's call, estimate cost as
    `max(last_observed_cost_for_(provider,model), 1.5 × tier_floor)`
    where `tier_floor = {small: $0.01, medium: $0.05, large: $0.30,
    frontier: $0.90}`. If the estimate would push any of the three
    ceilings over, abort *before* the call (do not issue it).
  - **Free-tier preference:** when both a `:free` and paid variant of the
    same model family exist (e.g., `llama-3.1-8b-instruct:free` vs
    `llama-3.1-8b-instruct`), prefer `:free` unless the user explicitly
    pinned the paid one in the model list.
  - **Frontier guard:** issuing a call to a model whose `tier_floor`
    exceeds $0.50/cell requires `CORTEX_EVAL_ALLOW_FRONTIER=1`. This is
    a separate gate from `CORTEX_EVAL_ALLOW_SPEND` so a routine run
    can't accidentally fire Sonnet/Opus.
  - **Partial-result emit:** on abort, flush all completed CellResult
    rows + write a `<run_id>.partial.csv` summary to `.cortex/db/`
    explaining which ceiling tripped.
  - **Done:** unit tests cover (a) run ceiling trips after N cells with
    a fake provider returning fixed `cost_usd`, (b) lifetime ceiling
    persists across two `RunGrid()` calls, (c) free-tier preference
    routes correctly, (d) frontier guard blocks Sonnet without env var.

- [ ] **11. End-to-end smoke run (gated).**
  - Requires `CORTEX_EVAL_ALLOW_SPEND=1`.
  - 1 scenario × 1 harness (aider) × 1 OpenRouter free model × 1
    strategy (baseline). Real call, real CellResult written to SQLite.
  - **Done:** smoke completes in < 5 min, row exists in
    `.cortex/db/evals_v2.db`, `cortex eval grid --report` shows it.

- [ ] **12. Stop the loop.** All boxes checked. Print a summary of what
  shipped and what's deferred (e.g., parallelism, hallucination detector,
  grid scheduler).

---

## Stop conditions (any one of these → halt the loop, do not schedule)

- A test in the gate command failed.
- A step would require modifying `cellresult.go`'s JSON tags or enum
  constants.
- A step would issue a paid OpenRouter call without the
  `CORTEX_EVAL_ALLOW_SPEND=1` gate.
- An external CLI (`opencode`, `pi`, `aider`, `ollama`) is missing and the
  current step needs it — emit a one-line install hint and stop.
- A step would push to a remote, open a PR, or run `cortex daemon`.
- The git tree is dirty in a way the loop didn't create (i.e., user has
  in-progress work).
- More than 3 consecutive ticks have failed at the same step.

When stopping, **leave a single short summary line** explaining which
condition triggered and what the user needs to do.

---

## Notes for the loop runner

- This file is the source of truth for ordering. If you discover the order
  is wrong (e.g., step 5 needs something step 7 produces), edit *this file*
  to reorder, commit the doc edit, then proceed with the new ordering.
- Don't add tasks via `TaskCreate` — the checkbox list above *is* the task
  list. `TaskCreate` is for ad-hoc work.
- Don't write CHANGELOG entries, README updates, or design docs unless a
  step explicitly calls for it.
- Don't refactor adjacent code. Bug fixes that block a step are fine; clean
  them up in the same commit with a clear "needed for step N" rationale.
