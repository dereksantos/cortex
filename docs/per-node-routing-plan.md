# Per-Node Model Routing Plan

> **Status:** Plan. Not yet implemented. Successor branch to
> `derek.s/model-registry-size-aware-reads` (PR #53), which delivered
> the ModelRegistry abstraction this plan depends on.

## Problem

Cortex's DAG has per-node model routing wired end-to-end —
`NodeSpec.attrs.model`, `llm.ProviderFactory.Get(id)`, model-catalog
injection into `decide.next`'s prompt — but the policy that flips the
switch doesn't exist. Live traces (2026-05-23 against
`chatterbox/Qwen3-Coder-30B-A3B-Instruct-GGUF`) show every `decide.*`
node running on the session default:

```
decide.next        lat=17220ms model=(default)
decide.tool_call   lat= 8173ms model=(default)
decide.coding_turn lat=852261ms model=(default)
sense.classify_intent lat= 2700ms model=(default)
```

`decide.tool_call` doing structured JSON emission in 8 seconds on a 30B
is pure waste — an xLAM-1.5B or Phi-3-mini-with-tools would do the same
work in <1s. Multiply by every utility node every turn and that's where
multi-minute sessions come from.

Cause: the LLM in `decide.next` *can* emit `"model": "<specialist>"` on
spawns, but:

1. We give it the model catalog but no signal "X is the tool-calling specialist."
2. No examples in the prompt show per-node routing.
3. No fallback mechanism — if it forgets, the default sticks.

The fix isn't "improve the prompt." The fix is making routing a
spawn-time *policy*, not a per-prompt LLM elective.

## Design

Routing becomes deterministic at spawn time, sourced from the
`ModelRegistry` + a per-node-type role policy. LLM-emitted
`attrs.model` survives as an explicit override.

### Policy: node → role

| Node | Role | Why |
|---|---|---|
| `sense.classify_intent` | `RoleFast` (CapToolCalling preferred) | Tiny classification; doesn't need a coder |
| `decide.next` | `RoleFast` (CapToolCalling required) | Emits JSON spawn lists; small fast specialist nails this |
| `decide.tool_call` | `RoleFast` (CapToolCalling required) | Structured tool-call emission — the canonical specialist case |
| `decide.coding_turn` | `RoleCode` (CapCoding required) | The actual coder; lean on the configured model |
| `attend.compress` / `attend.distill` | `RoleFast` | Summarization; fast > coder-quality |
| `attend.chunk` | — | Deterministic, no LLM call needed |
| `remember.vector_search` | — | Mechanical |
| `act.*` | — | Mechanical |
| `value.*` / `decide.should_capture` / etc. | `RoleFast` | Cheap utility |

### Resolution order (at spawn time)

1. `NodeSpec.attrs.model` set explicitly → use it. Overrides everything.
2. Look up node's qualified name in policy table → get target `Role`.
3. Query `Registry.PickRole(role, requires=[caps])` → ModelInfo.
4. Build provider via `ProviderFactory.Get(modelInfo.ID)`.
5. Fallback to session default if registry has no match (legacy
   behavior preserved).

### Where it lives

A new package `pkg/cognition/dag/routing` (or `internal/llm/dispatch`)
exposes:

```go
type NodePolicy struct {
    // QualifiedName ("decide.tool_call") → role + required caps.
    Rules map[string]RoleSpec
    Registry llm.ModelRegistry
    Default llm.Provider  // session default fallback
}

type RoleSpec struct {
    Role     llm.Role
    Requires []string // capability tags
}

func (p *NodePolicy) ResolveProvider(qname string, override string) (llm.Provider, string) {
    // 1. Override wins
    // 2. Look up rule for qname
    // 3. registry.PickRole(rule)
    // 4. Build provider; return (provider, "policy:roleX/modelY")
    // 5. Fallback to Default
}
```

`pkg/llm/recommend.go` already has the role-pick primitive
(`recommendOne(role, catalogs)`); the policy package wraps it with the
node→role mapping and caches the per-role decision per session.

### Executor wiring

`Executor` gains a `RoutingPolicy *NodePolicy` field. Before each
node's handler runs, the executor:

1. Consults `policy.ResolveProvider(node.QualifiedName, node.Attrs["model"])`.
2. Injects the resolved provider into the node's input map under a
   well-known key (e.g. `in["__provider__"]`), OR — cleaner — passes it
   via the existing `dag.Budget` carrier.
3. Handlers that today read `cfg.Provider` switch to reading the
   resolved provider when present.

Open question: minimum-invasive wiring. Two options:

- **A — handler-side resolution:** keep handlers' current signature;
  add an optional `Provider` field to the `Budget` struct (or a new
  `Context`-carried value); handlers prefer it when set.
- **B — executor pre-resolution:** executor builds the provider before
  invoking the handler; handler sees it as a fixed input. Cleaner but
  every handler signature changes.

Recommendation: **A**. Handlers stay self-contained; the routing
policy is opt-in per-handler (call sites can be migrated one at a
time).

## Implementation slices

1. **`NodePolicy` skeleton + tests.** New package, table-driven
   resolution tests with a fake registry. No executor wiring yet.

2. **Executor passes resolved Provider via Budget (or context).**
   Handler signature stays. Just a new field carried through.

3. **Migrate `decide.tool_call` to use the resolved provider.**
   First real user. Existing tests cover behavior; add one new test
   confirming `attrs.model` override still wins.

4. **Migrate `decide.next`.** Same shape.

5. **Migrate `sense.classify_intent`.** Smallest slice; verifies the
   pattern for non-decide nodes.

6. **Migrate `attend.compress` / `attend.distill`.** Last in the
   batch because they're the most-tested.

7. **Default policy in REPL setup.** `cmd/cortex/commands/repl.go`
   wires the policy at session start using
   `buildREPLRegistry`'s registry and a built-in default rule set.

8. **Optional: config override.** `.cortex/config.json` gains
   `routing: {node_qname: {endpoint, model}}` for operators who want to
   pin specific routes.

## Eval shape

The thesis: per-node routing reduces wall-clock latency and dollar/token
cost *without* regressing answer quality. Evals must compare cells.

### Comparison cells

| Cell | Description |
|---|---|
| `baseline` | Session model runs every `decide.*` (current behavior) |
| `routed_local` | `decide.tool_call` → local specialist (Phi-3-mini-tools or Qwen3-4B); `decide.coding_turn` → session |
| `routed_full` | Every node policy-routed per the table above |
| `manual` | Operator-pinned routes from `.cortex/config.json` |

### Scenarios (reuse existing eval scenarios where possible)

- `simple_read` — "tell me about cmd/cortex/main.go"
- `multi_step_explore` — "how does the cognitive architecture work?"
- `targeted_edit` — "rename `effectiveContextWindow` to `runtimeContext`"
- `swe_bench_lite` — one small fix from SWE-bench-Lite to anchor against industry benchmark

### Metrics

| Metric | Direction | Notes |
|---|---|---|
| `decide.tool_call.latency_ms` (p50/p95) | lower better | Direct signal; specialist should be <1s |
| `session.total_wall_ms` (p50/p95) | lower better | End-to-end UX |
| `session.tool_call_accuracy` | ≥ baseline | Did spawned acts match expected tool+args? |
| `session.final_answer_quality` | ≥ baseline | LLM-as-judge or human; the no-regression gate |
| `session.cost_usd` | lower better | Dollar cost; specialist-local is ~0 |
| `session.tokens_in/out` | lower better | Token cost |
| `session.no_progress_rate` | lower better | How often does the loop bail vs complete? |

### Success criteria for shipping

- `routed_local` reduces `session.total_wall_ms` p50 by ≥40% on
  `simple_read` and `multi_step_explore`
- No regression in `session.final_answer_quality` across all cells
- `session.no_progress_rate` doesn't increase
- `decide.tool_call.latency_ms` p50 drops to <1.5s when routed

### Failure modes to watch for

- Specialist emits malformed JSON → `decide.tool_call` falls back to
  fan-out → spawns garbage → wasted turns. (Today's xLAM-style models
  are usually fine; budget at least one calibration run per specialist
  before declaring shippable.)
- Specialist's tool catalog injection bloats prompt → no actual win.
  (The catalog is shared across all nodes; this is a fixed cost.)
- Multi-endpoint latency stacking — every node hits a different
  endpoint → cumulative network RTT exceeds the in-model latency
  savings. (Local specialists mitigate; pure cloud routes might
  regress.)

## Open questions

1. **Which specialist is the right default for `decide.tool_call`?**
   Phi-3-mini-with-tools (Ollama, ~2GB) vs xLAM-1.5B (HF, needs
   conversion) vs Qwen3-4B-2507 (already on chatterbox). Eval the
   three on `simple_read`; pick the fastest-per-correct.
2. **Does `decide.next` benefit from routing the same way, or does
   it need the bigger model's planning quality?** Eval `routed_full`
   vs `routed_local` on `multi_step_explore` to find out.
3. **Should the policy be per-session or per-call?** Per-session
   keeps cache hot (one specialist warmed); per-call lets the LLM
   override on edge cases. Default per-session, allow override.
4. **Cold-start: what if no specialist is available?** Fall back to
   session default with a one-line warning. Don't fail the session.

## Related

- `pkg/llm/recommend.go` — role-pick primitive (already exists)
- `pkg/llm/registry.go` — model discovery (delivered in PR #53)
- `docs/eval-strategy.md` — three thesis claims; multi-model leverage
  is claim #1, this work is direct evidence for it
- Memory: [[project-direction-small-model-amplifier]] — Cortex as
  salience layer for small models; this plan operationalizes
  "right model on the right node"
