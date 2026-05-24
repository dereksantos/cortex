# Per-Node Model Routing Plan

> **Status:** Plan. Pillar #1 of three-pillar transition. Successor to
> `derek.s/model-registry-size-aware-reads` (PR #53). Companion docs:
> [`picker-as-node.md`](picker-as-node.md) (Pillar #2 — picker evolution),
> [`intent-execution-split.md`](intent-execution-split.md) (Pillar #3 —
> intent + delegated execution). This doc lands the substrate both
> companions depend on.

## Problem

The harness is currently unusable on the local stack. Live traces
(2026-05-23 against `chatterbox/Qwen3-Coder-30B-A3B-Instruct-GGUF`)
show every node running on the session default:

```
decide.next        lat=17220ms model=(default)
decide.tool_call   lat= 8173ms model=(default)
decide.coding_turn lat=852261ms model=(default)
sense.classify_intent lat= 2700ms model=(default)
```

The fast/slow distinction here is misleading. The deeper problem is
**reliability**: Qwen3-Coder-30B is trained for code completion, not
function-call JSON. It emits malformed tool calls regularly. Every
`decide.tool_call` failure cascades into wasted reasoning hops, which
is why multi-minute sessions become multi-failure sessions.

Cause: per-node `attrs.model` overrides exist in `NodeSpec`, but
nothing populates them. The LLM in `decide.next` *can* emit
`"model": "<specialist>"` on spawns, but it doesn't reliably — and
"improve the prompt" is the wrong fix. The fix is making model choice
a **property of the node**, not a per-spawn LLM elective.

The route framing isn't "make it faster." It's **"make each node
correctly match a model trained for that node's task."** Speed is
downstream; reliability is the load-bearing claim.

## Design

Each `NodeSpec` declares its capability requirements intrinsically.
The executor resolves the requirement to a model at spawn time via the
`ModelRegistry`. No central policy table; no Role taxonomy; no
separate routing package. The capability requirement lives in the same
file as the handler.

### Capability declaration on `NodeSpec`

```go
type NodeSpec struct {
    // ... existing fields (Function, Op, Inputs, Outputs, ...)
    Requires []string // ordered preference: try first, fallback to next.
                     // Tags from pkg/llm.Cap* (incl. :specialist variants).
}
```

`Requires` is an **ordered preference chain**, not an unordered set.
The picker walks it in order and returns the first match. Empty
`Requires` means "any model — use session default."

Example declarations:

```go
// Tool-call JSON emission: needs a specialist, falls back to any tool-caller.
decide.tool_call:    Requires: []string{CapToolCallingSpecialist, CapToolCalling}

// Coding turn: needs a coding specialist, falls back to any coder.
decide.coding_turn:  Requires: []string{CapCodingSpecialist, CapCoding}

// Intent planning: needs reliable structured emission.
decide.next:         Requires: []string{CapToolCallingSpecialist, CapToolCalling}

// Cheap classification.
sense.classify_intent: Requires: []string{CapToolCalling}

// Compression / summarization: any chat model is fine.
attend.compress:     Requires: []string{}
```

### Specialty tag convention

Existing capability tags in `pkg/llm/capabilities.go` cover
`coding`, `tool-calling`, `reasoning`, `embeddings`, `reranking`,
`vision`. We add a **`:specialist` suffix convention** as a sibling
tag:

```go
const (
    CapCoding              = "coding"
    CapCodingSpecialist    = "coding:specialist"

    CapToolCalling             = "tool-calling"
    CapToolCallingSpecialist   = "tool-calling:specialist"

    CapReasoning             = "reasoning"
    CapReasoningSpecialist   = "reasoning:specialist"
)
```

Rules:
- The specialist tag **implies** the base tag (enforced when labels
  are assigned in `InferCapabilities` and when an endpoint passes
  through `:specialist` in `EffectiveLabels`).
- Specialty means *trained primarily for this task* — xLAM-1.5B for
  tool-calling, DeepSeek-Coder for code. Not "best at" or "biggest" —
  *trained for*.
- A model can hold multiple specialist tags (DeepSeek-Coder-V2 is
  `[CapCoding, CapCodingSpecialist, CapToolCalling]` — coder
  specialist, also generally tool-capable).

Why same namespace + suffix (not a separate `Specialty` field on
`ModelInfo`):
- `Requires []string` stays flat. Single shape for both base and
  specialty requirements.
- Visible in `cortex models` output — operators sanity-check what the
  inference thinks each model is for.
- Lemonade-style endpoints can advertise specialty tags through the
  existing `labels[]` passthrough — no new probe surface needed.
- A model can be a specialist in multiple things (the field-shaped
  alternative forces an awkward single choice).

### Picker semantics (mechanical, no hidden preferences)

`Registry.PickForCapabilities(ctx, requires []string) (ModelInfo, bool)`:

```
For each cap in requires (in order):
  candidates = models where m.HasCapability(cap)
  if candidates is empty: continue to next cap
  sort candidates by:
    1. Local before cloud
    2. If cap ends in ":specialist": smaller before larger
       Else: larger before smaller
    3. Stable by id
  return candidates[0], true

If chain exhausted: return zero, false (caller falls back to default)
```

No "secretly prefer specialist" if the node didn't ask. The ordered
chain IS the preference; what's written is what runs.

### Resolution at spawn time

1. `NodeSpec.Attrs["model"]` set explicitly → per-spawn override wins.
2. `.cortex/config.json` routing pin (slice 9) → operator-declared per-node-type model. Reason `config:<modelID>`.
3. `NodeSpec.Requires` non-empty → `registry.PickForCapabilities(requires)` → ModelInfo. Reason `requires:<modelID>`.
4. Build provider via `ProviderFactory.Get(modelInfo.ID)`.
5. No match anywhere → fall back to session default (reason `default`); never fails the session.

A stale model id at any step (typo, uninstalled) silently falls through to the next — keeps the harness usable when an override drifts out of date.

### Executor wiring (Option A from earlier draft)

Handler signatures stay unchanged. The resolved provider is carried
through `dag.Budget` (or a sibling carrier on the Budget struct):

```go
type Budget struct {
    // ... existing fields ...
    Provider llm.Provider // resolved by executor pre-handler from spec.Requires
}
```

Handlers that today read `cfg.Provider` switch to reading `b.Provider`
when set, falling back to `cfg.Provider` otherwise. Migration is
per-handler; no global flag-day change.

## Implementation slices

1. **`NodeSpec.Requires` field + capability constants.** Add
   `Requires []string` to `NodeSpec` in
   `pkg/cognition/dag/registry.go`. Add `:specialist` constants to
   `pkg/llm/capabilities.go` + extend `InferCapabilities` patterns
   (xLAM, phi-3-mini-tools, coder families). Table-driven tests for
   inference. **No behavior change yet.**

2. **`Registry.PickForCapabilities` on the model registry.** New
   method on `ModelRegistry` in `pkg/llm/registry.go`. Wraps the
   ordered-chain walk + tiebreakers. Table-driven tests with a fake
   registry.

3. **Executor pre-resolves provider per spawn.** Executor consults
   the node's `Requires`, calls the registry, populates
   `Budget.Provider`. Handlers untouched. Add a trace field so the
   picked model appears in node trace lines (replaces `model=(default)`).

4. **Migrate `decide.tool_call`.** First handler to read
   `b.Provider`. Add a test confirming `attrs.model` override still
   wins. This is the reliability win.

5. **Migrate `decide.next`.** Second high-value handler.

6. **Migrate `sense.classify_intent`.** Smallest, verifies the pattern
   for non-decide nodes.

7. **Migrate `attend.compress` / `attend.distill`.** Last (most-tested
   handlers, lowest urgency).

8. **REPL wiring.** `cmd/cortex/commands/repl.go` passes the registry
   into the executor at session start. A no-op default policy lands
   in slice 3 so earlier migrations have somewhere to land; this
   slice activates the full picker chain.

9. **Operator override (`.cortex/config.json`).** Allows pinning
   specific `(node_qname → model)` routes. Required for ops — first
   time the auto-pick is wrong, operators need an escape hatch
   without editing code.

## Eval shape

The thesis: per-node routing makes the harness reliable. Latency is
downstream of reliability (fewer retries, fewer dead ends). Eval must
measure reliability first.

### Comparison cells

| Cell | Description |
|---|---|
| `baseline` | Session model runs every node (current behavior — and the unusable one) |
| `routed_local` | `decide.tool_call` + `decide.next` → local specialist; everything else session |
| `routed_full` | Every node policy-routed per its `Requires` chain |
| `manual` | Operator-pinned routes from `.cortex/config.json` |

### Scenarios

- `simple_read` — "tell me about cmd/cortex/main.go"
- `multi_step_explore` — "how does the cognitive architecture work?"
- `targeted_edit` — "rename `effectiveContextWindow` to `runtimeContext`"
- `swe_bench_lite` — one small fix from SWE-bench-Lite as industry anchor

### Metrics (reliability first, latency second)

| Metric | Direction | What it tells us |
|---|---|---|
| `decide.tool_call.success_rate` | higher better | Did the structured emission parse + dispatch? (The primary claim.) |
| `decide.tool_call.malformed_json_rate` | lower better | Direct measure of specialist value |
| `session.no_progress_rate` | lower better | How often did the loop bail vs complete? (Reliability proxy at session level.) |
| `session.completion_rate` | higher better | Did the session reach a terminal state? |
| `session.final_answer_quality` | ≥ baseline | Did routing cost us answer quality? (No-regression gate.) |
| `session.total_wall_ms` | not-much-worse | Wall-time is secondary — reliability matters more |
| `endpoint_rtt_ms` per node | observable | Watch for multi-endpoint stacking that could regress sessions |

### Success criteria for shipping

- `decide.tool_call.success_rate` jumps materially over `baseline` (target: 90%+ on `routed_full` from whatever the baseline number is — measure first, target then)
- `session.no_progress_rate` drops on `routed_full` vs `baseline`
- No regression in `session.final_answer_quality`
- `session.total_wall_ms` doesn't regress catastrophically (multi-endpoint stacking caught early)

The aggressive 40% latency target from earlier drafts is dropped — it
was extrapolated from cost composition the trace didn't actually
support (`decide.coding_turn` dominates wall-time and stays on the
session default).

### Failure modes to watch for

- **Specialist emits worse JSON than the generalist.** Some
  "specialists" are specialized for one tool shape and bad on others.
  Mitigation: per-(node × model) reliability measurement before
  declaring a specialist shippable.
- **Multi-endpoint latency stacking.** Every node hits a different
  endpoint → cumulative network RTT exceeds in-model latency wins.
  Mitigation: prefer local; measure `endpoint_rtt_ms` per spawn.
- **Specialist context bloat.** The catalog injection is shared
  across all nodes; if a small specialist can't hold it, prompt
  truncation breaks the call. Mitigation: bound catalog size per
  `EffectiveContextWindow`.
- **No specialist available locally.** `Requires` chain exhausts, falls
  back to session default. This is the current behavior — no
  regression, but the operator should see a one-line warning.

## What this plan deliberately does NOT do

These belong to companion docs to keep this PR shippable:

- **Picker evolution to bandit / learned classifier.** The picker stays
  a mechanical chain-walk in v1. See [`picker-as-node.md`](picker-as-node.md)
  for the swappable-handler evolution.
- **Intent + delegated execution.** `act.*` nodes remain as-is.
  Reasoning nodes still emit structured tool calls directly. See
  [`intent-execution-split.md`](intent-execution-split.md) for the
  architectural split.

Both companions depend on this doc's substrate
(capability-declared nodes). Ship this first; they unblock cleanly
after.

## Open questions

1. **Which specialist tags get inferred today?** xLAM, phi-3-mini-tools,
   DeepSeek-Coder, Qwen-Coder — slice 1 has to enumerate. Operator-side
   labels (Lemonade) pass through, so getting inference right matters
   most for raw Ollama listings.
2. **`Requires` field in JSON persistence.** Currently `nodeSpecPersist`
   (in `pkg/cognition/dag/registry.go`) only persists identity-shaped
   fields, with registration-time fields reconstituted by qualified
   name. `Requires` is registration-time → no JSON change needed.
   Verify this holds.
3. **Cache invalidation for per-session picker decisions.** v1 picker is
   stateless (every spawn re-walks the chain). Hot-path overhead is
   negligible. Leave as-is; revisit if profiling shows it matters.
4. **Cold-start: what if no specialist is available anywhere?**
   `Requires` chain exhausts, executor falls back to session default
   with a one-line warning. Don't fail the session — the harness has
   to keep working with whatever models exist.

## Related

- `pkg/llm/capabilities.go` — capability tags + inference (already exists; extend with `:specialist`)
- `pkg/llm/recommend.go` — `recommendOne` role-pick (Role taxonomy stays for onboarding flow; no longer load-bearing for routing)
- `pkg/llm/registry.go` — `ModelRegistry` (delivered in PR #53); add `PickForCapabilities`
- `pkg/cognition/dag/registry.go` — `NodeSpec` (add `Requires`)
- `pkg/cognition/dag/ops/model_route.go` — V0 routing node; obsolete once `decide.coding_turn`'s parent uses intrinsic `Requires`. Retire in a later slice.
- `docs/picker-as-node.md` — Pillar #2, picker evolution
- `docs/intent-execution-split.md` — Pillar #3, intent + delegated execution
- `docs/eval-strategy.md` — thesis claim #1 (multi-model leverage); this plan is direct evidence
- Memory: [[project_direction_small_model_amplifier]] — Cortex as salience layer for small models; this plan operationalizes "right model on the right node"
