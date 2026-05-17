# ADR-001: `decide.coding_turn` internal structure

**Status:** Accepted (Stage 1 v0)
**Date:** 2026-05-17
**Context:** `docs/dag-build-plan.md` Stage 1 deliverable 6 + week-1 question

## Decision

In Stage 1 v0, `decide.coding_turn` runs the existing LLM agent loop
**inline** within a single handler call. It returns a single
`NodeResult` with `Out` containing the final response, `CostConsumed`
reporting total turn cost, and `Spawn: nil` (no child nodes).

In Stage 3 (loop rewrite), `decide.coding_turn` is refactored to
**spawn `act.*` children** for each tool call the LLM makes during
the turn. The tool calls become first-class nodes in the trace with
their own `parent_node_id` chain, axis contracts, and budget
accounting.

## Rationale

**V0: inline.**
- LLM's classical tool-calling discipline is well-tested and works
  reliably on the existing harness.
- Forcing it to emit DAG-spec spawn lists in V0 would either require
  reformatting the LLM's tool calls into spawn specs (extra hop), or
  changing the LLM's interface (risks coding quality regression for
  no immediate gain).
- V0's purpose is to prove the protocol works end-to-end. The agent
  loop as one opaque node is sufficient — telemetry shows the node
  ran, consumed cost, returned output.

**Stage 3: spawn children.**
- Once V0 is stable, tool calls *should* surface as child nodes
  because:
  - Axis contracts (`tool-surface.md`) apply per tool call; only
    making them DAG nodes enforces this uniformly.
  - Per-tool-call cost accounting feeds the Modulate gate.
  - Tree-shape evals (Phase 6) want to see what tools the model
    chose, not just a single `coding_turn` blob.
- The spawn shape: `decide.coding_turn` handler invokes the LLM loop
  but intercepts each tool call before dispatch. Each tool call
  becomes an `act.*` spawn returned in the handler's spawn list at
  end-of-turn (or via an incremental spawn API if we add one later).

## Consequences

- V0 telemetry: one `decide.coding_turn` row per turn, no per-tool
  children. Cost reflects total LLM + tool wall time.
- Stage 3 telemetry: N+1 rows per turn (1 coding_turn + N tool
  children). Backwards-compatible — analyses can roll up children to
  derive the V0 shape.
- The LLM's tool-calling code path is unchanged in V0; Stage 3 wraps
  it with a dispatch interceptor.

## Alternatives considered

**Make the LLM emit spawn specs from day one (rejected for V0).** Would
require either retraining the model or wrapping every tool call response
into a synthetic spawn list — high risk for V0's "prove the protocol"
goal.

**Skip Stage 3 and keep coding_turn opaque forever (rejected).** Would
mean axis contracts can't be enforced per tool call, defeating the
purpose of the `act` function category.
