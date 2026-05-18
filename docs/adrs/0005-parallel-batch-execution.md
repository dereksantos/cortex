# ADR-005: Parallel batch execution in the DAG executor

**Status:** Accepted (Stage 4)
**Date:** 2026-05-18
**Context:** `docs/dag-build-plan.md` Stage 4 — parallelism + budget
rollover; `docs/dag-protocol.md` "Spawning" / "Budget model".

## Decision

The DAG executor walks the pending set as a **time-batched parallel
frontier**, not a strict FIFO queue:

1. Each tick, drain the entire current pending set into a `batch`.
2. Launch every item in `batch` concurrently (one goroutine per item).
   Each goroutine sees the same pre-batch `budget` snapshot.
3. After all goroutines join, serialize the post-batch work in
   `WallStart` order: apply each handler's `CostConsumed` to the
   shared budget, then call `scheduleChildren` (which applies
   depth / `MaxFanout` / per-op `Cost` pre-checks against the
   running budget).
4. Loop. The pre-batch `Exhausted` check at the top of the tick is
   what stops the walker; a batch that pushes the budget negative
   does not get its spawns scheduled, because the next tick's
   `Exhausted` check trips first.

## Why this shape

A "ready set" frontier is the simplest correct interpretation of
"independent siblings run in parallel" given that v0 has no explicit
inter-sibling edge model — spawn ordering is the only dependency, and
spawns are appended only after the parent's handler returns. Any
item currently in `pending` is therefore independent of any other
item currently in `pending`.

Per-item goroutines (no worker pool) are fine for the realistic
batch sizes (1-10 nodes per tick at the call-site profile we have).
A worker pool can be retrofitted later if a fan-out planner pushes
batches into the hundreds.

## Mutability + serialization

Mutable state shared across goroutines:

| State | Protection |
|---|---|
| `Budget` (shared running budget) | Mutated only after `wg.Wait()`, in the serialized post-batch loop |
| Auto-assigned child ID counter | `sync.Mutex` (`spawnIDMu`) |
| `Trace.Entries`, `Trace.SpawnRefusals`, `Trace.TotalExecuted` | Mutated only in the serialized post-batch loop |
| `traceCB` | Called from the serialized post-batch loop |
| `pending` slice | Replaced wholesale at tick boundary; never read+written concurrently |

Handlers themselves are the caller's contract — they receive an
immutable `Budget` snapshot and a per-call `ctx`. Any handler that
mutates shared state is responsible for its own concurrency. The
existing handler set is either pure (mock handlers, prompt-template
LLM ops, embedding lookups) or already protected (Stage 3
coding_turn tool dispatch uses the harness's own locking).

## Trace ordering

Trace entries within a batch are emitted in `WallStart` order
(stable sort) so the on-disk JSONL projection is monotonic in time
regardless of goroutine scheduling. Cross-batch ordering is natural —
batch N's entries are appended before any of batch N+1's.

## Backward compatibility

A `SetSequential(bool)` toggle preserves the Stage 1-3 FIFO single-
threaded walk. The mechanic-4 unit test pins this path because the
"in-flight finishes, no new spawns" semantic it tests is
fundamentally a sequential semantic — in parallel mode the whole
current batch executes regardless of whether one item in the batch
would have exhausted the budget alone.

The mechanic-4 YAML fixture passes under either mode because it
declares `cost_hint` on its children, so the pre-spawn `CanAfford`
check refuses them at scheduling time rather than relying on
mid-batch exhaustion.

## What this does NOT change

- The depth cap, per-op `MaxFanout`, and pre-spawn `CanAfford` checks
  retain their Stage 1-3 semantics.
- Handler context (`NodeIDFromContext`) still surfaces the executing
  node's own ID; act-op handlers that fabricate synthetic child rows
  continue to work.
- The `Spawn` mechanism is unchanged — handlers still return spawned
  children inline; the executor still owns scheduling.

## What follows

ADR-006 (cross-turn budget rollover, also Stage 4) defines how
deferred spawns from one turn re-enter the seed of the next, and
how the per-project deferred-spawn queue is bounded against stale
replay.

## Verification

- `go test -race ./pkg/cognition/dag/` clean
- `TestExecutor_Parallel_BatchConcurrency` proves siblings actually
  run concurrently (wall time ≈ single sleep, not Nx)
- `TestExecutor_Parallel_TraceOrderedByWallStart` proves trace
  ordering
- `TestExecutor_Parallel_BatchExhaustionAdmitsAll` documents the
  parallel-mode batch-burst semantic
- All 5 mechanic evals PASS (`./bin/cortex eval --suite=mechanic`)
- All existing package tests PASS under race detector
