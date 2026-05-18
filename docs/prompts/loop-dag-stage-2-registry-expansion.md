# DAG Build — Stage 2: Registry Expansion

Expand the DAG node registry from Stage 1 v0's 4 stub ops to ~9 real
micro-LLM-backed ops. This is where the small-model amplifier thesis
becomes literal: each `attend.rerank` / `value.score` /
`decide.inject` / etc is a narrow LLM call (≤100 tokens output) the
executor composes through the seed-and-grow walk.

After Stage 2, `cortex run --type=turn --prompt "X"` produces a tree
whose mid-chain nodes are real micro-decisions, not stubs — and the
22 legacy/cognition scenarios become per-op acceptance tests for the
new registered ops.

See [`docs/dag-build-plan.md`](../dag-build-plan.md) Stage 2 for the
spec, [`docs/dag-protocol.md`](../dag-protocol.md) "Node distribution"
section for the workload shape, and ADR-001 / ADR-002 for the
coding_turn integration plan.

## Prerequisites (verify before starting)

```bash
git log --oneline -5
./bin/cortex eval --suite=mechanic              # expect 5/5 PASS
./bin/cortex eval --suite=legacy-cognition      # current baseline
./bin/cortex run --type=turn --prompt "X"       # 5-node stub chain works
```

Plus: `loop-continue-build.md` should be substantively complete
(Phase B per-mode dispatchers + Phase D execution adapter landed,
or at least far enough that this Stage 2 work doesn't conflict).

## Working environment

- **Worktree:** `/Users/dereksantos/eng/projects/cortex-dag-build/`
- **Branch:** `derek.s/dag-build`
- **Build:** `go build -o bin/cortex ./cmd/cortex` after each change
- **Tests:** `go test ./...` must stay green
- **API key:** OpenRouter via macOS keychain
  (`security find-generic-password -s cortex-openrouter -w`)
  required for real LLM calls; ops degrade to mechanical fallback
  without it (per the budget-aware self-modulation pattern from
  `executor.go`)

## Outcome (when this loop stops)

Nine real ops registered + their prompt templates + cost calibration:

| Op | Function | Scope (max ~100 tok output) |
|---|---|---|
| `attend.rerank` | attend | "rank these N candidates 1-10 by relevance to <query>" |
| `value.score` | value | "is this candidate load-bearing? Y/N + 1-line why" |
| `value.detect_contradiction` | value | "conflicts with any of {prior}? Y/N + which" |
| `decide.inject` | decide | "inject / defer / queue — and which subset" |
| `decide.should_capture` | decide | "worth keeping in journal? Y/N + tag" |
| `model.predict_next` | model | "top 3 likely follow-up queries" |
| `maintain.extract_insight` | maintain | "1-2 durable insights from <content>, or 'none'" |
| `remember.vector_search` | remember | mechanical (embedder-driven, no LLM) |
| `represent.embed` | represent | mechanical (embedder, no LLM) |

Each op also gets:
- Versioned prompt template under `pkg/cognition/prompts/<function>_<op>.tmpl`
- Cost hint calibrated from real runs (overwrites the placeholder hints)
- Registration in `pkg/cognition/dag/registry.go` (or wherever the
  v0 registry lives — refactor if needed for clarity)

The `cortex run --type=turn` chain in `cmd/cortex/commands/run.go`
gets updated to wire these real ops instead of the v0 stubs.

Legacy/cognition scenarios become **the per-op acceptance suite**:
once Stage 2 lands, scenarios that previously PASS/FAIL/SKIP against
the legacy `internal/cognition/*` implementations now PASS/FAIL/SKIP
against the registered DAG ops. The runner stays the same; the
modes it dispatches to point at the new registry.

## Loop

Each iteration:

0. **Verify environment.** Run `pwd && git rev-parse --abbrev-ref HEAD`.
   If `pwd` is not `/Users/dereksantos/eng/projects/cortex-dag-build`
   or the branch is not `derek.s/dag-build`, STOP and report.

1. **Read state** — `dag-build-plan.md` Stage 2, the current
   `pkg/cognition/dag/registry.go`, and `dag-protocol.md`'s Node
   distribution table (which is the spec for what each op does).

2. **Pick the next op** in this order (cheapest first; each is
   ~30-60 min if focused):

   - `represent.embed` (no LLM; thin wrapper around the embedder)
   - `remember.vector_search` (no LLM; thin wrapper around storage)
   - `maintain.extract_insight` (LLM, narrow output)
   - `attend.rerank` (LLM, ranking task)
   - `value.score` (LLM, binary + 1-line)
   - `value.detect_contradiction` (LLM, binary + which)
   - `decide.inject` (LLM, 3-way decision)
   - `decide.should_capture` (LLM, binary + tag)
   - `model.predict_next` (LLM, top-3 prediction)

3. **For each op:**

   a. **Author the prompt template** under
      `pkg/cognition/prompts/<function>_<op>.tmpl`. Versioned (header
      with `version: 1`). Narrow output. Includes a few-shot example
      where it sharpens the contract.

   b. **Implement the handler** wiring the prompt to
      `pkg/llm`'s provider. Self-modulates on remaining budget
      (skip LLM, use mechanical fallback below ~200ms remaining).
      Returns proper `CostConsumed`.

   c. **Register** in the executor's default registry (or move the
      registry out of `cmd/cortex/commands/run.go` into
      `pkg/cognition/dag/defaults.go` if it makes sense to share
      with the harness rewrite that lands in Stage 3).

   d. **Test** via legacy/cognition: at least one scenario for the
      op (e.g., `reflect_rerank` for `attend.rerank`) passes after
      registration.

   e. **Calibrate cost hint** from the test run's `cell_results.jsonl`
      latency + token observations. Update the `NodeSpec.Cost` to
      match observed p50.

4. **After all 9 ops:** rewire `buildV0Registry` in
   `cmd/cortex/commands/run.go` to use the new real ops; rename if
   no longer "V0".

5. **Author ADR-004** — per-op prompt template format + versioning
   conventions. Land under `docs/adrs/0004-prompt-templates.md`.

6. **Commit** per op (or batched 2-3 if logically grouped). Conventional
   commits. Do NOT push.

7. **Update docs** as ops land:
   - Check off Stage 2 items in `docs/dag-build-plan.md`
   - Append a `docs/eval-journal.md` entry with the new
     legacy-cognition baseline (PASS rate should improve as real
     ops replace stubs)

8. **Stop** when all 9 ops landed + ADR-004 exists + legacy-cognition
   PASS rate shows the new ops working.

## Constraints

- **Don't modify the executor itself.** Stage 4 adds parallelism;
  Stage 2 is op-registration only.
- **Don't break legacy/cognition tests** that pass today (9-16
  depending on session state). The new ops should be at-least-as-
  good as the legacy implementations they replace.
- **Per-op output budget: ≤100 tokens** — this is the small-model-
  amplifier thesis. If a prompt needs more, the scope is too broad;
  split it.
- **Prompt templates are versioned + checked into git.** No
  dynamically-constructed prompts that aren't auditable.
- **Mechanical fallback per op** — every LLM-backed op must have a
  deterministic fallback when budget is low. Required for
  eval-principles 4 (Reproducible) and 5 (Isolated).
- **Don't push to remote** (per `feedback-no-push-without-consent`).
- **Regenerate `tools.json`** if any new cobra command lands.

## Verification

Per op:
- ☐ Prompt template exists; `version: 1`; narrow output
- ☐ Handler registered; `go build ./...` green
- ☐ At least one legacy/cognition scenario dispatched to the op PASSes
- ☐ Cost hint reflects observed p50 from a real run
- ☐ Mechanical fallback path exists + tested

Loop-wide stopping condition:
- ☐ All 9 ops landed + committed
- ☐ ADR-004 (prompt template format) authored
- ☐ `cortex run --type=turn` chain uses real ops (not stubs)
- ☐ `cortex eval --suite=legacy-cognition` PASS rate improves over
  Phase B's 16/29 baseline (target: 24+ PASS once reflect/think/dream
  dispatchers wire to real DAG ops)
- ☐ All 5 mechanic evals still PASS (no regression)
- ☐ `docs/eval-journal.md` "Stage 2 complete" entry exists
- ☐ `docs/dag-build-plan.md` Stage 2 deliverables checked off

## When to ask the user

- If a prompt template would need >100 tokens of output to do its
  job correctly — surface the scope question before broadening.
- If an op's mechanical fallback would significantly degrade quality
  vs. the LLM path; the trade-off needs naming.
- If the existing legacy `internal/cognition/<mode>` implementation
  is meaningfully different from the registered op's behavior, and
  removing the legacy code would break something — surface before
  removing.
- If the registry refactor (moving out of `commands/run.go`) becomes
  a big yak-shave; defer to Stage 3 if so.

## Reference index

| File | Why it matters |
|---|---|
| `docs/dag-build-plan.md` Stage 2 | Authoritative spec |
| `docs/dag-protocol.md` | Node distribution + handler signature |
| `pkg/cognition/dag/registry.go` | NodeSpec + Registry to extend |
| `cmd/cortex/commands/run.go` | Where buildV0Registry registers ops |
| `internal/cognition/{reflex,reflect,resolve,think,dream}.go` | Legacy implementations to mirror behavior of |
| `internal/eval/legacy/runner.go` | Per-mode dispatchers (validation suite) |
| `pkg/llm/` | Provider for LLM-backed ops |
| `internal/storage/` | For remember.vector_search backend |
