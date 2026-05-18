# Phase C — Author 5 Mechanic Evals for the DAG Executor

5 YAML fixtures under `test/evals/mechanic/` exist, plus a CLI runner
stub at `cortex eval --suite=mechanic` that loads them. Each fixture
**fails today** with a clean "DAG executor not implemented" message
(not a crash). The 5 fixtures become the TDD signal for Phase 5's
DAG executor — when v0 lands, all 5 must pass before the build stage
is considered done.

See [`docs/eval-prep-epic.md`](../eval-prep-epic.md) Phase C for the
5 evals defined, [`docs/dag-protocol.md`](../dag-protocol.md) for the
handler signature, budget model, and spawn mechanics each fixture
exercises, and [`docs/prompts/eval-principles.md`](eval-principles.md)
for the principles (especially 4 Reproducible + 5 Isolated — these
mechanic evals must be deterministic and network-free).

## Working environment

- **Worktree:** `/Users/dereksantos/eng/projects/cortex-dag-build/`
- **Branch:** `derek.s/dag-build`
- **Build:** `go build -o bin/cortex ./cmd/cortex` after each change
- **Tests:** `go test ./...` after each change
- **No executor exists yet** — fixtures are authored against the
  spec in `dag-protocol.md`, not against running code

All paths in the loop instructions below are **relative to the
worktree root**. If `pwd` is not the worktree root, `cd` there first.

## Loop

Each iteration:

0. **Verify environment.** Run `pwd && git rev-parse --abbrev-ref HEAD`.
   If `pwd` is not `/Users/dereksantos/eng/projects/cortex-dag-build`
   or the branch is not `derek.s/dag-build`, STOP and report.

1. **Read state.** Open `docs/eval-prep-epic.md` Phase C (the 5 evals
   listed). Open `docs/dag-protocol.md` sections "Seed + grow + decay",
   "Node schema → Handler signature", "Budget model", "Spawning". These
   are the contracts each fixture exercises.

2. **Pick the next deliverable** in order:

   - **(i) Suite scaffolding** — `test/evals/mechanic/` directory; a
     `README.md` in it explaining the suite's purpose; CLI wiring for
     `cortex eval --suite=mechanic` that loads YAMLs from that dir
     and returns "DAG executor not implemented" for each (the failure
     mode is structured, not a crash).

   - **(ii) Mechanic-1: Budget decay determinism.** Fixture: 3 mock
     nodes with fixed `CostConsumed` values (e.g. 100ms each); fixed
     initial budget (e.g. 1000ms); expected `remaining_budget` at
     each step. Pass criterion: arithmetic matches to the millisecond.

   - **(iii) Mechanic-2: Tree reconstruction from parent pointers.**
     Fixture: a known 5-node tree (seed → 2 children → 2 grandchildren
     under one of them). Expected: post-hoc reconstruction from
     `cell_results.jsonl` rows produces the same tree shape (parent
     pointers correctly chain).

   - **(iv) Mechanic-3: Depth cap enforcement.** Fixture: `budget.depth
     = 3`; a mock node at depth 3 that attempts to spawn a child at
     depth 4. Expected: spawn refused with `error: depth_exceeded`;
     in-flight node finishes normally.

   - **(v) Mechanic-4: Budget exhaustion graceful degradation.**
     Fixture: a node sequence that exhausts `latency_ms` mid-tree.
     Expected: in-flight nodes finish; no new spawns; the exhaustion
     event names the exhausted axis (`latency_ms`); no orphaned
     in-flight nodes in the trace.

   - **(vi) Mechanic-5: Tree-shape variation.** Two fixtures (or one
     parametrized) with prompts that should warrant different trees
     — e.g. trivial prompt vs. code-symbol-rich prompt. Expected:
     post-hoc tree shapes differ measurably (depth, fan-out, or op
     mix). This verifies the tree is actually grown based on inputs,
     not always the same default walk.

3. **Author the YAMLs.** Format follows v2 scenario shape where
   possible. Critical fields per fixture:
   - `id`, `version`, `description`
   - `mocked_handlers:` — declarative cost specs per node-id
     (`{node_id, cost_consumed: {latency_ms, tokens}, spawn: [...]}`)
   - `seed:` — initial nodes the executor walks
   - `initial_budget:` — `{latency_ms, tokens, depth}`
   - `expected:` — concrete assertions (`remaining_budget_at_step`,
     `tree_shape`, `error_codes`, etc.)
   - `failure_message_today:` — the structured "not implemented"
     string the runner stub returns

4. **Test** the runner stub: each fixture loads, fails with the
   expected message, doesn't crash, produces one `cell_results.jsonl`
   row marking the failure as structured (not silent).

5. **Commit** with conventional-commits style. One commit per
   fixture (or batched 2-3 at a time):
   - `eval(mechanic): suite scaffolding + CLI stub`
   - `eval(mechanic): mechanic-1 budget decay determinism fixture`
   - …
   Do NOT push.

6. **Update `docs/eval-prep-epic.md`** Phase C to check off each
   completed mechanic-N fixture.

7. **Check stopping condition.** All 5 fixtures + scaffolding present,
   suite CLI loads them, each fails with the "not implemented"
   structured failure. STOP and append a "Phase C complete" entry
   to `eval-journal.md` listing the 5 fixtures and confirming they
   fail as expected.

## Constraints

- **No DAG executor code.** This phase authors fixtures only. Phase 5
  ships the executor; these fixtures are its TDD signal.
- **Deterministic by construction.** Mocked handler costs eliminate
  variance. If a fixture depends on real timing, it's wrong — rewrite.
- **Network-free.** No LLM calls, no real I/O. Mocks all the way.
- **All 5 must fail today** with a structured failure (cell_results
  row with `error_code: not_implemented`), not a crash. A crash is a
  bug; a structured failure is the expected pre-Phase-5 state.
- **Do not push to remote.** Local commits only.
- **Per eval-principles 4 (Reproducible):** running the suite twice
  back-to-back produces byte-identical output.
- **Per eval-principles 5 (Isolated):** the suite runs offline.
- **Per eval-principles 6 (Structured):** every fixture failure
  produces a `cell_results.jsonl` row, not just stderr noise.

## Verification

Per fixture:
- ☐ YAML exists; parses; required fields populated.
- ☐ CLI loads it via `cortex eval --suite=mechanic` without crash.
- ☐ Failure is structured (cell_results row with `error_code: not_implemented`).
- ☐ Expected assertions are concrete (a future implementer can mechanically
  verify pass).

Loop-wide stopping condition:
- ☐ All 5 mechanic fixtures present in `test/evals/mechanic/`.
- ☐ `cortex eval --suite=mechanic` runs cleanly (5/5 fail with structured
  "not implemented" rows).
- ☐ Two back-to-back runs produce byte-identical output.
- ☐ `docs/eval-prep-epic.md` Phase C checked off.
- ☐ `eval-journal.md` "Phase C complete" entry exists.

## When to ask the user

- If `dag-protocol.md` is ambiguous about a contract a fixture needs
  to exercise (better to surface the ambiguity than guess and bake
  it into a fixture).
- If the structured-failure pattern would require executor changes
  that bleed into Phase 5 scope.
- If a sixth mechanic eval becomes obviously needed and you want to
  add it (vs. note it as a follow-up).
