# Cortex Eval Strategy

> **Authoritative.** This doc is the north star for evaluation work in
> Cortex. When `CLAUDE.md`, `ROADMAP.md`, `docs/benchmarks/coverage-matrix.md`,
> or any per-benchmark doc conflicts with this one, this one wins.
>
> **Living doc.** Update when a tier definition changes, a claim is added or
> retired, or an eval moves between tiers. Run-level findings stay in
> [`eval-journal.md`](eval-journal.md), not here. This doc tracks *what we
> measure and why*, not *what the latest numbers are*.

---

## Scope (locked)

Cortex is **a general-purpose coding harness that leverages multiple models,
learns over time, and has bounded emergence.**

Three claims follow from that scope, and every eval is justified by which
claim it tests (or which baseline competence it guards):

1. **Multi-model leverage.** A small model + Cortex matches or exceeds a
   bigger model alone, at lower cost. The metric of record is *quality
   normalized by model size or dollars spent*, never absolute pass-rate.
2. **Learning over time.** The same harness, same model, on a sequential
   workload produces higher quality on later sessions than earlier ones.
   The metric is the slope of the learning curve, not a single snapshot.
3. **Bounded emergence.** The DAG seed+grow+decay model produces
   task-appropriate complexity: cheap tasks → small graphs, complex tasks
   → larger graphs, with quality flattening at a knee rather than blowing
   up unbounded. The metric is the budget–quality curve.

These three claims are what Cortex is *for*. Everything else the harness
does (intent-ingress, in-flight observability, safety, presentation) is
*table stakes for being a coding harness at all* — necessary but not
differentiating.

---

## The three tiers

Every eval has one of three jobs. Mixing them up is how eval work loses
focus.

### Tier 1 — Baseline competence

**Job.** Prove Cortex is a real coding harness. Stand on the same
leaderboards as Claude Code, Cursor, Aider. Don't be embarrassing.

**Metric of record.** *Not* absolute pass-rate. Compete on:
- pass-rate at a fixed (small) model size
- pass-rate per dollar spent
- pass-rate per second of wall-clock

A 38% score at 1/10 the cost of a frontier-only run is a defensible
claim. A 60% score against a 70% leader is not.

**Risk to manage.** Optimizing for SWE-bench's raw grader converges Cortex
on being a slightly-better retriever. The grader doesn't reward the
differentiated work (Dream, learning, multi-model routing). Use Baseline
evals to *prove competence*, not to *optimize against*. Optimization
targets live in Tier 2.

**Wrapped today.**

| Eval | Dimension covered | Notes |
|---|---|---|
| SWE-bench Verified | Grounding, Execution | Primary baseline; report cost-normalized |
| LongMemEval | Memory & continuity | Chat-shaped; *not coding-shaped* — see Tier 2 learning-curve |
| NIAH | Within-session memory floor | Substrate check |
| MTEB | Embedding quality | Substrate check |

**Roadmap (deferred but in this tier).**

| Eval | Dimension | Status |
|---|---|---|
| BFCL | Execution breadth, tool-calling | Stage 2 — live split for contamination resistance |
| τ-bench | Planning, policy adherence | Stage 2 — pairs with MINT |
| MINT | Steering & interrupt | Stage 2 — shares simulated-user driver with τ-bench |
| AgentDojo | Safety, prompt-injection | Stage 2 |

### Tier 2 — Thesis evals (the differentiated work)

**Job.** Prove the three claims. These are the evals only Cortex needs to
care about, because they directly test what Cortex specifically does.

**Metric of record.** Curves and deltas, not single numbers. Single
numbers are vanity; curves are diagnostic.

#### 2a. Learning-curve eval — proves *learns over time*

**Shape.** SWE-bench instances (or library-service-style multi-session
scenarios) run *in sequence* with shared Cortex memory carried forward.
Plot pass-rate as a function of session index.

**What it answers.**
- Does the harness get measurably better on session N than session 0?
- Where does the slope flatten or regress? (That's where the
  forgetting/consolidation policy needs work.)
- Does the slope replicate on different model families?

**Why no existing benchmark covers this.** Every public leaderboard
treats each instance as independent. The learning-curve framing is the
eval-shaped contribution Cortex makes — turning a one-shot benchmark into
a sequential one.

**Substrate.** Reuses SWE-bench / library-service corpora. New code is
the session-ordering driver + a memory-isolation guarantee (each
learning-curve run starts from a known memory state, not from prior
runs' leftover state).

#### 2b. Budget–quality curve — proves *bounded emergence*

**Shape.** Run the same task at varying DAG budgets (small, medium,
large, unbounded). Plot output quality as a function of total budget
consumed (nodes spawned, tokens, dollars, wall-clock — pick one or report
several).

**What it answers.**
- Where is the knee? (Past the knee, more budget buys nothing — that's
  the seed+grow+decay model working correctly.)
- Does the knee position track task complexity? (Cheap tasks → early
  knee; complex tasks → later knee.)
- Does quality ever *decrease* with more budget? (If yes, the DAG is
  growing past its useful range.)

**Replaces.** ABR-as-ratio (`quality(Fast+Think) / quality(Full)`) is
retired. ABR's underlying question — does the cheap path approach the
expensive path — is preserved as a special case of the budget-quality
curve (the ratio of quality at two specific budget points).

**Caveat on assumed monotonicity.** "Quality goes up as Cortex memory
matures" is the hypothesis. The *metric* is the curve; whether it goes up
is the result you report. Don't pre-bake the conclusion into the eval
design.

#### 2c. Multi-model cost/quality delta — proves *multi-model leverage*

**Shape.** Paired runs on the same corpus:
- `small_model alone` vs `small_model + Cortex`
- `small_model + Cortex` vs `frontier_model alone`
- `multi-model + Cortex routing` vs `frontier_model alone`

Report the cost-quality Pareto frontier across the comparison set.

**What it answers.**
- Does Cortex amplify small models toward frontier performance?
- At what cost ratio does (small + Cortex) match (frontier alone)? Below
  1.0 = the amplifier claim is real.
- Does the routing policy (which sub-task → which model) measurably beat
  any single-model strategy?

**Substrate.** Reuses SWE-bench / BFCL corpora; new code is the
paired-run harness and the routing-policy comparison driver.

### Tier 3 — Regression guardrails

**Job.** Catch silent degradation across the 10 coding-harness UX
dimensions. These evals don't need to *win*; they need to not *regress*.

**Metric of record.** Pass/fail thresholds, not score optimization. A
regression eval that's worth $0.10/run and runs weekly is doing its job;
one that costs $10/run and runs quarterly is not.

**Why this tier exists.** A harness that posts world-class numbers on
Tier 1 and Tier 2 evals can still ship with a broken clarifier, a
silent tool-call, or a presentation step that lies about what changed.
Without Tier 3, those break invisibly between releases.

**Mapped from the coverage matrix.**

| Dim | Proxy | Status |
|---|---|---|
| 1. Intent ingress | Synthetic ambiguous-prompt corpus + LLM-judge | Stage 3 |
| 6. In-flight observability | Mechanical event-stream cadence/density/coverage check | Stage 3 — depends on CLI event-stream surface |
| 7. Steering (mid-flight) | Subset of MINT, scored as regression not optimization | Stage 2 |
| 8. Safety / destructive ops | Coding-specific destructive-op corpus (`rm -rf`, force-push, `DROP TABLE` — does the agent execute without asking) | Stage 3 |
| 9. Presentation | LLM-judge over end-of-turn summary vs actual diff | Stage 3 |
| 10. Extensibility | Custom MCP server with a tool the model couldn't have memorized | Stage 3 |

These are not deferred-because-unimportant. They are *regression
guardrails*, and the priority is **building cheap mechanical versions
of each before any one of them silently breaks**, not building the most
sophisticated version of any single one.

---

## Mapping: every existing eval surface → which tier

This is the audit. Every directory under `internal/eval/` and
`test/evals/` is accounted for.

| Surface | Tier | Status | Notes |
|---|---|---|---|
| `internal/eval/benchmarks/` (swebench, longmemeval, niah, mteb) | 1 | Keep | Baseline competence. Report cost-normalized. |
| `internal/eval/mechanic/` + `test/evals/mechanic/` | 2 (substrate for 2b) | Keep | Tests DAG executor directly. The budget-quality curve runs on this substrate. |
| `internal/eval/v2/` + `test/evals/v2/` | 2 (substrate for 2a, 2c) | Keep, reshape | Library-service runner becomes the learning-curve runner; ABR session files (`abr_session*.go`) downgraded to internal sanity check, not a metric of record. |
| `internal/eval/journey/` + `test/evals/journeys/` | 2 (2a) | **Repurpose** | Multi-session shape is exactly what learning-curve needs. Either rename/reshape or extract scenarios into the v2 runner; finish or remove the half-built execution adapter. |
| `internal/eval/legacy/` + `test/evals/legacy/` | — | **Retire** | Tests the superseded cognitive-mode abstraction (Reflex/Reflect/Resolve/Think/Dream in isolation). The DAG executor + mechanic runner replace this. ≈ 204K removable. |
| `internal/eval/dagtrace/` | 2 (substrate for 2b) | Keep | DAG trace infrastructure; needed for budget-quality curve plotting. |
| `test/evals/coding/`, `test/evals/library-service/` | 2 (2a, 2c) | Keep | Corpus for thesis evals. |
| `test/evals/projects/` | — | Audit | 1.7M; verify what consumes it. If orphaned, retire. |
| `test/evals/corpus/`, `test/evals/e2e/` | — | Audit | 24K + 8K; small but verify purpose. |

---

## What this strategy is *not*

- **Not a complete coverage doctrine.** Cortex doesn't need 10/10
  dimensions to ship. It needs Tier 1 floor + Tier 2 differentiation +
  Tier 3 guardrails on the dimensions where the architecture should
  differentially help (memory, planning, observability, extensibility,
  steering — i.e. dimensions 2, 3, 4, 6, 7, 10).
- **Not a quarterly rebuild.** Tiers and claims are stable. Specific
  benchmarks rotate as the field evolves; the *shape* of the strategy
  shouldn't churn.
- **Not a substitute for the live coverage matrix.** That doc tracks
  *which benchmarks are wrapped* and *which CLI surfaces are missing*.
  This doc tracks *why each tier exists and what claim each eval
  serves*. Both are needed; they don't overlap.

---

## Open questions

- **Sequential-run isolation.** Learning-curve evals need per-run memory
  isolation (start from a known state). How to express "this run starts
  from snapshot X" without ad-hoc DB resets? Likely a `journal replay`
  primitive — coordinate with `cmd/cortex/commands/journal.go`.
- **Routing-policy ablation.** Multi-model delta wants to compare
  different routing policies, not just different model mixes. Does the
  current DAG node-selection logic expose policy as a swappable
  component? Verify before building.
- **Judge-variance budget for Tier 3.** LLM-judged regression proxies
  cost real money; setting per-proxy variance targets and re-running on
  drift is a meta-cost. Decide at build time per proxy.

---

## See also

- [`benchmarks/coverage-matrix.md`](benchmarks/coverage-matrix.md) — the
  10-dimension framework this strategy maps tiers onto.
- [`prompts/eval-principles.md`](prompts/eval-principles.md) — the nine
  principles every wrapped benchmark must satisfy (still authoritative).
- [`eval-journal.md`](eval-journal.md) — run-level findings.
- [`dag-protocol.md`](dag-protocol.md) — the runtime protocol that Tier
  2b's budget-quality curve measures.
