# Continue Build — Phase B / D / Stage 1 v0 Finish

Continues the build from the 2026-05-17 session that committed 18
changes on `derek.s/dag-build` (last commit `86f2dfe`). That session
landed substantive completion of Phase B (SeedFixtures + reflex
dispatcher), Phase D (seed adapter), and Stage 1 v0
(executor + decide.coding_turn). This prompt finishes the
well-scoped iteration that remains.

The substrate is **in place and proven** — this is iteration
following established patterns, not blocked work.

## Working environment

- **Worktree:** `/Users/dereksantos/eng/projects/cortex-dag-build/`
- **Branch:** `derek.s/dag-build` (currently 18+ commits ahead of
  origin/main; not pushed)
- **Build:** `go build -o bin/cortex ./cmd/cortex` after each change
- **Tests:** `go test ./...` — currently green; keep it that way

## What's already done (do NOT redo)

Validate state before starting:

```bash
git log --oneline -20
./bin/cortex eval --suite=mechanic     # 5/5 PASS expected
./bin/cortex eval --suite=legacy-cognition   # 16 PASS / 13 FAIL / 0 SKIP expected
CORTEX_JOURNEYS_WITH_SEED=1 ./bin/cortex eval --suite=journeys  # 10/10 SEED_OK expected
```

If any of those baselines differ, something regressed — stop and
investigate before adding new code.

## Outcome (when this loop stops)

Three deliverables landed and committed:

1. **Phase B per-mode dispatchers wired** — reflect, think, dream,
   router each get a `run<Mode>Test` function following the
   `runReflexTest` pattern in `internal/eval/legacy/runner.go`.
   `cortex eval --suite=legacy-cognition` drops the
   `needs_per_mode_dispatcher` SKIPs and surfaces real PASS/FAIL.
2. **Phase B FAIL triage** — the 13 reflex FAILs from this session
   are categorized as (a) fixture-content tuning, (b) scenario
   expectation update, or (c) real Reflex regression. Each categorized
   FAIL gets either a fix or a documented finding in
   `docs/eval-journal.md`.
3. **Phase D full-execution adapter** — `cortex eval --suite=journeys`
   gains an `--execute` (or `CORTEX_JOURNEYS_EXECUTE=1`) mode that
   drives the `cortex code` harness through each session's task
   after seeding. At least 1 of the 10 journeys runs end-to-end
   producing structured cell_results.jsonl rows.

After all three: `docs/eval-prep-epic.md` Phases B + D get checked
off; `docs/eval-baseline.md` Phase F gets a refresh pass picking up
the new numbers.

## Loop

Each iteration:

0. **Verify environment.** Run `pwd && git rev-parse --abbrev-ref HEAD`.
   If `pwd` is not `/Users/dereksantos/eng/projects/cortex-dag-build`
   or the branch is not `derek.s/dag-build`, STOP and report.

1. **Read state** — current entry in `docs/eval-journal.md`, the
   deliverable status table in `docs/eval-prep-epic.md`, and the
   commits since `86f2dfe` to see what's already landed.

2. **Pick the next deliverable** in this order:

   ### A. Phase B per-mode dispatchers (~30 min each)

   Pattern reference: `internal/eval/legacy/runner.go` →
   `runReflexTest` is the template. For each new mode:

   - `runReflectTest`: dispatch to `cognition.NewReflect(...)`,
     compare result reranking to `expected.top_result_ids`,
     `expected.contradictions`, etc. (per the `reflect_*` scenarios).
   - `runDreamTest`: dispatch to `cognition.NewDream(...)`. Note
     `Dream` is async/budget-bounded; these scenarios test single-shot
     dispatch (e.g. `dream_source_coverage` expects N sources hit).
   - `runThinkTest`: dispatch to `cognition.NewThink(...)`. Scenarios
     check `TopicWeights` content (e.g. `session_topic_learning`).
   - `runRouterTest`: dispatch to `cognition.NewRouter(...)`. Routes
     queries to mode by characteristics; check `expected.routed_to`.

   Each: same shape as `runReflexTest`. Per-scenario temp dir →
   SeedFixtures → `storage.New` → construct the mode → invoke →
   compare against `expected` block.

   The 19 scenarios marked `needs_per_mode_dispatcher` today should
   either PASS or surface real findings once these land.

   ### B. Phase B FAIL triage (the 13 reflex FAILs)

   Run `./bin/cortex eval --suite=legacy-cognition -v 2>&1 | grep -A 1 FAIL`
   and walk through each. Three categories:

   - **(a) Fixture-content tuning**: the canonical fixtures'
     Summary text doesn't contain the keywords Reflex's text-scoring
     matches. Adjust the Summary to include relevant terms.
   - **(b) Scenario expectation update**: the scenario expected an
     ID that isn't a canonical fixture; either add a new canonical
     fixture or update the scenario's `result_ids`.
   - **(c) Real Reflex regression**: the implementation behaves
     differently than the scenario assumes for valid reasons. Document
     in `eval-journal.md` like the Phase B mode-drift triage entry
     from this session.

   Per-scenario: pick a category, apply the fix, re-run, commit.

   ### C. Phase D full-execution adapter (~3-4h)

   The seed adapter (this session, `journey.RunSuiteWithSeed`) proves
   the journey → Cortex-context pipeline works. The full-execution
   adapter adds:

   - After seeding session N's events, derive a "task" from the
     session's context (the events themselves are the established
     decisions/patterns; the task is to write code consistent with
     them).
   - Invoke `cortex code` (via `evalv2.CortexHarness` —
     `internal/eval/v2/library_service_cortex_harness.go`) against
     the scaffold workdir + task + the seeded Cortex storage.
   - Score the result: file changes match expectations, tests pass
     (some scaffolds have tests), no destructive ops.
   - Repeat per session in the journey.
   - Emit structured `cell_results.jsonl` rows (one per session).

   Use `trivial-hello-world` as the first end-to-end target — it's
   the smallest journey (3 sessions / 2 events) so iteration is
   fast.

   Requires: an OpenRouter API key (via macOS keychain
   `cortex-openrouter` per `pkg/llm/client.go:137`). Without a key,
   the adapter falls back to seed-only mode.

3. **Test** after each change — `go build ./...` then
   `go test ./...`. Don't proceed if anything regresses.

4. **Commit** with conventional-commits style. One commit per
   deliverable. Do NOT push.

5. **Update docs** as deliverables land:
   - Check off Phase B / D items in `docs/eval-prep-epic.md`
   - Add an `eval-journal.md` entry per major deliverable
   - Refresh `docs/eval-baseline.md` once Phase B + D have new numbers

6. **Stop** when all three deliverables are committed AND
   `eval-journal.md` has a "build continuation complete" entry
   summarizing the new state.

## Constraints

- **Don't modify the cognitive mode implementations**
  (`internal/cognition/*`) unless triage category (c) requires it.
  These are Stage 5 territory once the DAG protocol's node registry
  expands.
- **Don't push to remote** — user reviews + pushes manually
  (`feedback-no-push-without-consent` in memory).
- **Don't break the working CLI surfaces** that landed this session:
  - `cortex eval --suite=mechanic` must stay 5/5 PASS
  - `cortex eval --suite=legacy-cognition` resolve scenarios must
    stay 9/9 PASS
  - `cortex run --type=turn` chain must still execute 5 nodes
  - `cortex eval --suite=journeys` validation + seed must keep
    working
- **Per eval-principles 4 (Reproducible)** and **5 (Isolated)**:
  per-scenario temp dirs, no network beyond LLM calls, deterministic
  by construction.
- **Regenerate `tools.json`** if any new cobra command lands
  (`./bin/cortex tools` after build). The `TestToolsJSONUpToDate`
  test guards against drift.

## Verification

Per deliverable:

**A. Per-mode dispatchers:**
- ☐ `runReflectTest` exists and at least one reflect scenario PASSes
- ☐ `runThinkTest` exists and at least one think scenario PASSes
- ☐ `runDreamTest` exists and at least one dream scenario PASSes
- ☐ `runRouterTest` exists and at least one router scenario PASSes
- ☐ `cortex eval --suite=legacy-cognition` reports 0 needs_per_mode_dispatcher

**B. FAIL triage:**
- ☐ Each of the 13 reflex FAILs is in a named category (a/b/c)
- ☐ FAILs in (a) and (b) are fixed (committed)
- ☐ FAILs in (c) are documented in `eval-journal.md`
- ☐ `legacy-cognition` PASS rate improves measurably

**C. Phase D full-execution:**
- ☐ `journey.RunSuiteWithExecution` (or similar) exists
- ☐ At least 1 journey (start with trivial-hello-world) runs end-to-end
- ☐ Produces structured `cell_results.jsonl` rows per session
- ☐ `--execute` or `CORTEX_JOURNEYS_EXECUTE=1` toggle works

Loop-wide stopping condition:
- ☐ All three deliverables landed + committed
- ☐ `eval-journal.md` "build continuation complete" entry exists
- ☐ `docs/eval-prep-epic.md` Phases B + D + F status reflects new state
- ☐ `go test ./...` green; CLI baselines from this session preserved

## When to ask the user

- If a per-mode dispatcher reveals that the scenario fixtures need a
  separate set (not just the canonical 16 from Phase B) — surface
  before authoring more fixtures.
- If a Phase B FAIL triage uncovers what looks like a real Reflex
  regression (category c) that's severe enough to block other work.
- If the Phase D full-execution adapter would require modifying
  `library_service_cortex_harness.go` rather than wrapping it.
- If credentials/API keys aren't available and the Phase D adapter
  can only run in seed-only mode (acceptable; document and continue).
- If the build plan's Stage 2 (per-mode op expansion for the DAG
  registry) starts to overlap with this work — these two paths can
  productively converge (the legacy-cognition runner becomes a
  validation suite for Stage 2's registered ops), but converging is
  a design decision worth surfacing.

## Reference index — files this prompt depends on

| File | Why it matters |
|---|---|
| `internal/eval/legacy/runner.go` | `runReflexTest` is the per-mode dispatcher template |
| `internal/eval/legacy/fixtures.go` | `SeedFixtures` + `CanonicalFixtures` — the seed-via-JSONL pattern |
| `internal/eval/journey/runner.go` | `SeedJourney` + `RunSuiteWithSeed` — Phase D seed adapter template |
| `internal/eval/v2/library_service_cortex_harness.go` | Phase D full-execution adapter pattern to reuse |
| `pkg/cognition/dag/` | Stage 1 v0 executor (no changes expected) |
| `docs/eval-prep-epic.md` | Phase status checkboxes to update |
| `docs/eval-journal.md` | Per-deliverable entries to append |
| `docs/eval-baseline.md` | Refresh pass after Phase B + D land new numbers |
| `docs/adrs/0001-coding-turn-structure.md` | ADR-001 V0 plan (don't deviate without surfacing) |
