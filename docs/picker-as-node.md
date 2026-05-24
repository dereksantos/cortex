# Picker-as-Node

> **Status:** Plan. Future evolution of `docs/per-node-routing-plan.md`.
> Depends on capability-declared nodes (Pillar #1). Cleanly separable —
> this doc captures the *evolution shape* so v1 implementation makes
> handler-swap room.

## Problem

`docs/per-node-routing-plan.md` v1 lands capability-declared nodes:
each `NodeSpec` declares `Requires []string`, and the executor looks up
"best available model for this capability" at spawn time.

The lookup itself is mechanical: walk the requirement chain, take the
first match, apply local-then-size tiebreakers. That's good enough for
cold-start, and good enough to make the harness usable.

But it's blind to empirical performance:

- "Which tool-calling specialist actually works best on *our* node shapes?"
- "Does the 30B coder beat the 1.5B specialist when the catalog has >12 tools?"
- "Has model X's reliability drifted since last week?"

The heuristic picker can't answer these. Every spawn already produces
the data needed to learn them — we just don't capture or learn from it.

## Design: the picker is a node

`decide.pick_model` (or `model.pick`) is a real DAG node:

```go
type PickModelIn struct {
    Candidates  []llm.ModelInfo   // pool the executor narrowed by requires
    Requires    []string          // the requirement chain
    NodeQname   string            // who's spawning (for outcome attribution)
    Recent      []PickOutcome     // recent outcomes for these candidates
}

type PickModelOut struct {
    ModelID     string
    Reason      string            // "heuristic" | "bandit:thompson@0.94" | "classifier:0.87"
    Confidence  float64
}
```

Same node, **swappable handlers**:

| Phase | Handler | What it does |
|---|---|---|
| 1 | `heuristic` | Walks `Requires` chain, returns first match (== v1 from `per-node-routing-plan.md`) |
| 2 | `bandit` | Thompson sampling over historical outcomes per `(node_qname, model_id)` |
| 3 | `classifier` | Embedded ONNX/tree, scores each candidate from features |

Every variant emits the same trace shape. The executor doesn't know
which is running — it just calls the node. Eval cells become
`picker=heuristic` vs `picker=bandit` vs `picker=classifier`.

## Phased rollout

### Phase 1 — Heuristic (ships with per-node-routing v1)

Handler is a pure function over `(Candidates, Requires)`. No state, no
side effects. Lives in `pkg/cognition/dag/ops/decide_pick_model.go`.

The executor can call this inline (no DAG node materialized) as an
optimization — but the *contract* is the node shape, so swapping in a
later handler doesn't touch handler call sites.

### Phase 2 — Passive outcome logging

Every spawn writes a `pick.outcome` record:

```jsonl
{"ts":"...","node_qname":"decide.tool_call","picked":"xlam-1.5b","reason":"heuristic","outcome":{"success":true,"latency_ms":420,"quality":0.91}}
```

Lands in `.cortex/journal/pick/` (new writer-class — see
`docs/journal.md` for adding a class) or rides on existing
`eval.cell_result` records. Free, low signal density (one decision per
spawn), but accumulates.

Outcome signal sources, weakest → strongest:
- Did the node return without error?
- Did downstream nodes treat this node's output as usable, or did they retry / fallback?
- LLM-as-judge on the node's output vs intent (expensive; sample-only).
- Explicit feedback events (`feedback.correction`, `feedback.retraction`).

### Phase 3a — Bandit picker

Thompson sampling per `(node_qname, model_id)`:
- Maintain `Beta(α, β)` posterior over success rate per pair.
- At spawn: sample success rate from each candidate's posterior, pick max.
- After outcome: increment α (success) or β (failure).

Properties:
- Principled exploration (no ε to tune).
- Naturally generates training data — the sampling noise IS the exploration.
- Cold-start: prior is `Beta(1,1)` (uniform) — falls back to heuristic order via tiebreak.
- Per-session state OR persisted to `.cortex/picker_state.json` (decision deferred to slice).

This is the most interesting milestone — it's online learning with no
offline training step. The bandit's behavior under noise IS the
"decisions emerging from micro-decisions" pattern in `MEMORY.md`'s
[[project_dag_nodes_are_micro_decisions]].

### Phase 3b — Counterfactual replay

`cortex journal replay` is already a skeleton (CLAUDE.md: "full
overrides land in a follow-up"). The follow-up: replay a session with
the picker's choice overridden to each alternative candidate, score
the resulting outcome. One real session → N×M training examples
(N spawns × M candidates per spawn).

Generates dense training data from sparse real usage. This is the
killer feature for phase 4 — without it, the classifier has too
little signal to learn cross-feature interactions.

### Phase 4 — Learned classifier

Train offline (Python notebook against the journal). Features per
`(candidate_model, spawn_context)`:

- Required caps (one-hot)
- Candidate caps (one-hot, incl. `:specialist` flags)
- Candidate size (billion params), locality (local/cloud)
- Node qname (embedding or one-hot)
- Recent success rate at this node qname
- Catalog size / context-window pressure (where relevant)

Label: outcome success (0/1) or quality (0–1).

Model: gradient-boosted tree (~MB, pure Go via leaves/onnx-go) OR
small MLP via the existing `pkg/llm/hugot.go` ONNX runtime. Score each
candidate, argmax.

`//go:embed` the trained artifact. Fall back to heuristic when
classifier confidence is below threshold (so low-signal decisions
don't look high-confidence).

## Why "node, not function"

- **Same trace shape across variants** — every picker decision shows up in the journal identically, so eval cells can compare them apples-to-apples.
- **Counterfactual replay works.** The picker decision is a node spawn; replay can override it like any other spawn.
- **Composable.** A future `decide.pick_model_with_fallback_chain` can spawn `decide.pick_model` twice and pick between results. Same pattern as everywhere else in the DAG.
- **Swappable per session / config.** `.cortex/config.json` can say `picker: "bandit"` and the executor wires that handler. No code change.

## Eval shape

Cells: `picker_heuristic`, `picker_bandit`, `picker_classifier`.

Scenarios: same as `per-node-routing-plan.md` (`simple_read`,
`multi_step_explore`, `targeted_edit`, `swe_bench_lite`).

Headline metric: **node-level success rate per `(node_qname, picker_variant)`**, with the slope over session-count being the "learning over time" evidence (thesis claim #2).

Secondary metrics:
- Picker decision latency (heuristic <1ms, bandit <5ms, classifier <10ms — all sub-perceptible)
- Confidence-vs-outcome calibration (does the bandit's posterior match observed success rate?)
- Cold-start time-to-stable-policy (how many spawns until the bandit converges on a clear winner?)

## Open questions

1. **Bandit state persistence.** Per-session (decays — good for adapting to new workloads, bad for re-learning every session) or persisted to `.cortex/picker_state.json` (durable — good for stable patterns, bad for staleness)? Probably persisted with TTL.
2. **Outcome attribution latency.** Some outcomes are known immediately (node errored); some need downstream evidence (did the spawned act actually accomplish the intent?). How long does the bandit wait before crediting/debiting?
3. **Confidence threshold for classifier fallback.** Below what posterior probability do we revert to heuristic? Likely tuned empirically once we have data.
4. **Multi-candidate ties.** When the picker sees two candidates with indistinguishable scores, does it pick randomly (extra exploration) or deterministically (stable trace)? Default deterministic; bandit's sampling handles exploration.

## Related

- `docs/per-node-routing-plan.md` — Pillar #1, the substrate this evolves
- `docs/intent-execution-split.md` — Pillar #2; the picker eventually routes translator nodes too
- `docs/journal.md` — adding the `pick` writer-class
- `docs/eval-strategy.md` — thesis claim #2 (learning over time), this is direct evidence
- Memory: [[project_dag_nodes_are_micro_decisions]] — the picker-as-node embodies this; the bandit's noise IS the substrate the user described
