# Intent + Delegated Execution

> **Status:** Plan. Architectural pillar. Depends on capability-declared
> nodes (`docs/per-node-routing-plan.md`) for routing tool-using nodes
> to specialists. Larger surface than the routing plan — touches every
> `act.*` node and the reasoning-side handlers.

## Problem

Today's `act.*` nodes mash two distinct concerns into one call:

1. **Intent** — what the spawning reasoner *wants* to happen, and why
2. **Execution** — the structured call that performs it

A `decide.coding_turn` running on Qwen3-Coder-30B emits an `act.read_file(path="foo.go")` spec directly. The reasoning model is responsible for both deciding to read the file AND emitting valid tool-call JSON. It's frequently bad at the second — that's the reliability bug at the heart of `docs/per-node-routing-plan.md`.

The deeper issue is that this couples three things that should be independent:

- **The reasoning model's capability** (semantic understanding, planning)
- **The tool-call surface** (which tools exist, their argument shapes)
- **The security perimeter** (sandboxing, confirmation, audit)

When tools change, reasoning prompts have to change. When reasoning patterns evolve, tool wrappers have to evolve. When a destructive op needs a new confirmation policy, both have to update. No clean boundary.

## Design

**Reasoning nodes emit intent. Tool-using specialist nodes translate intent into structured calls. `act.*` nodes are pure execution.**

```
decide.coding_turn (CapCoding, reasoning)
        │
        ▼ emits Intent {"type": "read", "target": "foo.go",
        │              "purpose": "find where bar() is called"}
        │
represent.intent_to_tool_call (CapToolCallingSpecialist)
        │
        ▼ emits act.read_file{path: "foo.go"}
        │
act.read_file (pure execution; no LLM)
        │
        ▼ returns raw bytes
        │
represent.result_to_observation (optional; CapToolCallingSpecialist or small chat)
        │
        ▼ returns observation digest tailored to original purpose
        │
back to decide.coding_turn for next step
```

Each layer is replaceable in isolation:

- New tool? Update the translator's catalog. Reasoning prompts unchanged.
- New reasoning pattern? Emit a new intent shape. Translator absorbs it (or fails loudly, which is honest).
- New security policy? Translator enforces. `act.*` unchanged.

### Intent vocabulary

Intent is a typed record, not free-form NL. Free-form is too ambiguous for a small specialist to translate reliably; full schema is brittle. Middle ground:

```go
type Intent struct {
    Type    string         // "read" | "write" | "search" | "run" | "edit" | ...
    Target  string         // path, query, command — type-dependent
    Purpose string         // free-text: what the reasoner is trying to learn / do
    Hints   map[string]any // optional: line ranges, file globs, env vars, etc.
}
```

The reasoning node emits this. The translator turns it into a concrete tool call. The `Purpose` field is the bridge to `represent.result_to_observation` — it knows what to preserve from the raw result.

Defining the `Type` vocabulary is most of the early design work. Start with the existing `act.*` surface as the seed (one Type per existing act). Expand as patterns emerge.

### Where it lives

`pkg/cognition/dag/registry.go` already has `FuncRepresent` — "re-representing intent in a different form" fits exactly. New ops:

- `represent.intent_to_tool_call` — Intent → NodeSpec (the structured call to spawn next)
- `represent.result_to_observation` — raw result + original Intent.Purpose → digest for reasoning consumer

`act.*` nodes get audited: which currently mix intent and execution? They get split — the act keeps execution, the intent side migrates to the reasoning node that spawns through the translator.

### Security perimeter

The translator is the choke point. All policy enforcement lives here, never inside `act.*` handlers:

- `AxisContract.Mutator` — translator refuses to emit a mutator call from a read-shaped intent
- `AxisContract.RequiresConfirmation` — translator pauses and prompts (or fails closed) before emitting destructive ops
- Sandbox path restrictions — translator validates `Target` against allowed roots
- Cost gates — translator checks budget before emitting expensive ops (long shell commands, large reads)
- Audit logging — every translation is a journal entry; the translation IS the audit record

This is a clean security argument. Today's `act.*` nodes can be invoked from anywhere with a malformed spec and it'll just run. With the split, malformed/unauthorized intents are caught at translation, before any system call.

### Routing implication

Translator nodes need `Requires: []string{CapToolCallingSpecialist, CapToolCalling}` — they're the canonical specialist use case. Reasoning nodes (`decide.coding_turn`, etc.) need `CapCoding` or `CapReasoning`. The routing plan's capability declarations naturally express this division.

This is why the docs are ordered: routing is the substrate. You can't have a translator specialist routed correctly until nodes can declare capability requirements.

## Trade-offs

- **Latency**: every reasoning → translate → execute → translate-back hop is real time. For tools that complete in <100ms (most reads), translator overhead may dominate the actual op. Mitigation: keep translator on a small fast model (xLAM-1.5B class, <50ms). Net wall-time should still be a wash because the *reasoning* model isn't burning cycles emitting JSON badly.
- **Universal vs conditional delegation**: "ONLY tool users may use tools" is the strong form. The alternative — allow direct calls from reasoning when intent is trivial — fragments the security gate (sometimes enforced, sometimes not). Recommendation: **universal**. Absorb the latency cost in exchange for an actual security perimeter and the reliability gains.
- **Intent vocabulary lock-in**: once `Type` enums ship, they're hard to evolve. Mitigation: version the vocabulary, allow `Type: "raw"` with a free-text `Hints["call"]` escape hatch for the long tail.
- **Observation translation cost**: `represent.result_to_observation` doubles the LLM hops per tool call. May not be worth running on every call — start with a heuristic ("if result < 200 tokens, pass raw; else translate"). Becomes a routing decision in its own right.

## Eval shape

Cells:
- `direct_tool_calls` — current behavior, reasoning model emits structured calls directly
- `intent_delegation` — full pattern: reasoning emits intent, translator emits call, act executes
- `intent_no_obs_translate` — partial: translate intent → call, but pass raw result back to reasoning

Scenarios: reuse the existing eval set (`simple_read`, `multi_step_explore`, `targeted_edit`).

Metrics:

| Metric | Direction | What it tells us |
|---|---|---|
| `tool_call_success_rate` | higher better | Did the structured call parse + execute? (Specialist >> reasoning model — the primary reliability claim.) |
| `intent_satisfied_rate` | higher better | Did the executed call actually serve the original intent? (LLM-as-judge; the correctness claim.) |
| `session_latency` p50/p95 | not-much-worse | Does the indirection regress wall-time? (We're betting on roughly break-even.) |
| `session_final_quality` | higher or equal | Does delegation improve final-answer quality via reliability? |
| `unauthorized_call_rate` | lower better | How often does the translator block an intent it shouldn't have emitted? (Security claim.) |

Success criterion for shipping: `tool_call_success_rate` jumps materially (target 90%+ from whatever today's number is) with no regression in `session_latency` or `session_final_quality`.

## Implementation slices

1. **Define Intent type + Type vocabulary** in `pkg/cognition/dag/intent.go`. Seeded from existing `act.*` surface. Includes JSON schema for translator validation.

2. **`represent.intent_to_tool_call` op with heuristic handler.** v0 handler is pattern-matching (regex / type-switch on Intent.Type → NodeSpec). Ships before the LLM-based translator so the architecture lands without depending on model availability.

3. **Migrate `act.read_file` as the canonical first split.** Reads are simple, idempotent, non-destructive — the safest target. Reasoning nodes that previously emitted `act.read_file` directly now emit a `read` intent.

4. **Wire `decide.coding_turn` to emit intent.** First reasoning-side migration. Existing handler swaps direct-spawn for intent-spawn.

5. **Add policy gates in translator.** Path sandboxing, confirmation prompts, audit logging. Now the security claim has teeth.

6. **Migrate remaining `act.*` nodes.** `act.run_shell`, `act.write_file`, etc. One per slice for safety.

7. **`represent.result_to_observation` (optional).** Heuristic v0 (raw passthrough below threshold, summarize above), LLM-based handler later.

8. **Swap heuristic translator for small-model handler.** Tiny model embedded via `//go:embed` (intersects with `docs/picker-as-node.md` phase 4 — same tooling). Trained on the journal's accumulated `Intent → NodeSpec` pairs.

## Open questions

1. **Intent vocabulary scope.** Start with read/write/search/run/edit (mirroring existing acts) or broader (analyze, validate, retry, ...)? Lean small for v1, expand from observed reasoning-side emissions.
2. **Single-step vs multi-step intents.** Does an intent always map to one act, or can it map to a sub-DAG ("read these N files and tell me their relationship")? v1 single-step; multi-step is its own design once we see the demand.
3. **Backward compat during migration.** Should we keep the direct-call path as fallback while migrating, or hard-cut per node? Per-node migration with hard cut is cleaner; partial-migration of a single node is too messy to test.
4. **Where the translator's traces live.** New journal entry type (`represent.translation`) or extension to existing `dream.insight` / similar? Probably new — translation is structurally distinct.
5. **Failure modes when intent can't be translated.** Fail closed (reasoning node sees error, must re-emit) or fail open (translator best-guesses, logs uncertainty)? Default fail closed — better signal for the eval, less risk of unauthorized acts.

## Related

- `docs/per-node-routing-plan.md` — Pillar #1, the routing substrate this depends on
- `docs/picker-as-node.md` — Pillar #2; the embedded-tiny-model tooling for v3 translator handler is shared
- `docs/tool-surface.md` (referenced from `pkg/cognition/dag/registry.go`) — `AxisContract` semantics the translator enforces
- `docs/journal.md` — adding the translation writer-class
- Memory: [[project_dag_nodes_are_micro_decisions]] — the translator IS a micro-decision in the middle of the reasoning loop; the split makes the architecture homogeneous (decisions all the way down)
