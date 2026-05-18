# DAG Protocol

> **Purpose.** The minimal runtime protocol that unifies Cortex's cognitive
> architecture, eval contract, and tool surface into one shape. Every
> conversational turn grows a DAG of typed nodes that read and write Cortex
> state under a decaying budget; nodes spawn children as they discover
> what's needed; outcomes update both LLM context and Cortex state.
>
> **Status.** Design. The integration roadmap
> ([`integration-roadmap.md`](integration-roadmap.md)) lands this in Phase
> 5; earlier phases are prerequisites.
>
> **Owner.** `pkg/cognition/` + `internal/harness/loop.go`.

---

## Why this shape

Four observations collapse into one protocol:

1. **The cortex functions** (Sense / Attend / Represent / Remember / Model
   / Value / Decide / Act / Maintain / Modulate) want to compose as a
   *graph*, not a pipeline. The current Fast/Full retrieval paths are
   the same DAG type at different budgets, not different code paths.

2. **The 10 UX dimensions** and **6 tool axes** judge emergent session
   behavior and per-call invariants respectively. Neither is a runtime
   structure — both are *measurements over* a runtime structure.

3. **Most nodes are narrow micro-LLM calls.** Small models excel at tiny
   scoped decisions (rank these 10, score Y/N + reason, conflict or not).
   Replacing one giant orchestrator-LLM call with N tiny micro-LLM calls
   is the small-model amplifier thesis made concrete.

4. **Planning is the composition** — not a separate planning call. There
   is no upfront DAG to emit. A turn starts with a tiny seed, nodes
   spawn children as they execute, budget decays uniformly, and the
   "plan" is whatever tree emerges.

The minimal protocol that satisfies all four: typed nodes that grow a
tree from a seed under a decaying budget.

---

## The minimal protocol

Three primitives. Nothing else at runtime.

```
Node = id + function + op + attrs
Edge = parent → child (carries data) (+ optional why)
DAG  = the tree that grows from a seed under a budget
```

- **Node** — a single call to a Cortex-provided primitive. `function`
  names the cortex function (the broad category); `op` names the
  specific primitive within that function; `attrs` carry call args.
- **Edge** — a dataflow / spawn relationship. Derived from spawn specs
  and `$node.out` references; not separately emitted.
- **DAG** — the tree that grows turn-by-turn. Acyclic by construction
  (children can't spawn ancestors); bounded by budget and depth cap.

There is no upfront DAG to emit, no separate planner to fail, no path
constants to maintain.

---

## Seed + grow + decay

Each DAG type has a tiny seed (1-2 nodes). The executor walks the seed;
each node's handler may return *spawn specs* for children. Spawned
children get scheduled into the pending set. As each node runs it
returns its actual cost, which decays the turn's budget. When budget
hits zero (or the hard depth cap is reached), no new spawns; in-flight
nodes finish with what they have.

```
seed     = {sense.prompt}                                  # turn-DAG seed
budget   = {latency_ms: 2000, tokens: 4000, depth: 10}     # initial

executor.run(seed, budget):
  pending = list(seed)
  while pending and budget > 0:
    n = next(pending)
    out, spawn_specs, cost = n.handler(ctx, in(n), remaining(budget))
    budget -= cost
    if budget > 0 and depth(n) + 1 <= budget.depth:
      pending += spawn_specs
  finish in-flight; abandon unscheduled
```

The "DAG" you see post-hoc is the call tree the executor produced. That
tree is reconstructable from `cell_results.jsonl` rows that carry
`parent_node_id`, and replayable from the journal.

---

## Cortex functions (the node-type set)

The 10 cortex functions defined in [`integration-roadmap.md`](integration-roadmap.md)
are the *node types*. Every node has exactly one function.

| Function | Role | Example ops |
|---|---|---|
| `sense` | Receives unsolicited signal (user prompt, hook injections, file-change events) | `prompt`, `hook_event`, `notification` |
| `attend` | Scores salience over substrate; gates what gets surfaced | `reflex`, `rerank`, `filter` |
| `represent` | Encodes raw input as patterns (embeddings, AST, symbols) | `embed`, `parse_ast`, `extract_symbols` |
| `remember` | Persists and retrieves representations | `journal_lookup`, `vector_search`, `recent` |
| `model` | Generates predictions and counterfactuals | `predict_next`, `simulate`, `theory_of_user` |
| `value` | Assigns relevance / reward / threat scalars | `score`, `detect_contradiction`, `rank_safety` |
| `decide` | Selects next action; runs the heavy LLM coding agent | `inject`, `defer`, `route`, `coding_turn` |
| `act` | Executes work in the world (tools, edits, shell) | `read_file`, `write_file`, `run_shell`, `cortex_search` |
| `maintain` | Offline consolidation, capture, plasticity | `capture`, `consolidate`, `prune` |
| `modulate` | Cross-cutting: budget, arousal, oscillation | `budget_check`, `gate`, `dampen` |

`modulate` is the executor's budget-enforcement layer applied to every
spawn (see [Budget model](#budget-model)). Explicit `modulate` ops
exist for nodes that want deliberate checkpoints; the implicit gate is
always active.

---

## Node distribution: most are micro-LLM calls

Per typical coding turn, the workload breaks down:

| Node | LLM? | Scope |
|---|---|---|
| `attend.rerank` | yes (small) | "rank these 10, 1-10 + reason" |
| `value.score` | yes (small) | "does this matter? Y/N + 1-line why" |
| `value.detect_contradiction` | yes (small) | "conflicts with prior? Y/N" |
| `decide.inject` | yes (small) | "inject now / defer / queue" |
| `decide.should_capture` | yes (small) | "worth keeping? Y/N + tag" |
| `model.predict_next` | yes (small) | "likely next queries (top 3)" |
| `maintain.extract_insight` | yes (small) | "1-2 insights or none" |
| **`decide.coding_turn`** | **yes (BIG)** | **full tool-calling agent loop** |
| `act.read_file`, `act.run_shell` | no | mechanical |
| `remember.vector_search` | no | mechanical |
| `represent.embed` | embedding model | mechanical-ish |

~70-80% of nodes are LLM-backed; all but one (`decide.coding_turn`) are
narrow micro-decisions a small model handles well. That ONE big LLM
node hosts the heavy agent loop — surrounded by micro-LLM nodes that
interpret intent, score salience, and decide capture before/after.

**This is the small-model amplifier thesis made concrete.** The
architecture works because each node only has to be good at one narrow
thing, and small models are good at narrow things. Big-model spend is
reserved for the one node that genuinely needs it (the coding agent).

---

## Node schema

A node in canonical form:

```json
{
  "id":       "n2",
  "function": "attend",
  "op":       "rerank",
  "in":       { "candidates": "$n1.candidates" },
  "out":      ["top"],
  "attrs":    { "k": 3 },
  "why":      "resolve_contradictions",
  "parent":   "n1"
}
```

- `id` — unique within the turn's tree.
- `function` — one of the 10 cortex functions above.
- `op` — primitive name registered in the node registry.
- `in` — input map. Values are either literals or `$<node_id>.<out_name>`
  references to other nodes' outputs.
- `out` — names of outputs this node produces.
- `attrs` — call-time options for the op.
- `why` — optional terse annotation explaining the spawning node's intent.
- `parent` — the spawning node's id (executor fills this in; not provided
  by handlers).

### Handler signature

```go
type NodeResult struct {
    Out          map[string]any   // outputs downstream consumers see
    Spawn        []NodeSpec       // children to schedule (optional)
    CostConsumed Cost             // actual latency_ms + tokens used
}

type Handler func(ctx, in map[string]any, budget Budget) (NodeResult, error)
```

A handler:
- **Reads** its materialized inputs (`in`) and the remaining budget.
- **Self-modulates** based on budget — e.g., with 1500ms left, do a
  careful LLM rerank; with 50ms left, fall back to a mechanical
  heuristic.
- **Returns** outputs, optional children to spawn, and the actual cost
  consumed (executor uses this to decay the turn budget).
- **Refuses** with `error: budget_insufficient` if the cheapest fallback
  also can't fit in the remaining budget.

## Edge schema

Edges are *derived* from spawn relationships and `$node.out` references.
Two edge types:

- **Spawn edge** — parent spawned child. One per spawned node.
- **Data edge** — child's `in` references parent's `out`. Multiple
  possible (a node can consume outputs from multiple ancestors).

Edges are not emitted separately. The executor builds them during
walk and records them in the trace.

## DAG schema (the recorded artifact)

The recorded trace, not an upfront emission:

```json
{
  "version":  "1",
  "turn_id":  "...",
  "dag_type": "turn",
  "seed":     [...],
  "budget":   {"latency_ms": 2000, "tokens": 4000, "depth": 10},
  "trace":    [ ...one entry per executed node, each with parent_id... ],
  "outcome":  { "ok": true, "exhausted_axis": null }
}
```

There is no `emitted_by` field — nothing emits the DAG. The trace IS
the artifact, captured from `cell_results.jsonl` rows + journal.

---

## Spawning

Nodes spawn children via the `Spawn` field of `NodeResult`. Four
patterns cover the common cases:

| Pattern | Use case | Example |
|---|---|---|
| **No spawn** | Leaf op | `act.read_file` returns content; no children |
| **Fan-out** | Parallel sub-work | `attend.reflex` spawns 3 `attend.rerank` children for 3 candidate batches |
| **Sequential follow-up** | Chain | `decide.inject` spawns `maintain.capture` to record the injection |
| **Conditional spawn** | Branch on output | `value.score` spawns `decide.escalate` only if score < threshold |

The executor schedules spawned children into the pending set and walks
in topological order. Independent siblings may execute in parallel
(executor decides based on remaining budget and CPU availability).

### Compact representation

Spawn specs and post-hoc traces serialize to a terse one-line-per-node
form for humans and for compact wire/log representations:

```
<function>  <op>     <inputs> → <outputs>     [attr=value ...]  [why=...]
```

Example — the salience-injected default turn, rendered as a trace:

```
sense    prompt                                  → prompt
attend   reflex       prompt                     → candidates  k=10
attend   rerank       candidates                 → top         k=3   why=resolve_contradictions
decide   inject       top                        → context_msg
maintain capture      context_msg                → journal
```

Same shape, whether read top-down as a post-hoc trace or constructed by
nested handlers spawning children. The canonical form is JSON; the
terse form is for humans.

---

## Budget model

Three axes of budget, all decaying:

| Axis | Decays when | Stops at |
|---|---|---|
| `latency_ms` | every node call consumes wall time | 0 — turn must complete |
| `tokens` | every LLM-backed node consumes tokens | 0 — cost ceiling |
| `depth` | every spawn increments depth | hard cap (default 10) |

### Per-DAG-type seeds and initial budgets

| DAG type | Seed | Initial budget (defaults) |
|---|---|---|
| `turn` | `{sense.prompt}` | `2000ms / 4000 tokens / depth 10` |
| `think` | `{think.session_check}` | proportional to spare cycles (inverse activity); small tokens |
| `dream` | `{maintain.idle_probe}` | proportional to idle time; large tokens; depth 15 |
| `capture` | `{sense.hook_event}` | tiny — `100ms / 500 tokens / depth 3` |
| `eval` | `{sense.cli_invocation}` | explicit `--max-ms`, `--max-tokens` |

Per-DAG-type defaults are configurable via `pkg/config`. The executor
seeds and walks; nothing else changes per type.

### Decay mechanics

Decay is implicit: every node returns `CostConsumed`; the executor
subtracts. Nodes receive `remaining_budget` as input and can self-throttle:

```go
func (h ReflectHandler) Call(ctx, in, budget) (NodeResult, error) {
    if budget.LatencyMS < 200 {
        return mechanicalFallback(in)            // skip the LLM call
    }
    return llmRerank(in, budget.TokenLimit())
}
```

### Bounding

Two safety nets prevent runaway:

1. **Hard depth cap** — `budget.depth` decrements on each spawn; spawn
   refused at 0. Prevents infinite recursion regardless of LLM behavior.
2. **Budget exhaustion** — soft limit on `latency_ms` and `tokens`.
   In-flight nodes finish; new spawns refused.

Both bounds emit telemetry: an exhaustion event in `cell_results.jsonl`
naming which axis was hit. Phase 6 evals chart exhaustion frequency
per DAG type.

---

## Modulate as the budget enforcer

`modulate` is not a node type in DAG topology. It is the executor's
budget-enforcement layer, applied to every spawn:

```
Before spawning child:
    if modulate.check(child.cost_hint, remaining_budget) == OVER_BUDGET:
        emit cell_result(child, error="budget_exceeded", spawned=false)
    else:
        schedule(child)
```

Budget state is visible in the event stream (per Phase 3c of the
integration roadmap). The explicit `modulate.budget_check` op exists
for nodes that want to insert deliberate checkpoints (e.g., before
spawning a fan-out of 10 children), but the implicit per-spawn gate is
always active.

---

## Tool-axis contract for `act` nodes

Every `act`-typed node satisfies all six axes from `tool-surface.md`:

| Axis | How it appears on an `act` node |
|---|---|
| 1 Contract | Op declared in the registry with name, schema, version |
| 2 Authorization | Registry marks `read` vs. `mutator`; per-project allowlist gate runs before dispatch |
| 3 Dispatch + Execution | Executor invokes handler with timeout from `Cost.MaxMs` |
| 4 Result | Outputs land in `{ok, data, error, meta:{trace_id, latency_ms}}` envelope per Phase 1 |
| 5 State / side-effects | Mutators write a journal entry; destructive ops require a `confirm=true` attr |
| 6 Observability + Budget | Per-node `cell_results.jsonl` row; budget enforced at the spawn edge by `modulate` |

A non-`act` node does not carry these guarantees — they're not relevant.
Only `act` reaches into the world; the other functions move data within
Cortex's own memory.

---

## Node registry

Each registered op declares:

```go
type NodeSpec struct {
    Function    CortexFunction       // sense, attend, ...
    Op          string               // "reflex", "read_file", ...
    Description string               // surfaced to other nodes that may spawn this op
    Inputs      []ParamSpec          // name + type + required
    Outputs     []ParamSpec
    AxisContract *AxisContract       // required for act-typed nodes
    Cost        CostHint             // expected latency_ms / tokens (used by Modulate)
    MaxFanout   int                  // max children of this op per call (default 10)
    Handler     Handler              // see signature above
}
```

The registry is the single source of truth for what nodes exist.
Adding a Cortex capability is registering a node. No separate tool
registration, mode wiring, or schema authoring.

---

## Co-evolution: how both sides update per turn

Every turn evolves two state spaces in lockstep:

| State space | What updates each turn |
|---|---|
| **LLM context** | Injected context from `decide.inject` nodes; per-node summaries from the event stream; the `decide.coding_turn` node's own transcript |
| **Cortex state** | Journal entries from `maintain.capture` and the writer-classes; `cell_results.jsonl` rows for every executed node (with `parent_node_id` for tree reconstruction); ProactiveQueue updates from `model` nodes' predictions |

Next turn:
- The executor sees evolved Cortex state when computing the seed and
  initial budget for the new turn.
- The LLM (inside `decide.coding_turn`) sees evolved context via the
  injected messages from prior turns' trees.

The DAG protocol is the bidirectional spine. Neither side updates the
other behind the protocol's back.

---

## Examples

### Example 1 — salience-injected turn (default seed)

Seed: `{sense.prompt}`. Tree that grows:

```
sense.prompt                              → prompt
└─ attend.reflex(prompt)                  → candidates  k=10
   └─ attend.rerank(candidates)           → top         k=3   why=resolve_contradictions
      └─ decide.inject(top)               → context_msg
         └─ maintain.capture(context_msg) → journal
```

5 nodes, depth 5, ~50ms total budget. The Phase 3a auto-inject made
literal — no upfront emission, each node spawns the next.

### Example 2 — code question, deeper tree

User mentions a code symbol. `attend.reflex` spawns AST parsing and
broader retrieval because its inputs warrant it:

```
sense.prompt                              → prompt
├─ attend.reflex(prompt)                  → candidates  k=20
│  └─ attend.rerank(candidates, related)  → top  k=5   why=cross_grounding
│     └─ decide.inject(top, likely)       → context
│        └─ decide.coding_turn(context, prompt) → output      [BIG LLM]
│           └─ decide.should_capture(output) → keep?
│              └─ maintain.capture(...)    → journal
├─ represent.parse_ast(current_buffer)    → ast
└─ remember.vector_search(prompt, ast)    → related → (joins rerank above)
```

Branches under `sense.prompt` ran in parallel (executor identified no
edge between `represent.parse_ast` and `attend.reflex`); `attend.rerank`
waited on both before firing. Each node spawned what it found necessary
given its inputs and remaining budget — no upfront design.

### Example 3 — budget-exhausted turn

System is busy (low Think budget). Same seed, tighter initial budget:

```
sense.prompt                              → prompt
└─ attend.reflex(prompt)                  → candidates  k=3
   └─ decide.inject(candidates)           → context     [skipped rerank: budget < 200ms]
      └─ decide.coding_turn(context, prompt) → output
         (maintain.capture: budget_exceeded; deferred to next turn's seed)
```

`attend.rerank` was skipped because the handler self-modulated to
mechanical fallback. `maintain.capture` got `budget_exceeded` and the
executor queued the capture for the next turn's seed.

### Example 4 — destructive op gated

```
sense.prompt                              → prompt
└─ attend.reflex                          → candidates
   └─ decide.plan_action(prompt, candidates) → action
      └─ act.run_shell(cmd="git reset --hard", confirm=false)
         → error: confirmation_required   [axis-5 gate trip]
```

Axis-5 gate trips before dispatch. Downstream `maintain.capture` not
spawned. The LLM's next turn sees the failure in its context (via the
trace).

---

## Code deltas required

In dependency order:

1. **`pkg/cognition/registry.go`** — node registry. Each existing mode
   (Reflex, Reflect, Resolve, Think, Dream) becomes one or more
   registered ops with handlers conforming to the new signature.
2. **`pkg/cognition/dag/spawn.go`** — terse-syntax serializer for spawn
   specs and traces; JSON canonical form.
3. **`pkg/cognition/dag/budget.go`** — Budget type, decay mechanics,
   per-DAG-type defaults loaded from `pkg/config`.
4. **`pkg/cognition/dag/executor.go`** — seed-and-grow walker. Manages
   pending set, spawn scheduling, parallelism, depth cap, budget
   enforcement. Writes `cell_results.jsonl` rows with `parent_node_id`.
5. **`internal/harness/loop.go`** — rewrite as `seed → walk →
   finalize`. The 5 existing tools become 5 registered `act` ops; the
   heavy LLM loop becomes the `decide.coding_turn` op.
6. **`internal/eval/`** — per-node and per-tree assertions for Phase 6's
   tree-shape analyses.

No separate planner module. No upfront DAG to parse. No
malformed-emission fallback. The seed + budget + registry are
sufficient.

---

## Open questions

- **Conditional spawn vs. up-front fan-out.** Handlers can spawn
  conditionally based on intermediate results, or spawn all candidates
  and let Modulate cancel under-budget ones. Trade-off depends on
  whether canceled spawns dominate cost. Start with conditional;
  revisit if executor parallelism wins are large.
- **Cost-hint accuracy.** Handlers report `CostConsumed` after running;
  the executor uses this for budget decay. Should it also self-calibrate
  the registered op's `cost_hint` so future budget checks are smarter?
  Probably yes, eventually — out of scope for v1.
- **Cross-turn budget rollover.** A `maintain.capture` deferred for
  budget should still happen. Spec: the next turn's seed includes any
  deferred spawns from the prior turn, prepended.
- **Spawn limits per node.** `MaxFanout` (per op, in the registry) caps
  children-per-call so a single node can't fan out 1000 children at
  depth 1. Default 10; override per op.
- **Replay vs. live trees.** `cortex journal replay` (per
  `journal-implementation-plan.md`) gives counterfactual evals; the
  tree shape should be reconstructable from `cell_results.jsonl` +
  journal, and replayable with budget overrides. Spec that in the
  replay tool, not here.

---

## Cross-references

| Doc | Relationship |
|---|---|
| [`integration-roadmap.md`](integration-roadmap.md) | Lands this protocol in Phase 5; earlier phases are prerequisites |
| [`tool-surface.md`](tool-surface.md) | Defines the 6 axes that every `act`-typed node carries |
| [`benchmarks/coverage-matrix.md`](benchmarks/coverage-matrix.md) | Defines the 10 UX dimensions; per-dim evals score against the post-hoc trace |
| [`journal.md`](journal.md) | Defines the 8 writer-classes; `maintain.capture` nodes write to them |
| [`emergence-evals.md`](emergence-evals.md) | The auto-tuning gate that closes the loop on registry quality over time |

---

*This is a living design doc. Update when the protocol shape changes or
new node types land. Per-op detail belongs in the registry / handler
code, not here.*
