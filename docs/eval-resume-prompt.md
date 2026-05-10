# Resumption prompt — picks up where the 2026-05-10 session left off

> Read this file end-to-end. It captures everything a fresh Claude
> Code session needs to continue the eval-harness work without
> rediscovery.

---

## The anchor — what we are trying to prove

**Thesis:** Cortex produces measurable lift on real coding tasks at
equal-or-lower token and dollar cost, on a smaller / cheaper model
than the baseline frontier.

This is the small-model amplifier claim from `CLAUDE.md` / the
project's product memo. Everything else (cell-result schema, grid
runner, multi-tier ceilings, the retrieval framework) is plumbing in
service of this question.

### Pass criteria for "real lift signal that translates to real-world usage"

A run lands as **decisive evidence** when *all four* of these are true
on a coding scenario set (not retrieval-only):

| dimension | pass threshold | why |
|---|---|---|
| **Quality** | Cortex pass-rate ≥ baseline pass-rate + **10 pp** at the same model | The amplifier must produce work that's actually better, not just different. Within-model variance on n≥15 scenarios is roughly ±5 pp, so 10 pp is the smallest interval where one seed isn't noise. |
| **Cost** | Cortex `cost_usd` per *passing* cell ≤ baseline `cost_usd` per passing cell | The pitch is "cheaper, not just additive context." If cortex adds tokens but doesn't increase quality enough to offset, that's a regression. |
| **Tokens** | Cortex `tokens_in + tokens_out` per cell ≤ 1.5× baseline | Static-cortex prefixes inflate the prompt; the floor is "you're not paying 3× to get 10% lift." |
| **Cross-tier** | Small-model + cortex ≥ next-tier-up baseline on the same scenarios | The thesis is *amplifier* — small-model + cortex reaches medium-model baseline territory, or medium + cortex reaches large-baseline territory. |

A run lands as **decisive falsification** when:
- Cortex pass-rate < baseline pass-rate - 5 pp (cortex actively hurts),
  *or*
- Cortex pass-rate ≈ baseline pass-rate AND cortex cost > 1.3× baseline
  (no benefit, real cost).

Anything between is **inconclusive** — needs harder scenarios, larger
sample, or different model tiers.

### Current state against the anchor

| dimension | status | evidence |
|---|---|---|
| Quality lift on coding | **❌ not yet** | 0 pp lift on 5 saturated coding scenarios (post `--file` fix) |
| Quality lift on retrieval | ✅ partial | +31% / +52% lift on Haiku × 15 scenarios; clean and noisy |
| Cost reduction | ❓ unmeasured | Per-passing-cell cost-per-quality comparison not yet run |
| Cross-tier amplifier | ❌ not yet | No experiment has shown small+cortex ≥ medium baseline |

We're at **mechanism-validated, outcome-unvalidated**. The retriever
works; we haven't yet shown it delivers the headline claim on coding
work at lower cost.

### What this means for the next experiment

**Every next experiment should be framed as "would this move the
status table above?"** If the answer is "no, it just adds more
mechanism testing," redirect.

The single experiment that would move the table the most is the
**library-service cumulative experiment** (see plan below). It tests
all four dimensions on one coding-task corpus, accumulates state
across sessions (the real-world shape), and has an existing scoring
rubric (shape similarity, naming adherence, smell density, test
parity, e2e pass rate). Bigger setup, but the only experiment likely
to land decisive evidence one way or the other.

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

## Open experiments — ranked by anchor-table impact

Re-read the anchor's status table above. Each experiment is rated by
which dimensions it could move. The bolded row is highest-leverage.

| Experiment | Quality | Cost | Tokens | Cross-tier | Effort | Est cost |
|---|:-:|:-:|:-:|:-:|---|---:|
| **Library-service cumulative** (see plan below) | ✅ | ✅ | ✅ | ✅ | Big | $3–5 |
| Full v2 retrieval sweep on Haiku | — | — | — | — | Tiny | $1.50 |
| Harder coding scenarios (hidden-context shape) | ✅ | — | — | partial | Medium | $0.50 |
| Real-store seeding (capture from session logs) | partial | — | — | — | Medium | $0.50 |
| Noisy-retrieval comparison | — | — | — | — | Tiny | $0.20 |
| Compare-runs tool | n/a | n/a | n/a | n/a | Small | $0 |

Legend: ✅ = experiment can directly move that anchor dimension;
partial = experiment provides side evidence but not the headline
metric; "—" = doesn't bear on that dimension.

**The library-service cumulative experiment is the only one that
could move all four anchor dimensions in a single run.** Everything
else either polishes mechanism evidence (retrieval sweeps) or chips at
one dimension. If you have budget for one experiment, run that one.

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
