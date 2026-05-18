# DAG Build — Stage 1 v0 (Minimum Viable DAG)

End-to-end working DAG protocol at the smallest possible scope: **one
DAG type (`turn`), four ops (`sense.prompt`, `attend.reflex`,
`decide.inject`, `decide.coding_turn`), single-threaded executor, CLI
entry point, telemetry**. Validates the protocol against reality
before the full registry expansion in Stage 2.

The 5 mechanic evals from Phase C are the test gates: all 5 must pass
before v0 is considered done.

See [`docs/dag-build-plan.md`](../dag-build-plan.md) Stage 1 for the
authoritative spec, [`docs/dag-protocol.md`](../dag-protocol.md) for
the runtime semantics, and
[`docs/integration-roadmap.md`](../integration-roadmap.md) Phase 5 for
how this fits into the larger build.

## Working environment

- **Worktree:** `/Users/dereksantos/eng/projects/cortex-dag-build/`
- **Branch:** `derek.s/dag-build`
- **Build:** `go build -o bin/cortex ./cmd/cortex` after each change
- **Tests:** `go test ./...` after each change (TDD where possible)

**Prerequisites** (verify all before iteration 1):
- [ ] Phase 1 (tool-surface foundation) DONE — `tools.json` exists,
  uniform envelope on `--json`, unified `cell_results.jsonl` sink
- [ ] Eval prep Phase A DONE — pre-integration baseline recorded
- [ ] Eval prep Phase C DONE — 5 mechanic eval fixtures authored
- [ ] `cortex run` CLI scaffold exists OR you'll create it as
  deliverable 5 below

If any prerequisite is missing, STOP and report — do not proceed.
The build needs the verification floor first.

All paths in the loop instructions below are **relative to the
worktree root**. If `pwd` is not the worktree root, `cd` there first.

## Loop

Each iteration:

0. **Verify environment + prerequisites.** Run `pwd && git rev-parse
   --abbrev-ref HEAD`. Confirm prerequisites above (one quick `ls`
   per item). If any missing, STOP.

1. **Read state.** Open `docs/dag-build-plan.md` Stage 1. Check which
   deliverables are checked off. Open `docs/dag-protocol.md` for the
   relevant sections (handler signature for whichever deliverable
   you're starting; budget model for #2; executor mechanics for #3;
   etc.).

2. **Pick the next deliverable** in this order (do them sequentially;
   each depends on prior):

   - **(1) `pkg/cognition/registry.go`** — Registry interface; 4 ops
     registered (`sense.prompt`, `attend.reflex`, `decide.inject`,
     `decide.coding_turn`). NodeSpec includes `Function`, `Op`,
     `Description`, `Inputs`, `Outputs`, `AxisContract` (for `act`-typed
     — none in v0), `Cost`, `MaxFanout`, `Handler`.

   - **(2) `pkg/cognition/dag/budget.go`** — `Budget` type with
     `latency_ms`, `tokens`, `depth`; decay logic (subtract on each
     node call); per-DAG-type seeds + initial budgets loaded from
     `pkg/config`. Turn-DAG defaults: `2000ms / 4000 tokens /
     depth 10`.

   - **(3) `pkg/cognition/dag/executor.go`** — Single-threaded
     seed-and-grow walker. Maintains pending set; spawn scheduling
     via `pending` deque; depth cap check; budget exhaustion handling;
     per-node `cell_results.jsonl` writes with `parent_node_id`.
     **No parallelism in v0** — leave that for Stage 4.

   - **(4) `pkg/cognition/dag/spawn.go`** — Spawn-spec serialization:
     terse line-per-node form (humans / logs) + JSON canonical form
     (storage / replay). Round-trip tests.

   - **(5) `cmd/cortex/commands/run.go`** — `cortex run --type=turn
     --prompt "<text>"` entry point. Routes to the executor with the
     turn-DAG seed. `--json` output uses the Phase 1 envelope.

   - **(6) `decide.coding_turn` handler** — Wraps the existing LLM
     agent loop from `internal/harness/loop.go`. **For v0: does NOT
     spawn `act.*` children** (just runs the inner loop, captures
     final response + `CostConsumed`, returns). The spawning-children
     variant lands in Stage 3 after ADR-001 settles.

3. **Resolve open ADRs as they come up.** Three open ADRs from
   `dag-build-plan.md` Stage 1:
   - **ADR-001:** `decide.coding_turn` internal structure (in v0:
     run inline — defer spawn-children to Stage 3).
   - **ADR-002:** Budget pass-through to LLM tool calls (in v0:
     `coding_turn` consumes its `CostConsumed` as a single block; no
     per-tool-call decomposition until Stage 3).
   - **ADR-003:** First-turn bootstrap with empty journal (in v0:
     `attend.reflex` returns empty `candidates`; `decide.inject`
     handles empty top-k gracefully; nothing crashes).
   For each, author a short ADR file under `docs/adrs/` capturing the
   decision + rationale.

4. **Test against the 5 mechanic evals** (from Phase C). All 5 must
   transition from "DAG executor not implemented" (current state) to
   green pass. Run `cortex eval --suite=mechanic` after each
   executor-touching change. Don't proceed to the next deliverable
   if a mechanic eval regresses.

5. **End-to-end smoke test.** Run `cortex run --type=turn --prompt
   "What does this codebase do?"`. Verify:
   - Returns a sensible response
   - Produces exactly 4 `cell_results.jsonl` rows
   - Parent pointers chain: seed → reflex → inject → coding_turn
   - Sum of per-row `CostConsumed` matches budget delta
   - Re-run with same prompt + fixed seed produces identical row
     counts and parent structure (determinism)

6. **Commit** with conventional-commits style. One commit per
   deliverable. Examples:
   - `feat(cognition): node registry with 4 v0 ops`
   - `feat(cognition/dag): budget model with 3-axis decay`
   - `feat(cognition/dag): single-threaded seed-and-grow executor`
   - `feat(cognition/dag): spawn-spec serialization`
   - `feat(cli): cortex run --type=turn entry point`
   - `feat(harness): decide.coding_turn handler wrapping LLM loop`
   - `docs(adrs): ADR-001 coding_turn structure, ADR-002 budget
     passthrough, ADR-003 cold-start bootstrap`
   Do NOT push.

7. **Update `docs/dag-build-plan.md`** Stage 1 to check off each
   deliverable + test gate.

8. **Check stopping condition.** If all 6 deliverables done, all 5
   mechanic evals green, smoke test passes, 3 ADRs authored — STOP
   and write a "DAG Stage 1 v0 complete" entry to `eval-journal.md`
   reporting the smoke-test trace and the mechanic-eval result.

## Constraints

- **No expansion beyond the 4 v0 ops.** Stage 2 expands the registry;
  Stage 1 proves the protocol on the minimum viable surface. Adding
  ops here is scope creep.
- **No parallelism.** Stage 4 adds parallel sibling execution; v0 is
  single-threaded. The executor walks pending FIFO.
- **No loop rewrite of `cortex code` / REPL.** Stage 3 rewires them
  to use the new executor; v0 leaves them on the legacy path. The new
  surface is `cortex run --type=turn` only.
- **No cross-turn budget rollover.** Stage 4 adds rollover; v0 a
  budget-exceeded spawn is just lost.
- **Don't push to remote.**
- **Per `dag-protocol.md`:** handlers return `(Out, Spawn[],
  CostConsumed)` — don't deviate from the signature.
- **Per eval-principles 4 (Reproducible):** the executor must be
  deterministic given the same seed + handler costs + (when LLM-free)
  no RNG.

## Verification

Per deliverable:
- ☐ (1) Registry exposes the 4 ops via `NodeSpec`; each has
  description usable by another node deciding to spawn it.
- ☐ (2) Budget decay arithmetic correct; per-type seeds loaded from
  config; mechanic-1 (budget decay determinism) passes.
- ☐ (3) Executor walks; depth cap enforced; exhaustion graceful;
  mechanic-3 + mechanic-4 pass.
- ☐ (4) Spawn-spec round-trips between terse and JSON forms.
- ☐ (5) `cortex run --type=turn --prompt "X"` works end-to-end;
  `--json` output validates against Phase 1 envelope.
- ☐ (6) `decide.coding_turn` invokes the LLM loop; returns
  `CostConsumed` populated; no panic on empty `top` input.

Loop-wide stopping condition (v0 done when):
- ☐ All 6 deliverables checked off in `docs/dag-build-plan.md` Stage 1.
- ☐ All 5 mechanic evals (Phase C) pass green.
- ☐ Smoke test produces 4 cell_results rows with correct parent
  pointers, deterministic across re-runs with fixed seed.
- ☐ 3 ADRs authored under `docs/adrs/`.
- ☐ `eval-journal.md` "DAG Stage 1 v0 complete" entry present.
- ☐ `go test ./...` green.
- ☐ Existing v2 + legacy + Phase A baselines still run (no regressions
  in pre-existing functionality — `cortex code` and REPL untouched).

## When to ask the user

- If `decide.coding_turn` needs to spawn children to integrate with
  existing harness telemetry (i.e. ADR-001 reveals the inline approach
  has a blocker — surface before forcing it).
- If a mechanic eval's expected output is ambiguous against the spec
  in `dag-protocol.md` (worth resolving the spec ambiguity rather than
  baking an interpretation into v0).
- If the budget defaults (`2000ms / 4000 tokens / depth 10`) produce
  silly results in practice — propose new defaults before hardcoding.
- If Stage 1 reveals the protocol design needs adjustment — that's
  the whole point of v0; pause and surface rather than ship something
  the design doesn't actually support.
