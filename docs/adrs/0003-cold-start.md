# ADR-003: First-turn bootstrap with empty journal

**Status:** Accepted (Stage 1 v0)
**Date:** 2026-05-17
**Context:** `docs/dag-build-plan.md` Stage 1 week-1 question

## Decision

A first turn against an empty Cortex journal succeeds without special
casing:

- `sense.prompt` always succeeds (user input is the trigger; nothing
  to look up).
- `attend.reflex` returns an empty `candidates` slice when the journal
  has no entries. Not an error — empty is a valid result.
- `decide.inject` (or any downstream node) handles empty `top`
  gracefully — produces an empty injected-context message (or skips
  injection entirely, per its own logic).
- `decide.coding_turn` runs against the original user prompt with
  whatever empty/minimal injected context was produced (possibly none).
- `maintain.capture` records the turn outcome to the journal — this
  is what populates the substrate for subsequent turns.

No "first-turn" flag is needed. The same DAG runs cold and warm; the
shape of the tree may differ slightly (no candidates to rerank, no
contradictions to detect) but no node crashes or returns an error.

## Rationale

The seed + grow + decay model naturally accommodates emptiness: each
node's handler is a self-contained function that takes inputs and
returns outputs. Empty inputs produce empty outputs; no exception
handling required at the protocol level.

This avoids:
- A "cold-start path" / "warm-start path" code split (which would
  double the test surface).
- The "what's a sensible default response when journal is empty?"
  judgment, which is best deferred to the LLM in `decide.coding_turn`
  rather than baked into the protocol.

## Consequences

- First-turn quality is bounded by the LLM's baseline capability —
  Cortex injection adds zero value when the journal is empty.
- Each handler must implement "empty input" correctly. Already true
  for all v0 handlers; new handlers added in Stage 2 (per-mode op
  expansion) must follow suit. Tests should include an empty-journal
  variant per op where it could plausibly receive empty input.
- The `maintain.capture` after the first turn is what makes the
  second turn potentially better than the first. This is the loop
  closing — captured turn N becomes Reflex-able context for turn N+1.

## Alternatives considered

**Special-case first turn with a "fast path" (rejected).** Would split
the code path and require detecting cold-start somewhere. Adds
complexity for marginal benefit (the cold path saves at most a Reflex
call's worth of latency).

**Pre-seed the journal at `cortex init` (rejected for V0).** Has merit
for some use cases (e.g., a project template's known patterns), but
is its own design decision deserving its own ADR. V0 ships without it
to keep first-run behavior fully empty-journal-handled.
