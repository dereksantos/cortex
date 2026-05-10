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

8. **All eval results land as structured rows.** Every `CellResult` goes
   to *both* the SQLite `cell_results` table and the
   `.cortex/db/cell_results.jsonl` append log so downstream analysis
   (pandas, polars, DuckDB, `jq`) doesn't need to scrape opaque files.
   `CellResult`'s JSON tag names are the column-name contract for both
   backends — see hard constraint #1 about the schema being a contract.

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

- [x] **2. Add `pkg/llm/openrouter.go` (Provider implementation).**
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

- [x] **3. Add `HarnessResult` + `RunSessionWithResult` (additive, non-breaking).**
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

- [x] **4. Implement `RunSessionWithResult` for `AiderHarness`.**
  - Aider with `--no-stream` writes a final summary line. Capture stdout
    (replace `io.Discard`), parse for token/cost lines (Aider exposes
    `Tokens: 1,234 sent, 567 received. Cost: $0.0012` or similar — verify
    against actual output by running once locally against Ollama).
  - When the model is `openrouter/...`, also fetch
    `https://openrouter.ai/api/v1/generation?id=<gen_id>` for authoritative
    cost (Aider may not surface OpenRouter's exact cost field).
  - **Done:** new test that runs Aider against `ollama/qwen2.5-coder:1.5b`
    and asserts a populated `HarnessResult` (skip when Ollama unreachable).

### Phase 3 — Persistence and runner

- [x] **5. Persist `CellResult` rows (SQLite + JSONL append).**
  - Add a new SQLite table `cell_results` in `internal/eval/v2/persist.go`
    with one column per CellResult JSON tag.
  - **Also append each row to `.cortex/db/cell_results.jsonl`** — one
    valid JSON object per line (the same shape `json.Marshal(*CellResult)`
    already produces), opened `O_APPEND|O_CREATE`, fsync'd after each
    write. JSONL is the canonical portable format for downstream data
    analysis (pandas / polars / DuckDB / `jq` consume it natively);
    SQLite handles ad-hoc queries. Both backends are populated; neither
    is optional.
  - `Persister.PersistCell(ctx, *CellResult) error` calls `r.Validate()`
    first (never insert invalid rows), writes to SQLite, then appends to
    JSONL. If the JSONL append fails the function returns the error
    *without* rolling back the SQLite insert — duplicate analysis rows
    are tolerable; a missing row is not.
  - Migration: append-only, follow existing `ALTER TABLE` pattern.
  - **Done:** persistence test with table-driven cases for valid + invalid
    rows + a JSONL line-count assertion + a round-trip test (write,
    read line, `json.Unmarshal` back into CellResult, equals original).

- [x] **6. Grid runner.**
  - New file `internal/eval/v2/grid.go`. Function:
    `RunGrid(ctx, scenarios []*Scenario, harnesses []Harness, models []string,
    strategies []ContextStrategy) ([]CellResult, error)`.
  - Cartesian product → one CellResult per cell. Persist each as it
    completes (don't buffer the whole grid).
  - Concurrency: serial by default. Add a `--parallel N` knob in the CLI
    later, not now.
  - **Done:** unit test using a fake harness that returns canned
    HarnessResults for 2×2×2 = 8 cells.

- [x] **7. CLI surface.**
  - New subcommand `cortex eval grid --scenarios <dir> --harnesses
    aider --models <list> --strategies baseline,cortex`. (opencode +
    pi_dev are added later by TODOs 19 and 20 — those harnesses are
    deferred past the smoke run, so the CLI ships aider-only first.)
  - Reads `OPEN_ROUTER_API_KEY` from env. Refuses to run if any selected
    harness binary is missing (clear error).
  - **Done:** `go build ./cmd/cortex/...` green; `cortex eval grid --help`
    shows the new flags.

### Phase 4 — Spend safety + smoke

- [x] **8. Cost ceiling — multi-tier guard for the $20 budget.**
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

- [x] **9. End-to-end smoke run (gated).**
  - Requires `CORTEX_EVAL_ALLOW_SPEND=1`.
  - 1 scenario × 1 harness (aider) × 1 OpenRouter free model × 1
    strategy (baseline). Real call, real CellResult written to *both*
    SQLite and the JSONL append log.
  - **Done:** smoke completes in < 5 min, row exists in
    `.cortex/db/evals_v2.db` AND a matching line exists in
    `.cortex/db/cell_results.jsonl`, `cortex eval grid --report` shows it.

### Phase 5 — Real signal on ~$5 OpenRouter budget (Aider-only)

> Loop redirected by user on 2026-05-10 after the smoke run landed.
> Stop chasing additional harnesses — opencode/pi.dev moved to Phase 7
> (indefinite hold). Refocus on producing real eval signal end-to-end
> through Aider × OpenRouter on a ~$5 budget. The grid runner currently
> lacks scoring AND context injection, so `strategy=cortex` is a label
> not a behavior and `task_success` is just "no error". These steps fix
> that, author a small coding-scenario library, run the experiment, and
> report results.
>
> **Self-heal philosophy:** transient OpenRouter 429s, single-model
> failures, partial-budget exhaustion → retry / drop / re-run. Halt
> only on hard failures (test red, schema drift, budget completely
> exhausted, repeated stop-conditions). Default toward producing
> *some* result, with a partial.csv if the full grid can't complete.

- [x] **10. Scenario `verify` field + post-harness verifier exec.**
  - Extend Scenario YAML with optional `verify: <shell command>`. After
    the harness session ends, the grid runner execs `bash -c "<cmd>"`
    in the workdir; exit 0 → TaskSuccess=true, non-zero → false.
  - When `verify` includes a `go test` invocation, parse the stdout
    summary for `--- PASS:` / `--- FAIL:` counts and populate
    TestsPassed/TestsFailed. For non-go-test verifiers leave both at 0
    (TaskSuccess alone is the signal).
  - Empty `verify` field → keep current behavior: TaskSuccess from
    harness exit code, no test counts. Don't impose a default — opt-in.
  - **Done:** unit test with a fake harness + scenarios whose verify is
    `true` vs `false` shows TaskSuccess flips; a third scenario with
    `verify: echo '--- PASS: TestFoo'; echo '--- PASS: TestBar'` shows
    TestsPassed=2.

- [x] **11. Scenario `seed_dir` field + copy-into-workdir.**
  - Optional `seed_dir: <relative path>` on Scenario. Before invoking
    the harness, the runner recursively copies `seed_dir` contents into
    the cell's temp workdir. Scenarios without `seed_dir` get an empty
    workdir (current behavior).
  - Use `cp -R` via exec (portable on macOS + Linux); fail the cell
    with a clear error if seed_dir doesn't exist.
  - **Done:** unit test with a scenario whose seed_dir contains a
    marker file `MARKER.txt` confirms the marker shows up in the
    cell's workdir before the harness runs.

- [x] **12. Cortex strategy = inject scenario `context` into prompt.**
  - Optional `context: [bullet, bullet, ...]` on Scenario. When
    `ContextStrategy == "cortex"`, the runner prepends the bullets to
    the harness prompt as:
    ```
    RELEVANT CONTEXT:
    - <bullet 1>
    - <bullet 2>
    ...

    TASK:
    <original query>
    ```
    Token estimate (`len(prefix)/4`) populates InjectedContextTokens.
  - This is *static* cortex — hand-authored context per scenario, not
    Reflex-mined. The point is to make the strategy dimension actually
    *vary the prompt* so a measurable signal exists. Real mining stays
    out of scope; if the static signal is positive, that justifies
    investing in mining; if not, that's also signal.
  - **Done:** unit test confirms strategy=cortex produces a prompt with
    the prefix and InjectedContextTokens > 0; strategy=baseline keeps
    the original prompt and InjectedContextTokens = 0.

- [x] **13. Author 5 minimal coding scenarios.**
  - Under `test/evals/coding/`: 5 small Go tasks with seeds + verifiers
    + cortex context. Suggested set:
    1. **fizzbuzz** — implement function from stub, verify `go test`
    2. **rename-json-tag** — change a struct's JSON tag from `old_name`
       → `new_name`, verify by grep on the new tag
    3. **fix-off-by-one** — fix a `<` that should be `<=`, verify
       `go test`
    4. **add-table-test** — add a test case to a table, verify the test
       runs ≥ N cases (`grep -c '"name"' file_test.go`)
    5. **error-wrap** — wrap a bare error with `fmt.Errorf("...: %w",
       err)`, verify by grep on the wrap pattern
  - Each seed_dir gets a minimal `go.mod`, the stubs, and the tests.
    Each scenario file gets 2-3 cortex-context bullets capturing the
    convention/decision the agent should follow (e.g. "Match the
    existing test style: table-driven with t.Run subtests").
  - **Done:** `cortex eval grid --scenarios test/evals/coding --models
    openai/gpt-oss-20b:free --strategies baseline` runs all 5 cells
    end-to-end without panics; pass/fail mix is fine — failure on a
    free model is real signal.

- [x] **14. Self-healing retry on transient 429s.**
  - Wrap each cell's harness call with bounded retry: up to 3 attempts
    with 15s / 30s / 60s backoff when Aider's stderr matches a 429-ish
    pattern (`temporarily rate-limited`, `retry_after`, `429`). Hard
    failures (auth, missing model, invalid request) do not retry.
  - Retry attempts are logged but each cell still emits one CellResult
    (the final attempt's outcome). Attach `retry_count: N` to the cell's
    `Notes` field if N > 0.
  - **Done:** unit test with a fake harness whose stderr contains
    "temporarily rate-limited" the first attempt then succeeds asserts
    2 calls + final TaskSuccess = true.

- [ ] **15. Run the $5 experiment.**
  - Grid: 5 scenarios × 5 models × 2 strategies = **50 cells**.
    - Free (3): `openai/gpt-oss-20b:free`, `google/gemma-4-26b-a4b-it:free`,
      `nvidia/nemotron-nano-9b-v2:free`. ~$0.
    - Paid medium (1): `qwen/qwen3-coder` (~$0.10/cell × 10 = $1).
    - Paid large (1): `anthropic/claude-haiku-4.5` (~$0.30/cell × 10 = $3).
    - Total expected: ~$4 of the $5 budget.
  - Command:
    ```
    CORTEX_EVAL_ALLOW_SPEND=1 \
    CORTEX_EVAL_RUN_USD_CEILING=5.00 \
    cortex eval grid \
      --scenarios test/evals/coding \
      --models openai/gpt-oss-20b:free,google/gemma-4-26b-a4b-it:free,nvidia/nemotron-nano-9b-v2:free,qwen/qwen3-coder,anthropic/claude-haiku-4.5 \
      --strategies baseline,cortex
    ```
  - **Self-heal protocol** if the run aborts mid-grid:
    1. If the trip is run-ceiling: drop the most-expensive remaining
       model (Haiku), re-run with the smaller model list.
    2. If a model is repeatedly 429-locked: drop it, continue.
    3. If < 20% of cells completed: stop, print partial CSV path,
       leave TODO 15 unchecked.
    4. If ≥ 80% of cells completed: accept the partial set, proceed
       to TODO 16.
  - **Done:** ≥ 40 of 50 cells completed; SQLite + JSONL contain the
    rows; final spend reported and ≤ $5.

- [ ] **16. Aggregate + report results.**
  - Add `cortex eval grid --report-summary` which queries SQLite,
    groups by (model, strategy), and prints:
    - Cells: count
    - Pass-rate: passes / cells
    - Mean tokens (in + out)
    - Mean cost USD
    - Mean latency
    - Lift = pass_rate(cortex) − pass_rate(baseline) for the same
      (model, scenarios) — needs the strategy join.
    - Cost-per-pass = sum(cost) / passes (or ∞ if 0 passes).
  - Print the headline: which model+strategy pairs reach the largest
    model's baseline pass-rate at lower cost — the small-model
    amplifier signal, if it exists.
  - **Done:** `cortex eval grid --report-summary` prints the table;
    user sees real numbers from the experiment.

- [x] **17. Wire OpenRouter into the existing `cortex eval` CLI.**
  - The legacy `cortex eval` CLI (Phase 1's existing v2 framework, NOT
    `cortex eval grid`) supports retrieval-style scenarios with a real
    scoring layer (judge LLM, NDCG, ABR, lift). It currently accepts
    `-p ollama` and `-p anthropic` via a switch in
    `cmd/cortex/commands/eval.go`. Add `-p openrouter` as a third arm
    that constructs `llm.NewOpenRouterClient(cfg)` (already exists from
    TODO 2) and treats `--model` as the verbatim OpenRouter model ID.
  - Goal: unlock the **48 retrieval scenarios** in
    `test/evals/v2/` + `test/evals/corpus/` against OpenRouter models
    without touching the grid runner. Existing ABR/lift output works as-is.
  - Don't reuse this work for the grid runner — they're complementary
    (grid runner = coding-task pipeline; existing CLI = retrieval-task
    pipeline). Keep them in their own lanes.
  - **Done:** `cortex eval -p openrouter -m openai/gpt-oss-20b:free`
    completes a small retrieval scenario sweep without panic; ABR or
    lift numbers print; SQLite has the result rows.

### Phase 6 — Final

- [ ] **18. Stop the loop.** All boxes checked. Print a one-page
  summary of what shipped, the experiment's headline numbers, and what's
  explicitly deferred (Reflex-mined cortex, multi-session scenarios,
  alternative harnesses in Phase 7).

### Phase 7 — Indefinite hold (alternative harnesses)

> Held back per the 2026-05-10 user pivot. These harnesses remain
> technically interesting (richer JSON event-streams than Aider, distinct
> prompt-injection shapes for ablation) but are off the critical path
> until Aider-via-grid has produced the lift numbers worth comparing
> against. Pick up only after Phase 5+6 deliver real signal.

- [ ] **19. Add `OpenCodeHarness` (`internal/eval/v2/library_service_opencode_harness.go`).**
  - **Requires `opencode` on PATH** (`curl -fsSL https://opencode.ai/install | bash`).
    Re-running the loop without it will halt again at this step.
  - Mirror `AiderHarness` structure: binary resolution
    (`$OPENCODE_BINARY` → PATH), CLI invocation
    `opencode run --model <model> --dir <workdir> --format json
    "<prompt>"`, JSON event-stream parser for tokens/turns.
  - Implements both `Harness` and `ResultfulHarness`.
  - **Done:** `go test ./internal/eval/v2/...` green; new test file
    `library_service_opencode_harness_test.go` with t.Skip when
    `opencode` not on PATH.

- [ ] **20. Add `PiDevHarness` (`internal/eval/v2/library_service_pidev_harness.go`).**
  - **Requires `pi` on PATH** (see https://pi.dev install instructions).
    Re-running the loop without it will halt again at this step.
  - CLI invocation: `pi --mode json --provider openrouter --model <x>
    -p "<prompt>"` from `cmd.Dir = workdir`. Parse newline-delimited JSON
    events for tokens, turns, file edits.
  - Custom-provider config (`~/.pi/agent/models.json`) is the user's job
    to set up — the harness should fail loudly with a clear error message
    pointing at the docs if pi can't reach OpenRouter.
  - **Done:** parallel to step 19.

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
