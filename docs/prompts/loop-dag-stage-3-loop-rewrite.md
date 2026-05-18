# DAG Build — Stage 3: Loop Rewrite + coding_turn Refactor

After Stage 2 lands ~9 real registered ops, `cortex run --type=turn`
produces a tree of real micro-decisions. Stage 3 makes the existing
host harness (`cortex code` + the REPL) **go through the DAG executor**
instead of running its own loop — and refactors `decide.coding_turn`
to spawn `act.*` children per tool call (per ADR-001's Stage 3 plan).

After Stage 3: every coding turn — whether triggered by `cortex code`,
the REPL, or `cortex run --type=turn` — runs as a DAG, and every tool
call surfaces as a first-class child node in the trace with its own
axis contract and cost accounting.

See [`docs/dag-build-plan.md`](../dag-build-plan.md) Stage 3,
[`docs/adrs/0001-coding-turn-structure.md`](../adrs/0001-coding-turn-structure.md)
for the V0-vs-Stage-3 plan, and
[`docs/tool-surface.md`](../tool-surface.md) for the 6-axis contract
each `act` op satisfies.

## Prerequisites (verify before starting)

```bash
git log --oneline -10
./bin/cortex eval --suite=mechanic           # expect 5/5 PASS
./bin/cortex eval --suite=legacy-cognition   # Stage 2 PASS rate
./bin/cortex run --type=turn --prompt "X"    # real ops chain works
./bin/cortex code --help                     # legacy harness still here
```

Stage 2 must be substantively complete (real ops registered) — Stage 3
relies on `decide.coding_turn` having a meaningful inline LLM call
from Stage 1 v0 + the surrounding chain ops being real (Stage 2)
before this rewrite is honest.

## Working environment

- **Worktree:** `/Users/dereksantos/eng/projects/cortex-dag-build/`
- **Branch:** `derek.s/dag-build`
- **Build/Tests:** standard
- **API key:** OpenRouter via keychain required (real LLM + tool calls)

## Outcome (when this loop stops)

Four things landed:

1. **`internal/harness/loop.go` rewritten** as a seed-and-grow
   walker that defers to the DAG executor. The current
   `Loop.Run(ctx, prompt)` keeps its signature for backwards
   compatibility but internally builds a turn DAG seed + invokes
   `pkg/cognition/dag.NewExecutor` rather than running its own
   loop iteration logic.

2. **Existing 5 tools registered as `act` ops** with `AxisContract`
   per `tool-surface.md` (Mutator + RequiresConfirmation flags):
   - `act.list_dir` (read)
   - `act.read_file` (read)
   - `act.write_file` (mutator)
   - `act.run_shell` (mutator + confirmation for destructive cmds)
   - `act.cortex_search` (read)

3. **`decide.coding_turn` refactored to spawn children** per ADR-001
   Stage 3:
   - Handler intercepts LLM-emitted tool calls before dispatch
   - Each tool call becomes an `act.*` spawn in the handler's
     `NodeResult.Spawn`
   - Tool results flow back via standard DAG dataflow
   - LLM's own token cost stays on the `coding_turn` node;
     per-tool latency/cost lives on the child rows
   - Backwards-compat: if the host registry has no act ops registered,
     `coding_turn` falls back to inline dispatch (V0 behavior preserved)

4. **`cortex code` + REPL become thin wrappers** around
   `cortex run --type=turn --workdir <X>`. The CLI behavior is
   unchanged from the user's POV; internally they go through the
   executor + emit per-node telemetry.

## Loop

Each iteration:

0. **Verify environment.** `pwd && git rev-parse --abbrev-ref HEAD`.
   Abort if mismatched.

1. **Read state** — `dag-build-plan.md` Stage 3, ADR-001 +
   ADR-002, current `internal/harness/loop.go`, `cmd/cortex/commands/
   code.go`, `cmd/cortex/commands/repl.go`.

2. **Pick the next deliverable** in this order:

   ### A. Register the 5 tools as `act` ops

   In `internal/harness/tools.go` (or a new file alongside): each
   existing tool gets a NodeSpec registration. AxisContract per tool:
   - `read_file`, `list_dir`, `cortex_search` → Mutator=false
   - `write_file` → Mutator=true, RequiresConfirmation=false
   - `run_shell` → Mutator=true, RequiresConfirmation=true (for
     destructive commands per the regex from sandbox.go); otherwise
     false

   Verify each registers cleanly + appears in `tools.json` after
   regen.

   ### B. Refactor decide.coding_turn to spawn children

   In `internal/harness/dagnode/coding_turn.go`:
   - Add a per-tool-call interception path. Today's V0 handler calls
     `h.RunSessionWithResult(ctx, prompt, workdir)` and returns the
     whole result. Stage 3 needs to intercept each tool call the
     LLM emits, decide whether to dispatch inline (V0 path) or
     return as Spawn (Stage 3 path).
   - Simplest interception: refactor `internal/harness/loop.go` to
     expose a per-tool-call callback before dispatch. `coding_turn`
     accumulates the calls into `NodeResult.Spawn` and lets the
     executor dispatch them as `act.*` nodes.

   This is the load-bearing refactor of Stage 3. Test with a known
   simple prompt that triggers exactly 1-2 tool calls before
   broadening.

   ### C. Rewire `cortex code` to go through cortex run

   In `cmd/cortex/commands/code.go`: replace the direct
   `evalv2.NewCortexHarness(model)` + `RunSessionWithResult` path
   with an invocation of `cortex run --type=turn --workdir <X>`
   (in-process via the RunCommand, not via subprocess).

   The current `code.go` CLI flags map to `cortex run` flags or
   environment variables. JSON output stays the same shape (uses
   the Phase 1 envelope).

   ### D. Rewire REPL to loop over cortex run

   In `cmd/cortex/commands/repl.go`: the per-turn handler that
   currently invokes the loop directly becomes an invocation of
   `cortex run --type=turn --prompt <X> --workdir <Y>` per user
   message. Preserves transcript handling.

3. **Test after each deliverable:**
   - `go test ./...` — must stay green
   - Run a known journey scenario via the new code path; compare
     against pre-Stage-3 baseline in `eval-baseline.md` (within
     noise envelope)
   - `cell_results.jsonl` for a turn shows N+1 rows: 1 `coding_turn`
     row + N `act.*` rows with `parent_node_id` chained to coding_turn

4. **Commit per deliverable** with conventional commits. Do NOT push.

5. **Update docs:**
   - Check off Stage 3 items in `docs/dag-build-plan.md`
   - Append `docs/eval-journal.md` entry: "coding_turn spawns N
     children per turn; trace shape post-rewrite"
   - Regenerate `tools.json` after the 5 act ops register

6. **Stop** when all 4 deliverables landed + baselines preserved +
   coding_turn produces per-tool trace rows.

## Constraints

- **Preserve cortex code + REPL CLI semantics.** Existing users /
  scripts must see the same behavior — only the internal path
  changes. The transcript writes / hooks / spawning behavior must
  match.
- **Don't break Stage 1 v0's stub-mode fallback.** Running
  `cortex run --type=turn --prompt "X"` without `--model` must
  still work via the stub path.
- **Don't break the existing v2 / library_service harness path.**
  v2 evals may still construct CortexHarness directly; that path
  stays alive until Stage 6 retires it.
- **Per-tool axis contracts enforced.** Destructive `run_shell`
  invocations without `confirm=true` must trip the axis-5 gate
  before dispatch (per the mechanic-3-style enforcement pattern).
- **No regression on existing baselines** in `eval-baseline.md`
  (within noise envelope already captured there).
- **Don't push to remote.**

## Verification

Per deliverable:
- **(A)** Each of 5 tools registered as NodeSpec with AxisContract;
  `tools.json` regenerated; CI test passes.
- **(B)** A 2-tool-call prompt produces 3 trace rows (1 coding_turn
  + 2 act.*). Parent pointers correct. Destructive run_shell w/o
  confirm trips axis-5 gate.
- **(C)** `cortex code "X"` produces equivalent output to pre-Stage-3
  + `.cortex/db/dag_traces.jsonl` populated.
- **(D)** REPL multi-turn session produces correctly-chained traces
  across turns (no orphaned rows).

Loop-wide stopping condition:
- ☐ All 4 deliverables committed
- ☐ Journey baseline scenarios (from Stage 2 / continuation) re-run
  via the new code path and within-noise of prior numbers
- ☐ At least one e2e SWE-bench instance runs through `cortex code`
  and produces a complete trace (sanity check on the integration)
- ☐ `cell_results.jsonl` shape for a coding turn: 1 coding_turn row
  + N act.* rows, all `parent_node_id` chained correctly
- ☐ `docs/eval-journal.md` "Stage 3 complete" entry exists

## When to ask the user

- If the tool-call interception requires significantly restructuring
  the existing `Loop.Run` (not just additive hooks) — surface before
  starting the refactor.
- If preserving cortex code CLI semantics requires a translation
  layer that's bigger than the rewrite saves — propose alternatives.
- If destructive op detection in `run_shell` becomes ambiguous (some
  commands are destructive in some contexts), surface for design call.
- If the LongMemEval / SWE-bench eval suites regress under the new
  path, pause and triage before proceeding to Stage 4.

## Reference index

| File | Why it matters |
|---|---|
| `docs/dag-build-plan.md` Stage 3 | Authoritative spec |
| `docs/adrs/0001-coding-turn-structure.md` | V0-vs-Stage-3 plan |
| `docs/adrs/0002-budget-passthrough.md` | Per-tool budget plan (Stage 4 in spirit, lands here for the structural piece) |
| `docs/tool-surface.md` | 6 axes each act op satisfies |
| `internal/harness/loop.go` | The loop to rewrite |
| `internal/harness/tools.go` | Existing tool registry → act op registrations |
| `internal/harness/sandbox.go` | Destructive-cmd regex for axis-5 gate |
| `internal/harness/dagnode/coding_turn.go` | V0 inline handler to refactor for spawn-children |
| `cmd/cortex/commands/code.go` | The CLI wrapper to thin out |
| `cmd/cortex/commands/repl.go` | The REPL loop to rewire |
| `evalv2.CortexHarness` (internal/eval/v2) | What today's coding_turn wraps; tomorrow's coding_turn delegates to the executor instead |
