# Cortex Integration Roadmap

> **Purpose.** A unified plan for bringing Cortex's cognitive architecture,
> UX eval coverage, and tool-surface contract into one well-integrated coding
> harness. This doc is the synthesis above four more specific docs:
> [`ROADMAP.md`](../ROADMAP.md) (ABR-driven phases),
> [`docs/benchmarks/coverage-matrix.md`](benchmarks/coverage-matrix.md)
> (10 UX dimensions), [`docs/tool-surface.md`](tool-surface.md) (6 per-call
> axes), and [`docs/dag-protocol.md`](dag-protocol.md) (the runtime protocol
> Phase 5 lands). Each of those docs tracks its own slice; this one tracks
> how they fit together and what order to land them in.
>
> **Living doc.** Update phase status as work lands. Detailed per-benchmark
> tracking stays in `coverage-matrix.md`; per-axis engineering stays in
> `tool-surface.md`; this doc only updates when an *integration boundary*
> changes — e.g. a cortex function becomes wired into the harness, a new
> framework is added, or a phase boundary moves.

---

## The unified picture

The Cortex coding harness can be modeled at three altitudes, each MECE in
its own right, each describing the same system from a different angle:

```
        [10 UX dimensions]  ← acceptance tests (what the human judges)
                ↑
                |  externalized behavior of...
                |
    Sense → Attend → Represent → Remember → Model → Value → Decide → Act → Maintain
                         (graph, not pipeline; reentrant; any edge)
                                            ↕
                                       [Modulate]
                                            ↓
                                     [6 tool axes]  ← per-call engineering contract
                                       governs every Act invocation
```

- **Cortex functions (architecture)** — what cognitive function is computing at
  each step. Sense → Attend → Represent → Remember → Model → Value → Decide
  → Act → Maintain, with Modulate as a cross-cutting gate.
- **10 UX dimensions (acceptance)** — what a human can observe and judge
  about a session, from intent ingress through result presentation.
- **6 tool axes (contract)** — what every Act invocation must guarantee:
  Contract, Authorization, Dispatch+Execution, Result, State/side-effects,
  Observability+Budget.

The three are not redundant. They are MECE across altitudes. The
architecture is *what computes*. The dimensions are *what an external
observer judges*. The axes are *what every tool call must guarantee* for
the architecture to hold together.

## The graph view (not a pipeline)

The arrows in the diagram above suggest a feed-forward flow. They are
shorthand. The real model: every cortex function is a *node* in a graph; any
node can call any other node; Modulate sits over every edge as a budget
gate. The current `pkg/cognition/` API exposes Reflex/Reflect/Resolve/Think
/Dream as pipeline stages with fixed Fast and Full paths — that is the
simplification Phase 5 of this roadmap eliminates.

A few examples of dynamic edges that the pipeline view hides:

- Reflect spawns a fresh Reflex query when it spots an unresolved contradiction.
- Resolve calls Reflect mid-decision when its own confidence is low.
- Dream enqueues Think tasks when it finds material worth pre-warming.
- Modulate dampens depth on every edge when the budget is exhausted.

The graph view changes how cognitive nodes compose. The pipeline view is
correct for the simple paths and useful as documentation; it is wrong as
the API.

## What Cortex actually is (positioning that falls out)

Cortex is the **Sense + Attend** function the host harness (Claude Code,
Aider, opencode) doesn't have. The interpretive stage that reads the
user's prompt, scores salience over the journal + project state, and
surfaces the top-k into context *before* the model's Decide function runs.

That "memory system" is incidental — the substrate happens to be an
event-sourced journal, but the *function* is salience over substrate.
Tool use (`cortex_search`) is Act, not Sense — the model reaching for
data. Cortex's distinctive contribution is the Attend step that runs
before any tool call is needed at all.

This positioning is the through-line of the roadmap: every phase either
sharpens the Sense+Attend function, instruments it, or measures it.

---

## Framework triangulation

### Cortex function → UX dimension

Each cortex function's externalized behavior is judged by one or more of the
10 UX dimensions in `coverage-matrix.md`:

| Cortex function | Primary UX dimension(s) | How |
|---|---|---|
| Sense | 1 Intent ingress | Prompt arriving cleanly; clarifying-question quality |
| Attend | 2 Grounding, 3 Memory & continuity | Salience selects what to surface from both |
| Represent | 2 Grounding | Encoding repo state as patterns the model can use |
| Remember | 3 Memory & continuity | Direct |
| Model | 4 Planning & alignment | Forward simulation of plan against policy |
| Value | 4 Planning, 8 Reversibility & safety | Scoring options against goals and risk |
| Decide | 4 Planning, 7 Steering & interrupt | Action selection; accepting mid-turn redirects |
| Act | 5 Execution | Direct |
| Maintain | 3 Memory (consolidation half) | Offline consolidation; what persists between sessions |
| Modulate | 6 In-flight obs, 8 Safety, 9 Verification | Global gating, budget visibility, confirmation |
| (cross) | 9 Presentation, 10 Extensibility | Cross-cutting; emerge from the whole system |

Note: Dimension 1 (Intent ingress) maps onto Sense+Attend exactly. The
coverage matrix marks dim 1 as "GAP, no upstream" — meaning no upstream
benchmark exists. That gap is *Cortex's home turf*: the small-model
salience function being built (see `MEMORY.md` direction docs) is the
in-house answer to that benchmark gap.

### Cortex function → tool axis

The 6 axes from `tool-surface.md` apply to **Act specifically**, plus the
edges around it:

| Tool axis | Cortex-function role | Notes |
|---|---|---|
| 1 Contract | Inputs to Decide | What tools the model sees in its prompt |
| 2 Authorization | Modulate gate on Act | Read vs. mutator allowlist |
| 3 Dispatch + Execution | Act mechanics | Running the tool; timeout, exit codes |
| 4 Result | Act → Sense feedback | The envelope by which tool results re-enter context |
| 5 State / side-effects | What Act mutates | Journal entries, file writes, destructive ops |
| 6 Observability + Budget | Modulate sensors over Act | Per-call telemetry; budget gating |

So the tool-surface doc is fully captured by saying:
**Modulate must instrument and gate every Act invocation, and Act's Result
must be machine-clean so Sense can ingest it without ambiguity.**

### UX dimension → tool axis

The 10 dimensions and 6 axes are nearly orthogonal. The dimensions judge
*emergent session behavior*; the axes govern *per-call engineering
contract*. A dimension can fail even when every tool call is axis-clean
(e.g. dim 4 Planning can fail because Decide chose badly, with no axis
violation). The most load-bearing overlap is dim 6 (in-flight
observability) ↔ axis 6 (per-call observability): the event stream
required for dim 6 is exactly the per-call telemetry required for axis 6.
Land axis 6 first and dim 6 falls out as instrumentation.

---

## What's missing across all three frameworks

When all three frameworks are taken together, four gaps are visible that
none of them surface alone:

### G1 — Model function is absent in Cortex

No forward simulation, no turn-ahead prediction. Dream is post-hoc pattern
extraction; Think is descriptive (TopicWeights, cached Reflect results).
The 10 dimensions don't quite force a Model function — Planning (dim 4)
measures *commitment-to-plan*, not predictive modeling. But the graph view
needs Model as a callable node (so Resolve can ask "what's the likely
outcome if I act now?"). Currently no node answers that.

### G2 — The cognition API doesn't reflect the graph

`pkg/cognition/` exposes modes as pipeline stages with two prescribed
retrieval paths (Fast, Full). Reentrant composition between modes is not
expressible — Reflect cannot trigger a fresh Reflex pass, Resolve cannot
deepen by calling Reflect again, etc. This blocks the dynamic-graph
behaviors that justify having distinct modes in the first place.

### G3 — No Sense → Attend edge wired into the harness loop

Reflex exists in `pkg/cognition/`. It is not auto-running before each turn
to inject salient context. The model has to remember to call
`cortex_search` instead. This is the single highest-leverage missing edge
in the entire picture: wiring it collapses chunks of dims 2 (Grounding)
and 3 (Memory) from "model must remember to ask" to "context just appears
in the prompt."

### G4 — Tool-surface axes 1, 4, 6 unmet

Per `tool-surface.md`'s "Current gaps" section: no generated `tools.json`
manifest, no uniform `{ok, data, error, meta}` envelope, no unified
`cell_results.jsonl` for ad-hoc CLI invocations (only eval cells emit
structured telemetry). This means instrumentation is asymmetric between
"inside an eval" and "outside an eval" — fatal for measuring whether any
later integration helps.

---

## The cohesive roadmap (6 phases + 1 reconciliation)

Phases are ordered by **strict dependency**, not by calendar. Skipping
ahead breaks measurement: every phase produces a measurable delta in the
coverage matrix, and the deltas are only interpretable if earlier phases
have landed.

### Phase 0 — Reconciliation pass (small, one-time)

**Goal.** Make the three framework docs reference each other and share
vocabulary so future updates stay coherent.

**Deliverables.**

- [ ] Add a "framework triangulation" subsection to `docs/benchmarks/coverage-matrix.md`
      linking to this doc + `tool-surface.md`.
- [ ] Add a "framework triangulation" subsection to `docs/tool-surface.md`
      linking to this doc + `coverage-matrix.md`.
- [ ] Add a "cortex function" column to the coverage matrix's per-dimension
      table so each dimension's owning architectural function is visible at
      a glance.
- [ ] Cross-link this doc from `ROADMAP.md` so the ABR-driven phases and
      this integration view are mutually discoverable.

**Success criteria.** A reader landing on any one of the three docs can
find the other two in two clicks.

**Dependencies.** None.

---

### Phase 1 — Tool-surface foundation (closes axes 1, 4, 6 → closes G4)

**Goal.** Land the instrumentation and contract floor that every later
phase depends on. Without this, no later change is honestly measurable.

**Deliverables.**

1. **`tools.json` generator** (axis 1 Contract).
   - Generated from the cobra command tree at build time.
   - Includes name, JSON schema, description, version field.
   - CI diff that fails when the surface changes without a version bump.

2. **Uniform result envelope** (axis 4 Result).
   - `{ok, data, error, meta:{trace_id, latency_ms}}` shape.
   - Wrapper applied to every `--json` output.
   - Explicit `truncated:true` flag where output is capped.
   - Path-redaction outside `.cortex/` + project root.

3. **Unified `cell_results.jsonl`** (axis 6 Observability).
   - Every CLI invocation writes a row, not just eval cells.
   - Schema includes cortex-function tag, tool name, latency, exit, tokens.
   - Default location: `.cortex/db/cell_results.jsonl`.
   - Existing eval rows continue to land there unchanged.

**Success criteria.** Running the same `cortex search` query inside an
eval cell and outside one produces structurally identical telemetry rows.

**Dependencies.** Phase 0.

**Why first.** The "structured eval outputs required" feedback direction
demands this; nothing else can be honestly measured until it lands.

---

### Phase 2 — CLI surfaces for upstream integration

**Goal.** Verify and close the CLI gaps that block the upstream benchmark
wave in Phase 4. This is `coverage-matrix.md` stage 1 made explicit.

**Deliverables.**

1. **Event-stream output** (`cortex code --events <file>` or `--stream`
   NDJSON).
   - Required event types: `tool_call_start`, `tool_call_end`,
     `text_delta`, `decision`.
   - Pre-events emit *before* a tool fires (so a human can read and
     interrupt).
   - Feeds axis 6 telemetry + unblocks dim 6 (in-flight observability).

2. **Multi-turn driver mode** for `cortex code`.
   - "Advance one turn with a new user message" as a black-box CLI op.
   - Required by τ-bench, MINT (dims 4, 7).
   - May already exist via in-process harness — verify before building.

3. **MCP server registration** via CLI flag.
   - `cortex code --mcp-server <url>` or equivalent.
   - Required for dim 10 (Extensibility) proxy.

4. **Confirmation-gate flag** for destructive operations.
   - Lets benchmarks test both gated and ungated behavior.
   - Required for dim 8 (Reversibility & safety) testing.

**Success criteria.** Each item resolves to either "already satisfied
(linked PR/commit)" or "filed issue, scoped change." Output: a short
addendum to `coverage-matrix.md` listing the resolution for each.

**Dependencies.** Phase 1 (so event-stream telemetry routes through the
unified `cell_results.jsonl`).

---

### Phase 3 — Wire the cognitive graph into the harness loop (closes G3)

**Goal.** Make the cortex functions act on the harness, not sit beside it.
This phase delivers the architecture; without it, Cortex's cognitive
modes are a library no one calls.

Ordered by leverage:

#### 3a. Sense → Attend edge (highest leverage; ship first)

Auto-inject Reflex results into every turn's context **before** the model
runs Decide. The user prompt arrives → Reflex scores journal entries →
top-k inserted as system context → model sees them without needing to
call `cortex_search`.

- Configurable top-k and budget; defaults from `pkg/config/`.
- Tag injected context with a stable marker so Phase 3b can detect use.
- Falls back gracefully when journal is empty.
- Default on; `--no-auto-context` flag to opt out.

#### 3b. Act → Value closure

Every auto-injection is followed by an outcome record. Did the model use
the injected context (cite it, call a tool matching it, mention a fact
from it)?

- Citation detection via a small LLM judge or heuristic.
- Result lands in a `feedback.confirmation` or `feedback.retraction`
  journal entry per the existing eight writer-classes.
- Becomes the supervision signal Reflex tunes against — the third tier
  from `docs/emergence-evals.md` (auto-tuning eval gate).

#### 3c. Modulate exposed to the loop

Budget state becomes visible to the loop. When Think budget is exhausted,
the loop knows to skip reranking this turn and serve cached Reflex
results. When Reflex budget is exhausted, the loop knows to inject fewer
items.

- Budget state visible in the event stream (axis 6 + dim 6).
- Over-budget returns `error: budget_exceeded` rather than blocking,
  per `tool-surface.md` axis 6.

#### 3d. Maintain → Sense edge

Dream-discovered insights enter ProactiveQueue. The Reflex pre-context
pass surfaces them opportunistically on the next turn that touches a
related topic. Maintain output becomes Sense input without requiring
explicit retrieval.

**Success criteria.** Re-running the LongMemEval suite shows Cortex's
auto-injected context being used by the model on a measurable fraction
of turns where it was load-bearing, with lift over baseline. Threshold
TBD after the Phase 3a baseline run lands; record in `eval-journal.md`.

**Dependencies.** Phase 1 (telemetry), Phase 2 (event stream for 3c
visibility).

---

### Phase 4 — Upstream benchmark wave

**Goal.** Lift coverage from 3-of-10 dimensions to 9-of-10 by integrating
the upstream benchmarks listed in `coverage-matrix.md` stages 2 and 3.

**Deliverables (per coverage-matrix.md stage 2).**

1. **τ-bench wrapper** → dimension 4 Planning. License: MIT. Source:
   `sierra-research/tau-bench`.
2. **MINT wrapper** → dimension 7 Steering. License: MIT. Source:
   `xingyaoww/mint-bench`. Shares simulated-user driver with τ-bench.
3. **AgentDojo wrapper** → dimension 8 Reversibility & safety. License:
   Apache 2.0. Source: `ethz-spylab/agentdojo`.
4. **BFCL wrapper** → dimension 5 breadth + dimension 10 partial.
   Source: `ShishirPatil/gorilla` (Apache 2.0). Start with `live` split
   for contamination resistance.

**Deliverables (per coverage-matrix.md stage 3).**

5. **Intent-ingress proxy** → dimension 1. Cortex-built; no upstream
   exists. Synthetic ambiguous-prompt corpus + LLM-judge rubric +
   simulated-user response. This is where the Sense+Attend function Cortex
   is building shows up as a measurable benchmark.
6. **In-flight observability proxy** → dimension 6. Mechanical; not
   LLM-judged. Depends on Phase 2 event-stream surface.
7. **Presentation judge** → dimension 9 (presentation half). LLM-judge
   over end-of-turn summary vs. actual diff.

**Success criteria.** 9 of 10 dimensions have at least one wrapped
benchmark producing structured `CellResult` rows. Each integration
satisfies all nine principles in `docs/prompts/eval-principles.md`.

**Dependencies.** Phase 2 (CLI surfaces), Phase 3 (so what's measured is
the wired-up architecture, not the bare loop).

---

### Phase 5 — Ship the DAG protocol + e2e scenario (closes G1, G2)

**Goal.** Replace the pipeline-shaped cognition API with the per-turn
DAG protocol — **seed + grow + decay**, no upfront emission, no
separate planner. The full design lives in
[`dag-protocol.md`](dag-protocol.md); the stage-by-stage build sits
in [`dag-build-plan.md`](dag-build-plan.md); the eval prep that
gates the build is in [`eval-prep-epic.md`](eval-prep-epic.md).

**Build sequence:** [`eval-prep-epic.md`](eval-prep-epic.md) →
[`dag-build-plan.md`](dag-build-plan.md) → implement. The eval prep
is the verification floor — without it, every claim "this got better"
is unmeasurable.

Once the protocol is live, the addressable-node refactor, the Model
function, the graph-walker, and the budget gate fall out of the
protocol — they aren't separate items. The work in this phase is
shipping the runtime and one e2e scenario that exercises it across all
10 dimensions.

**Deliverables.**

1. **Node registry** in `pkg/cognition/registry.go`.
   - Replaces per-mode constructor surface.
   - Each existing mode (Reflex, Reflect, Resolve, Think, Dream) gets
     re-exposed as one or more registered ops under their cortex
     function category.
   - Spec includes axis-contract for `act`-typed nodes, `Cost` hint,
     `MaxFanout`, and the handler signature `(ctx, in, budget) →
     (out, spawn, cost_consumed)`.

2. **Budget model** under `pkg/cognition/dag/budget.go`.
   - Three axes: `latency_ms`, `tokens`, `depth`.
   - Per-DAG-type seeds + initial budgets, loaded from `pkg/config`
     (turn / think / dream / capture / eval).
   - Decay mechanics + hard depth cap + per-spawn Modulate gate.

3. **Seed-and-grow executor** under `pkg/cognition/dag/executor.go`.
   - Maintains the pending set; walks topologically; schedules
     handler-returned `Spawn` specs; runs independent siblings in
     parallel where budget allows.
   - Enforces depth cap and budget exhaustion (in-flight finishes;
     new spawns refused; exhausted-axis event emitted).
   - Writes per-node `cell_results.jsonl` rows with `parent_node_id`
     so the post-hoc tree is reconstructable.
   - Closes gap G2 — the graph view IS the runtime.

4. **Spawn-spec serialization** under `pkg/cognition/dag/spawn.go`.
   - Terse one-line-per-node format for human inspection and compact
     trace rendering.
   - JSON canonical form for storage and replay.
   - Used by handlers when returning `Spawn`, by the executor when
     recording traces, and by `cortex journal replay`.

5. **Model node** — at least one registered op under the `model`
   function (e.g. `predict_next`).
   - Smallest viable: turn-ahead predictor that pre-warms Reflex with
     predicted-query embeddings during idle.
   - Just another registered micro-LLM node; nothing special about
     its place in the architecture.
   - Closes gap G1.

6. **Loop rewrite** in `internal/harness/loop.go`.
   - Current `Sense → LLM → Act → repeat` becomes `seed → walk →
     finalize` per `dag-protocol.md`.
   - The 5 existing tools become 5 registered `act` ops.
   - The heavy LLM agent loop becomes the `decide.coding_turn` op —
     the one big-LLM node in the per-turn tree, surrounded by
     micro-LLM nodes for salience, capture decisions, etc.

7. **End-to-end scenario** (per `coverage-matrix.md` stage 4).
   - One multi-session library-service-shaped scenario that exercises
     all 10 dimensions in sequence.
   - Per-step scoring (10 `CellResult` rows per run) + holistic pass row.
   - Reports per-tool-axis telemetry alongside per-dimension scores.
   - Reports per-turn tree evolution: did the executor grow varying
     trees across the session, or always the same shape?
   - The acceptance test for whether the integration paid off.

**Success criteria.** 10 of 10 dimensions covered; e2e scenario runs
green; per-function activation trace shows the tree shape varying
across turns based on budget and inputs (not always the same default
walk); budget-exhaustion rate measured and reported per DAG type in
`eval-journal.md`.

**Dependencies.** All prior phases.

**What's explicitly NOT in this phase.** No "planner node" that emits
DAGs upfront. No DAG-spec parser (no upfront DAGs to parse). No
malformed-emission fallback (no emission to malform). These were in
an earlier sketch and were dropped when the protocol moved to seed +
grow + decay — see `dag-protocol.md` for the design rationale.

---

### Phase 6 — Full eval suite review

**Goal.** After Phases 1-5 land, run the complete eval portfolio against
the integrated architecture and produce one comprehensive baseline-vs-
post-integration delta report. Per-phase success criteria catch local
regressions; this phase catches interaction effects and is where
`ROADMAP.md`'s ABR North Star gets updated under the new stack.

**Deliverables.**

1. **Restore runner** for `legacy/cognition/` per-node scenarios.
   - The 22 scenarios in `test/evals/legacy/cognition/` (`reflex_*`,
     `reflect_*`, `resolve_*`, `dream_*`, `abr_*`, `session_*`) map 1:1
     onto the Phase 5 node registry — they were authored before their
     runtime existed.
   - The DAG executor from Phase 5 is the natural execution target.
     Restore by writing a thin loader that translates each scenario
     into a per-node assertion against the registered op.
   - Audit `legacy/idiom/` against the v2 ports (`go-logging.yaml`,
     `go-naming.yaml`, `go-testing.yaml`); delete legacy copies if
     confirmed equivalent.
   - Leave `legacy/future/` as aspirational seeds; possibly rename to
     `test/evals/aspirational/` for clarity.

2. **Run the full suite end-to-end.**
   - v2 scenarios (40+ in `test/evals/v2/`)
   - Restored `legacy/cognition/` as per-node evals (22)
   - `journeys/` as e2e (10) — already actively cited as the canonical
     E2E suite in `docs/prompts/eval-data-gathering.md`
   - 7 upstream benchmarks from Phase 4 (τ-bench, MINT, AgentDojo, BFCL
     + intent-ingress, in-flight-obs, presentation-judge proxies)
   - 10-dim e2e scenario from Phase 5
   - Every scenario produces structured `cell_results.jsonl` rows per
     Phase 1's unified telemetry.

3. **Comparative delta report** appended to `eval-journal.md`.
   - **Pre-integration baseline** — current eval-journal numbers (the
     0.77 ABR baseline, the v2 win-rates, the LongMemEval scores).
   - **Post-Phase-3 snapshot** — cognitive graph wired into the loop;
     measures the Sense+Attend edge in isolation.
   - **Post-Phase-5 snapshot** — full DAG protocol live; measures the
     grown-tree shape vs. the Phase-3 fixed default.
   - **Per-axis breakdown** — using the 6 tool-axis columns from Phase 1
     telemetry, identify which axis carries most of the latency / cost.
   - **Per-cortex-function breakdown** — using the function tag in
     `cell_results.jsonl`, chart per-function cost and quality over the
     full suite.
   - **Per-dimension scores** — the 10-dim matrix populated end-to-end
     for the first time.
   - **Tree-shape analyses** (new with the DAG protocol):
     - Average tree depth per DAG type
     - Per-function fan-out distribution (do `decide` nodes typically
       spawn 1, 3, or 10 children?)
     - Budget-exhaustion frequency by axis (latency vs. tokens vs. depth)
     - Per-tree LLM-call distribution: how many micro-LLM nodes vs.
       one heavy `decide.coding_turn` per turn?

4. **Regression triage.**
   - Any scenario where post-integration score < pre-integration score
     gets a named issue with owner and root-cause hypothesis.
   - Triage criteria: noise, real regression, or expected trade-off
     (e.g. higher latency in exchange for better grounding).

5. **Update `ROADMAP.md`** with post-integration ABR + token-cost-
   reduction numbers, closing the loop with the existing metric-
   tracking doc.

**Success criteria.**
- Every scenario in `test/evals/` has run at least once under the
  integrated architecture.
- `eval-journal.md` contains a single comparative report with all three
  snapshots (pre / post-Phase-3 / post-Phase-5).
- ABR target from `ROADMAP.md` (≥ 0.9) measured under the new stack and
  reported, whether achieved or not.
- Regression list is non-empty (some regressions are healthy — silence
  here means the suite isn't sensitive enough).

**Dependencies.** All prior phases.

**Why a dedicated phase, not just per-phase gates.** Per-phase gates
catch local regressions; they miss interaction effects. Wiring
auto-context (Phase 3) plus DAG planning (Phase 5) may have emergent
behavior neither phase predicted alone. This is also the natural place
to honestly answer "did the integration pay off?" — one report, not
five scattered claims.

---

## Phase summary

| Phase | Closes | Depends on | Headline deliverable |
|---|---|---|---|
| 0 — Reconciliation | (docs only) | — | Cross-links + cortex-function column |
| 1 — Tool-surface foundation | G4 (axes 1, 4, 6) | 0 | `tools.json` + envelope + unified telemetry |
| 2 — CLI surfaces | (unblocks 4) | 1 | Event stream + multi-turn driver + MCP + `--confirm` |
| 3 — Cognitive graph in loop | G3 | 1, 2 | Reflex auto-inject + Value closure + Modulate exposure |
| 4 — Upstream wave | dims 1, 4, 6, 7, 8, 9, 10 | 2, 3 | τ-bench / MINT / AgentDojo / BFCL + 3 proxies |
| 5 — DAG protocol + e2e | G1, G2 | all | Node registry + budget model + seed-and-grow executor + Model node + 10-dim e2e (see [`dag-protocol.md`](dag-protocol.md)) |
| 6 — Full eval suite review | (interaction effects) | all + 5 | Restore legacy/cognition runner + run everything + comparative delta report in `eval-journal.md` + update ABR in `ROADMAP.md` |

---

## Cross-references

This roadmap stitches together work tracked in detail elsewhere. When this
doc says "Phase 4 lands τ-bench," the per-benchmark detail lives in
`coverage-matrix.md` and a yet-to-be-created `docs/benchmarks/tau-bench.md`.
When it says "Phase 1 lands the envelope wrapper," the per-axis detail
lives in `tool-surface.md`. This doc only updates when an integration
boundary shifts.

| Doc | What it tracks | When to update |
|---|---|---|
| [`ROADMAP.md`](../ROADMAP.md) | ABR-driven phases, eval numbers | When a numbered phase status changes or a metric moves |
| This doc | Cortex functions ↔ dims ↔ axes integration | When an integration edge changes or a framework is added |
| [`coverage-matrix.md`](benchmarks/coverage-matrix.md) | 10 dimensions, per-dim status | When a benchmark wraps or a CLI gap closes |
| [`tool-surface.md`](tool-surface.md) | 6 per-call axes, per-axis gaps | When an axis-level engineering change lands |
| [`dag-protocol.md`](dag-protocol.md) | Per-turn DAG runtime protocol (Phase 5 target) | When the protocol shape changes or a new node type lands |
| [`eval-prep-epic.md`](eval-prep-epic.md) | Eval infrastructure prep that gates the Phase 5 build | When a phase status changes or a new mechanic eval is added |
| [`dag-build-plan.md`](dag-build-plan.md) | Stage-by-stage implementation plan for Phase 5 (v0 first, CLI-first) | When a stage completes, an ADR lands, or scope changes |
| [`eval-principles.md`](prompts/eval-principles.md) | 9 principles every benchmark must satisfy | Rarely — principles are stable |

---

## Open questions (resolve as we go)

- **Should Model become a 6th cognitive mode?** Currently the modes are
  Reflex/Reflect/Resolve/Think/Dream. Adding Model as a peer (rather than
  folding it into Think) makes the graph more honest but expands the API
  surface. Decide at Phase 5 design time.
- **Where does the cortex-function tag in `cell_results.jsonl` come from?**
  Per-call telemetry needs to know which function the call belonged to (so
  analysis can chart per-function cost/latency). Probably a static mapping
  from tool name → function; revisit if reentrant calls make this ambiguous.
- **Default-on auto-context in Phase 3a?** Auto-injection by default
  vs. opt-in. Default-on is the small-model amplifier thesis; default-off
  is conservative for the harness's current users. Plan: default-on with
  `--no-auto-context` opt-out, revisit if it surprises users.
- **Shell-out vs. port simulated-user (Phase 4).** Inherited from
  `coverage-matrix.md` open questions; same answer: shell out first, port
  if the process boundary becomes painful.

---

*This is a living document. Update when integration boundaries shift.
Per-doc detail lives in the referenced framework docs; this one tracks
the seams between them.*
