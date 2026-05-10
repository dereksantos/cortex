# Resumption prompt — picks up where the 2026-05-10 session left off

> Read this file end-to-end. It captures everything a fresh Claude
> Code session needs to continue the eval-harness work without rediscovery.

---

## Where we are

The eval-harness build loop (`docs/eval-harness-loop.md`) is **complete
through TODO 18**. The harness, the grid runner, the scoring layer,
cortex injection, multi-tier USD ceilings, the 5-coding-scenario
library, the retry path, the experiment runs, and the CLI surfaces all
shipped. **Skip TODOs 19/20** (opencode, pi.dev) — on indefinite hold.

The findings memo (`docs/eval-findings-2026-05-10.md`) captures the
diagnostic arc and the **per-eval-shape signal**:

| eval shape | result with cortex |
|---|---|
| Coding tasks (5 scenarios, after the `--file` fix) | 0 pp lift; scenarios saturate at 75-100% pass-rate |
| Retrieval tasks (5-scenario sample, `gpt-oss-20b:free`) | **+52% lift, 31% token reduction** |
| Retrieval tasks (15-scenario, Haiku-4.5) | **+31% lift, 32/65 cortex wins** |

Cumulative spend: ~$0.50 of the $20 OpenRouter budget. Plenty of
runway.

---

## What's running or staged when this prompt is read

Two things may be in flight or recently complete — check these first:

1. **Noisy-variant retrieval sweep** (background task `bdsqze52d` in
   the prior session — won't survive a fresh session, just check the
   output file). Compares Haiku × 15 scenarios with 20 decoy
   `context:` items added per scenario. Logs at
   `/tmp/eval-noisy-15-paid.log`. The clean comparison is at
   `/tmp/eval-clean-15-paid.log` (saved during this session, may not
   survive a reboot).

2. **The full v2 retrieval sweep** (`go run ./cmd/cortex eval -p
   openrouter -m anthropic/claude-haiku-4.5 -d test/evals/v2`) — never
   ran clean to completion last session because the free-tier rate
   limits stalled the original attempt. Paid Haiku has no rate-limit
   issue; this is a ~$1 sweep you may want to do.

---

## Open experiments + their cost shape

| Experiment | What it measures | Effort | Est cost |
|---|---|---|---|
| **Noisy-retrieval comparison** | Does cortex retrieval signal survive a 20× noise dilution? Validates the retriever, not just the corpus. | If `/tmp/v2-15-noisy/` still exists, run + diff against clean. Otherwise regenerate with `/tmp/inject_noise/main.go`. | ~$0.20 |
| **Full v2 retrieval sweep on Haiku** | 40 scenarios → broader-sample lift number, not just 15. | One command, ~5-10 min. | ~$1.50 |
| **Library-service cumulative experiment** | The user's "build up knowledge via dreaming, then build it out" thesis test. Multi-session, cumulative cortex state. Headline shape-similarity comparison. | Significant — see "library-service plan" below. | ~$3-5 per full sweep |
| **Harder coding scenarios** | Author scenarios where the cortex bullets convey info NOT in seed files (hidden conventions, deprecated approaches, cross-file dependencies). Re-run grid. | ~2 hours scenario authoring + 1 run. | ~$0.50 |
| **Compare-runs tool** | A script that compares two eval result sets side-by-side. Useful for "before fix vs after fix" or "clean vs noisy" deltas. | ~30 min. | $0 |

---

## Library-service cumulative experiment plan

The user (2026-05-10) asked to extend the existing library-service
eval shape:

> Building a larger project from scratch, with cortex clean slate, and
> it should build up knowledge through some initial dreaming and then
> build it out and see if cortex thinking applies lift.

Status of the underlying framework:

- `internal/eval/v2/library_service.go` — `LibraryServiceEvaluator`
  exists, accepts any `Harness`, supports `baseline/cortex/frontier`
  conditions, has the `CortexInjector` that captures session output
  back into cortex and prepends mined patterns to the next session's
  prompt.
- `test/evals/library-service/SPEC.md` — full design doc.
- `test/evals/library-service/sessions/*.md` — five session prompts
  (scaffold-and-books, authors, loans, members, branches).
- **Missing:** CLI entry-point that takes `--model openrouter/<x>` and
  drives the full 5-session run.
- **Missing:** "Pre-flight Dream" phase — Dream the seed project
  before the first session to populate cortex with structural
  observations. Requires either reusing existing Dream code or a
  scripted-capture pass that mimics it.

Suggested next-session approach:
1. Add `cortex eval library-service --model openrouter/<x>
   --condition cortex` (or hang off the existing `cortex eval` switch).
2. Make `--pre-dream` optional; if set, walk the seed project + capture
   AST observations into cortex before S1.
3. Run baseline + cortex × {small, medium} models. Frontier (Haiku)
   already lands the work — the small-model amplifier signal is what
   matters here.

Budget guard: enable `CORTEX_EVAL_RUN_USD_CEILING=5.00` since
multi-session runs can drift higher than single-call cells.

---

## How to use this prompt in a fresh session

Paste this whole file as your first message (or attach it). Suggested
opening line:

> Read `docs/eval-resume-prompt.md` and `docs/eval-findings-2026-05-10.md`
> first, then continue the eval-harness work. Default direction:
> [whatever you want — usually one of the experiments above].

The fresh Claude will:
1. Pick up the schema contract from `internal/eval/v2/cellresult.go`
   and respect it (per `docs/eval-harness-loop.md` hard constraints).
2. Use `cortex eval grid` for coding-task experiments and `cortex eval
   -p openrouter` for retrieval experiments — both paths are wired.
3. Default to free-tier `:free` models unless you explicitly request
   paid (it will ask before spending real money on Haiku/Sonnet).

---

## Memory pointers (for the new session)

These memories under `~/.claude/projects/.../memory/` capture key
constraints and decisions:

- `feedback_structured_eval_outputs.md` — every CellResult writes to
  BOTH SQLite + JSONL; analysis pipeline assumed from day 1.
- `project_eval_signal_pivot_2026_05.md` — the 2026-05-10 pivot away
  from opencode/pi.dev toward Aider-only signal generation.
- `reference_pi_dev.md` — pi.dev is a coding-agent harness, not a
  scoring service.
- `feedback_no_unearned_performance_claims.md` — frame lift numbers as
  what the data shows, not as project achievements.

---

## Anti-checklist (things to avoid in a fresh session)

- **Don't re-author** `docs/eval-harness-loop.md` — it's the
  build-loop record, mostly complete; treat as historical.
- **Don't spawn `cortex eval` on `:free` models for the full v2 dir**
  — the original CLI doesn't have the retry path the grid runner
  has, so it stalls on Venice rate limits. Use paid models, or
  subset to ~15 scenarios.
- **Don't mock the LLM** in tests that touch a real harness. The
  retry/cost machinery has trip-wires that catch this.
- **Don't break the `CellResult` JSON-tag contract** without
  bumping `SchemaVersion` and getting user signoff. The whole
  analysis pipeline depends on those names.

---

## Files touched in the 2026-05-10 session worth knowing about

```
cmd/cortex/commands/eval.go         openrouter provider arm (TODO 17)
cmd/cortex/commands/eval_grid.go    cortex eval grid (TODOs 7, 16)
cmd/cortex/cortex-or-probe/main.go  throwaway openrouter probe (TODO 1)
docs/eval-harness-loop.md           the build-loop record
docs/eval-findings-2026-05-10.md    the diagnostic memo
docs/eval-resume-prompt.md          this file
docs/openrouter-tiers.md            cost-per-tier reference
docs/openrouter-probe.json          raw probe artifact
internal/eval/v2/cellresult.go      schema contract
internal/eval/v2/grid.go            grid runner (Cartesian + verify + retry + cortex)
internal/eval/v2/persist_cell.go    SQLite + JSONL persistence
internal/eval/v2/spend.go           multi-tier USD ceiling system
internal/eval/v2/harness.go         ResultfulHarness extension
internal/eval/v2/library_service_aider_harness.go  +SetModel, +--file auto-add, +stdout parser
pkg/llm/openrouter.go               Provider impl (TODO 2)
test/evals/coding/                  5 coding scenarios + seeds
test/evals/smoke/hello.yaml         smoke scenario
```

Latest commit: see `git log` — most recent commits all prefixed
`eval-harness:` or `docs(eval-harness-loop):`.
