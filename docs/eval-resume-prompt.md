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

## Second-order optimization — what investments compound

The current setup measures cortex on a hand-authored scenario set we
own. That's tractable but inherently shallow — every iteration's
upside is bounded by how much *more* scenario-authoring we do. The
bigger leverage is plugging into established benchmarks so cortex
lift becomes comparable to public numbers.

### Standard-benchmark integration (highest 2nd-order leverage)

The biggest force-multiplier we are *not* using:
**`lm-evaluation-harness`** (EleutherAI) — the framework the Hugging
Face Open LLM Leaderboard sits on. Defines 100+ standard tasks
(MMLU, GSM8K, ARC, HellaSwag, HumanEval, MBPP, TruthfulQA, ...) and
exposes a uniform `Model` adapter interface for any backend.

Why this is worth a day of effort:

1. **Battle-tested scenarios.** We stop hand-authoring; the field
   has curated thousands of tasks with care.
2. **Comparable numbers.** "Cortex lift on MMLU" is directly
   comparable to published RAG / fine-tuning / agent-framework
   numbers. Hand-authored scenarios aren't.
3. **Reproducibility.** Anyone with the cortex binary can re-run.
4. **Leaderboard path.** A "cortex-augmented model M" entry on the
   public leaderboard would be unambiguous social proof.

**Integration shape (~1 day):**

- `lm-eval` calls `LM.generate(prompt)` for each task item.
- Wrap our OpenRouter client + cortex injector as an `LM` subclass:
  for each prompt → `cortex search <prompt>` → prepend → route to
  OpenRouter → return completion.
- Run `lm-eval --model cortex-openrouter:haiku-4.5 --tasks humaneval`
  and `lm-eval --model openrouter:haiku-4.5 --tasks humaneval` (no
  cortex). Compare pass@1.

**On-point coding benchmarks to target first** (replacement for our
`test/evals/coding/` set, which saturated):

| benchmark | size | what it tests | why |
|---|---:|---|---|
| **HumanEval+** (EvalPlus) | 164 | Python function impl, pass@1 | The de-facto coding benchmark |
| **MBPP+** | 974 | Python basic programming | Larger sample, similar shape |
| **SWE-bench Lite** | 300 | Real GitHub bug fixes, resolution rate | Most realistic "agent does dev work" benchmark; aligns with the small-model amplifier claim |
| **MultiPL-E** | 164 × 18 lang | HumanEval translated (incl. Go) | Lets us test on our project's language |
| **LiveCodeBench** | rolling | Contamination-resistant | Detects "model memorized the benchmark" effect |

Among these, **SWE-bench Lite is the headline target** — its score
*is* the headline claim ("cortex makes small models resolve real
GitHub issues at a rate closer to frontier"). Other benchmarks are
supporting data.

### Other 2nd-order investments (smaller but real)

- **Variance bands on every metric.** Today we report point estimates
  from single seeds. Two more seeds per cell triples cost but gives
  ±σ. Without bands, every "lift" claim is ambiguous between effect
  and noise.
- **Pareto curve view.** Quality × cost is a curve, not a point.
  Cortex's value is moving us along that curve. Today's `--report-
  summary` shows the raw points but not the curve. A simple
  matplotlib export from JSONL would do it.
- **Statistical power calculator.** Before any expensive sweep, ask:
  given the effect size we expect (~10 pp lift), what n gives us
  80% power at α=0.05? Today we run n=15 with no power analysis.
- **CI-bound eval gate.** A nightly eval against a fixed scenario
  subset + a budget guard would catch regressions on every PR
  affecting cortex retrieval. Same scaffolding we already built.

---

## MECE coverage matrix — the experiment space

Three orthogonal axes define the design space. Today we've touched a
small corner of it. The matrix below makes the gap explicit.

### Eval shape × model tier (where we have evidence)

| shape | small (≤10B) | medium (10-70B) | large (70-200B) | frontier (≥200B) |
|---|---|---|---|---|
| A. Retrieval / QA | ✅ +52% | ✅ +20% | n/a | ✅ +31% |
| B. Single-shot coding | ✅ 0 pp (saturated) | ✅ 0 pp (saturated) | n/a | ✅ 0 pp (saturated) |
| C. Multi-session coding | ❌ | ❌ | ❌ | ❌ |
| D. Long-horizon agent | ❌ | ❌ | ❌ | ❌ |
| E. Standard benchmarks (lm-eval) | ❌ | ❌ | ❌ | ❌ |

C is the library-service shape. D is multi-tool-call agent work
(plan → execute → reflect → repeat). E is the lm-eval-harness path.
The cells most likely to show the small-model amplifier effect are
**C × small** and **E × small** — those are the bets.

### Cortex configuration (orthogonal to the above)

| config | what it is | tested |
|---|---|---|
| α. None (baseline) | No cortex in the loop | ✅ |
| β. Static bullets | Hand-authored `cortex_context:` per scenario | ✅ |
| γ. Reflex-mined | Real `cortex search` over a populated store | ✅ (retrieval evals only) |
| δ. Reflect-reranked | γ + LLM reranking of retrieved items | ❌ |
| ε. Full pipeline | γ + δ + Think (session learning) + Dream (idle mining) | ❌ |

We've only validated α and β at depth, with γ partial. δ and ε —
which are the *interesting* parts of cortex's architecture — have
zero eval coverage.

### Context source (the corpus side)

| source | description | tested |
|---|---|---|
| 1. Synthetic | Hand-curated `context:` items in scenario YAML | ✅ |
| 2. Real captures | Items captured from real dev sessions | ❌ |
| 3. Hybrid | Synthetic seed + real captures appended | ❌ |

(2) is the realistic shape — and the one most likely to invalidate
synthetic-corpus findings. Until we test on (2), every claim has the
caveat "in a synthetic store."

---

## Open questions to ask the user at session start

These are the choices that change which experiments are worth running.
Ask before committing to anything substantial.

1. **Audience for the numbers — publish vs. ship?**
   - Publish path → invest in lm-eval-harness integration, standard
     benchmarks, reproducibility.
   - Ship path → invest in scenarios that mirror this team's actual
     development work; standard benchmarks become less load-bearing.

2. **Frontier comparison budget?**
   - Sonnet/Opus/GPT-5 comparisons each cost ~$5-15 per full sweep
     and need `CORTEX_EVAL_ALLOW_FRONTIER=1`. The cross-tier
     amplifier claim needs them, but they're not "free" the way
     Haiku comparisons are.

3. **Real-corpus seeding — clean-room or this project?**
   - This project has session logs, git history, captured events.
     Using them as the cortex corpus tests the live system. But it
     biases toward "cortex helps because it has memorized this
     codebase" — fine for a product claim, questionable for a paper.

4. **lm-eval integration depth?**
   - Light (export our results to lm-eval-compatible JSON) — easy,
     limited cross-comparability.
   - Deep (cortex as a real `lm-eval --model cortex-...` adapter) —
     1 day, unlocks everything downstream.

5. **Target — beat a specific number, or characterize the system?**
   - "Beat number" → pick a benchmark, optimize until cortex wins.
   - "Characterize" → run a wider matrix to map where cortex helps
     and where it doesn't. Different experiments.

6. **CI-integrated eval?**
   - Every PR runs a small eval subset with a budget guard. Catches
     regressions but adds friction. Worth doing only after a stable
     methodology.

7. **Variance budget?**
   - Single seed per cell is fast but noisy. 3 seeds × 5 scenarios
     gives confidence intervals. Tripling cost for that confidence
     may or may not be worth it depending on (1).

8. **Comparison-against-what for "lift"?**
   - Today: cortex vs. *nothing* (no system prompt augmentation).
   - More fair: cortex vs. *a good engineered system prompt that
     includes project conventions*. That's the alternative cortex
     is competing with in practice.

---

## What would change the answer to "does cortex translate to real-world usage" (recap)

Currently 30% there. The full check:

| dimension | how to move it from ❌ to ✅ |
|---|---|
| Quality lift on coding | Library-service experiment OR SWE-bench Lite run |
| Cost reduction | Add per-passing-cell cost view to `--report-summary` AND run with cost-aware scenario set |
| Cross-tier amplifier | Run small (≤8B) cortex vs. medium (Haiku) baseline on same scenarios |
| Real-store conditions | Seed cortex from this project's actual session logs, run any eval |
| Reproducibility / external trust | lm-eval-harness adapter |

Any single one of these moves us from "interesting plumbing" → "evidence
worth showing someone else." The library-service experiment is the
single highest-value next move because it could move the first three at
once.

---

## Multi-loop architecture — parallel work via shared state

Single-loop linear iteration was the right shape for the build phase.
Once the harness is stable, the bottleneck shifts: how many
independent threads of work can run in parallel without losing
coherence? The answer is **N specialized loops, each with its own
ScheduleWakeup cadence, communicating through shared state in
`.cortex/db/`**.

The pattern is *not* one loop juggling multiple concerns. It's N
single-concern loops that read+write the same SQLite store, with
atomic claims for work items and the same cost-ceiling table guarding
all of them.

### Suggested loop roster

| loop | cadence | what it does | reads | writes |
|---|---|---|---|---|
| `experiment` | hourly | Pull from `experiment_queue`, run a sweep | queue table | `cell_results`, `daily_spend` |
| `coverage` | weekly | Author new scenarios filling MECE matrix gaps | gap-tracker file | new scenario YAMLs |
| `capture` | continuous (Monitor) | Tail conversation logs / git activity → durable cortex captures | session JSONL, git log | cortex store |
| `analysis` | hourly | Recompute lift / Pareto / variance / regression alarms | `cell_results` | reports + charts |
| `watch` | daily | Check HF Open LLM Leaderboard, OpenRouter catalog for new models worth adding to the matrix | external APIs | gap-tracker file |
| `driver` | per-tick | Read all of the above; update the anchor status table; emit user-facing summary | everything | the resume prompt itself |

Most projects don't need all six. The minimum useful set is
`experiment` + `analysis` + (optionally) `capture`.

### Shared-state mechanics

- **`experiment_queue` table** (new, small migration in
  `persist_cell.go`): rows are work items with `kind`, `params_json`,
  `status` (`queued`/`in_progress`/`done`/`failed`), `claimed_by`,
  `created_at`/`claimed_at`/`completed_at`. Atomic claim:
  `UPDATE experiment_queue SET status='in_progress', claimed_by=?
   WHERE id = (SELECT id FROM experiment_queue WHERE status='queued'
   ORDER BY id LIMIT 1)`.
- **`cell_results` + `cell_results.jsonl`** stay authoritative. Loops
  filter by `timestamp` / `run_id` for their slice.
- **`daily_spend` table** is *the* coordination point for cost — all
  loops see the same total, so the ceiling guard is global.
- **`gap-tracker` file** (new — markdown or YAML) tracks which MECE
  cells are filled. `coverage` and `watch` write; `experiment`
  reads to prioritize.
- **Lock file** (`.cortex/db/<loop>.lock`) for any non-DB shared
  resource (e.g., the cortex daemon when a loop wants exclusive
  access).

### Anti-patterns

- **Duplicate runs.** Two loops claim the same queue row. Fix: the
  atomic-claim SQL above.
- **Schema drift.** One loop's `CellResult` disagrees with another's.
  Fix: `SchemaVersion` check on read; loops refuse to process rows
  with a different version.
- **Cost leakage.** N parallel sweeps blow the daily ceiling. Fix:
  the existing `CORTEX_EVAL_DAILY_USD_CEILING` enforcement already
  reads `daily_spend` — every loop's spend counts against the same
  pool. As long as each loop uses `PersistCell` and the spend
  tracker, the ceiling holds globally.
- **Drift in human-facing priorities.** Each loop iterates on its
  slice; "what should we do next?" gets lost. Mitigation: the
  `driver` loop is the only one that writes the resume prompt's
  anchor status — single source of truth.
- **Hidden serialization.** SQLite WAL handles concurrent readers
  + one writer; multiple writers serialize. With 6 loops, sustained
  write contention is unlikely but possible. Fix: short transactions,
  retry on `SQLITE_BUSY`.

### When a new loop is worth standing up

- The work has a natural rhythm distinct from existing loops (e.g.
  `capture` is event-driven; `experiment` is queue-driven; `watch`
  is calendar-driven).
- The work has independent failure modes — one stuck loop shouldn't
  freeze others.
- The work can be expressed as "given state X, produce state Y" —
  not as a one-time imperative.

### When *not* to spawn a loop

- The work runs once and stops. Use a one-shot script.
- The work needs human input at every step. Loops are for
  autonomous iteration with human course-corrections, not for
  human-in-the-loop work where every step blocks on review.
- Two existing loops could do it with a small extension. Loops are
  cheap but not free — each adds a coordination surface.

### Wiring it up (small, additive)

Concretely, what would have to land before this is real:

1. New table + migration in `persist_cell.go`:
   `experiment_queue` (~30 LOC).
2. CLI: `cortex eval queue <add|claim|complete|list>` — operations on
   the queue. ~50 LOC.
3. Each loop is its own `/loop @docs/loops/<name>.md` prompt file in
   a new `docs/loops/` directory. The prompts encode that loop's
   single responsibility + how it reads/writes the shared state.
4. The `driver` loop's prompt is mostly "read the others' last
   outputs and update the anchor status table." It's the
   meta-loop.

None of this is on the critical path until the eval mechanism itself
is shipping value. But it's the natural shape the system grows into
once it does.

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
