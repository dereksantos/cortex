# ADR-002: Budget pass-through to LLM tool calls

**Status:** Accepted (Stage 1 v0)
**Date:** 2026-05-17
**Context:** `docs/dag-build-plan.md` Stage 1 week-1 question

## Decision

In Stage 1 v0, `decide.coding_turn` consumes its budget as a **single
opaque block** — the handler reports total `CostConsumed` at end of
turn, regardless of how many LLM calls / tool calls happened
internally.

In Stage 3 (loop rewrite, paired with ADR-001), budget passes through
to each tool call as it spawns:
- The remaining turn budget is given to each `act.*` child spawn.
- Mid-turn, if remaining budget drops below an act node's `cost_hint`,
  the spawn is refused with `budget_exceeded` (per the pre-spawn check
  in `executor.go`).
- The LLM's own LLM-call portion (the tokens it consumes generating
  the next tool call) is accounted against the `decide.coding_turn`
  node's `CostConsumed`.

## Rationale

**V0: opaque.**
- The existing LLM agent loop has its own internal budget mechanisms
  (max_tokens, max_turns); intercepting and overriding them in V0
  risks regression for no immediate gain.
- The executor still enforces the *outer* turn budget — if the LLM
  loop runs long, `decide.coding_turn` will report a CostConsumed
  large enough that downstream node spawns get refused via
  pre-spawn check (Modulate model).
- V0's purpose is to prove budget decay works at the executor level,
  not at per-LLM-token granularity.

**Stage 3: per-tool pass-through.**
- Once `act.*` calls are first-class spawned nodes (per ADR-001),
  giving each one its slice of remaining budget makes graceful
  degradation real: the LLM emits 5 tool calls, the first 3 fit,
  the 4th is refused, the 5th is refused — and the LLM gets a clean
  `budget_exceeded` signal mid-turn to wrap up.
- Per-tool cost hints (from the registry) drive the refusal — same
  Modulate model as elsewhere in the executor.

## Consequences

- V0: budget is decay-correct at the *turn* level, not at the
  *per-LLM-call* level inside `coding_turn`. A runaway agent loop
  consumes its full advertised cost; the next turn sees the budget
  drop.
- Stage 3: per-tool budget enforcement aligns with the rest of the
  executor and gives the LLM clean feedback when budget runs out
  mid-turn.

## Alternatives considered

**Per-LLM-token budget enforcement in V0 (rejected).** Would require
hooking into the streaming token loop of the LLM client and emitting
a stop-signal mid-completion. Too invasive for V0; would create a
parallel code path inside the LLM client that needs ongoing
maintenance.

**No budget pass-through, ever (rejected).** Would mean an LLM loop
could exhaust the entire turn budget on tool calls, leaving none for
`maintain.capture` / other post-turn nodes. Defeats the purpose of
having a budget at all.
