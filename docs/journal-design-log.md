# Journal — Implementation Design Log

Decisions and discoveries made during the 33-slice implementation on
`feat/journal`. Read [`journal.md`](./journal.md) first for the
architecture and [`journal-implementation-plan.md`](./journal-implementation-plan.md)
for the slice plan.

This log is honest about what the plan got right, what it got wrong,
and what's left as deferred follow-up work.

---

## 1. The biggest delta: there is no SQLite

The plan and v0.1 of `journal.md` described the read side as
"SQLite (FTS5, sqlite-vec, joins)". The implementation discovered
this was wrong: `internal/storage/storage.go` is JSONL-with-in-memory-
indexes, not a SQL engine. The package doc opens with "Package storage
provides event sourcing storage with JSONL files and in-memory
indexes."

The CQRS property is unchanged — the storage layer is still a
regeneratable read-side projection of the journal. What changed:
- All "SQLite" references in `journal.md` and `CLAUDE.md` say
  "storage layer" or name the specific JSONL projection file.
- The "Why files for the write side" arguments still hold — append +
  fsync, per-writer-class isolation, inspectability — but they're
  arguments against any SQL engine, not specifically SQLite.

Implication for the future: if scaling pressure ever pushes for a
real indexed read side (FTS5, vector search), it's a swap of one
projection consumer, not an architecture change. The journal stays
authoritative.

## 2. D1+D2: dual-write transition, not single commit

The plan called D1 + D2 a back-to-back pair landing in the same
iteration. D1 added journal emission alongside the existing direct
`storage.StoreInsightWithSession` call; D2 added the projector
and conditionally removed the direct call.

The condition: when Dream has a `journalDir` configured, the direct
storage write is suppressed (the projector handles it). When
`journalDir == ""`, the direct write remains as a fallback —
necessary because some Dream unit tests construct Dream without
a journal directory. Production wiring (in `cortex.go`) always sets
`journalDir`, so the direct write only fires in tests.

Cleaner long-term: make Dream tests construct a journal directory
too, then unconditionally remove the direct write. Defer until a
future cleanup pass.

## 3. Dream sources: only memory_md and claude_history wired

O2 plan: "Dream sources emit observation entries on read". Implemented
only for `memory_md.go` and `claude_history.go`. Skipped:

- **git source**: emits one DreamItem per commit-SHA. Observing each
  would multiply observations by Sample size; needs a per-SHA dedup
  story before wiring.
- **project source**: scans many files per Sample, often a tree
  traversal. Observing per file would flood the observation log.

The `Observer` helper is generic; wiring is a small follow-up. The
slice still demonstrated the pattern across two sources, which was
enough for the architectural contract.

## 4. The plan's eval bucket: parallel, not unified

Plan: E2 should "subsume `cell_results.jsonl`; remove dual-write
path".

Reality: `internal/eval/v2/persist_cell.go` is governed by a hard
constraint (#8 — "every CellResult writes to BOTH backends required
by hard constraint"). The eval-harness contract explicitly mandates
SQLite + cell_results.jsonl alongside each other. Refactoring eval
persistence to flow through the journal pipeline is a larger
contract negotiation than I could do in this loop.

What landed:
- `eval.cell_result` journal entry type matching CellResult fields.
- Stub projector that records a parallel `eval_cell_results.jsonl`
  in storage.
- `journal/eval/` indexer wired into the default set.

What's deferred:
- Refactoring `internal/eval/v2/persist*.go` to write the journal
  entry as the source of truth and project to the existing SQLite
  + cell_results.jsonl via the indexer pipeline.

This means `cortex journal verify` does NOT yet count eval entries
authoritatively — they're a side channel. The unification needs a
follow-up.

## 5. retrieval_stats.json and daemon_state.json: additive, not replaced

Plan: Z1 and T1 should "subsume" these legacy snapshot files.

Reality: replacing them requires rewriting daemon startup state
recovery + the watch view that reads `retrieval_stats.json`.
Z1 / T1 added `resolve.retrieval` / `think.session_context` journal
emission alongside the existing snapshot writes. The journal is the
"new" source; the legacy files still get written for backward
compat. A future cleanup removes the legacy paths.

## 6. think.topic_weight: type defined, not wired

The journal package declares both `think.topic_weight` (single-topic
update) and `think.session_context` (snapshot). Only session_context
is emitted from Think. Granular topic_weight emission was decided
unnecessary because snapshots cover the same state; if granular
replay timing becomes load-bearing for evals, wire it at
`updateTopicWeights` deltas in a follow-up.

## 7. Replay --config-overrides: skeleton only

`cortex journal replay --config-overrides=KV` parses the flag but
does not yet thread the overrides through cognition. The X2 slice
landed the structural skeleton — walking a range of entries,
printing summaries — because the actual counterfactual run requires
re-invoking Dream / Reflect / Resolve with overridden model, prompt-
hash, or budget, which is its own substantial piece of cognition
plumbing.

What the skeleton makes possible: a follow-up slice can add real
counterfactual runs without touching the journal layer. All needed
data is in the journal entries.

## 8. Cross-class source-offset verification: permissive

`cortex journal verify` checks that every entry's `Sources` offsets
resolve somewhere in the journal — but the resolution is permissive
across writer-classes. The strict version would require knowing
which class each source offset belongs to, which would either need
class-tagged offsets (a schema change) or a fancier index.

For the common case (within-class source references, e.g. nuance
insights referencing their parent insight), the current check is
sufficient. Strict cross-class verification can be added later.

## 9. internal/queue/ fully removed in C6

The plan called for removing `internal/queue/` after migration was
validated. C6 did this cleanly:
- Removed the package + its tests.
- Removed `Processor.AddQueueDir` compat shim (replaced with
  `AddJournalDir`).
- Removed `.cortex/queue/{pending,processing,processed}/` from
  `Config.EnsureDirectories`.
- Updated `cortex-journal-test` fixtures and `pkg/config/config_test.go`
  to expect `.cortex/journal/capture/` instead of queue dirs.

Field-side, `Config.Queue` (the unused "file"/"direct" strategy
string) is left in the config struct because removing it would break
JSON-config-file parsing for users with existing `config.json`.

## 10. Per-segment flock for capture

Capture is invoked per-hook by Claude Code — short-lived processes
that can race for the same capture segment file. Without locking,
two concurrent appends could interleave bytes inside PIPE_BUF
boundaries (512B on macOS, 4KB on Linux), producing corrupt JSONL.

Slice C1 added `journal/lock_unix.go` (syscall.Flock LOCK_EX) and
`journal/lock_windows.go` (no-op). The lock is acquired during
`openSegment` and released when the file closes, scoping it cleanly
to one Writer's lifetime.

Latency cost: BenchmarkCapture_CaptureEvent shows ~4.9 ms/op on
M4/APFS with FsyncPerEntry — well under the 50ms warning threshold.
The cost is honest: input durability is non-negotiable per
principle 4.

## 11. Default-indexer-count drift

The processor's default indexer set grew with each writer-class:
- F2: 0 (no auto-add in v0)
- C2: 1 (capture)
- O3: 2 (+observation)
- D2: 3 (+dream)
- R2: 4 (+reflect)
- Z2: 5 (+resolve)
- T2: 6 (+think)
- B3: 7 (+feedback)
- E2: 8 (+eval)

The `TestNew` test in `processor_test.go` updated its expected count
at every writer-class slice. Worth noting in case a future slice adds
another class — the test fails loudly with a clear message.

## 12. Observations: substrate hash, not substrate copy

Principle 3 says external substrates stay external. The
`ObservationPayload` records `URI + content_hash + size + modified`
— no substrate bytes. The projection's `(URI, content_hash)` dedup
makes re-observation of unchanged substrate a no-op at the storage
layer too.

A consequence: observations alone don't let you reconstruct what
Dream saw. You need the substrate (Claude transcripts, git, memory
files) to be intact. This is principle 10 (Claude transcripts are
upstream of last resort) — Cortex's journal is canonical for Cortex,
not for the universe.

---

## Summary of deferred work

A subsequent follow-up pass landed nearly all of the originally
deferred items (see commits `feat(journal): E2.1..2.3`, `X2.1..2.3`,
`D2`, `O2`, `X3`, `T1` on this branch). Status:

1. **Eval persistence unification** — **DONE**. `PersistCell` now emits
   `eval.cell_result` as the source of truth and the indexer projects
   it via `internal/eval/v2.ProjectCellFromEntry`. The storage-side
   `eval_cell_results.jsonl` side-channel is removed; `cortex journal
   verify` counts eval entries authoritatively.
2. **Replay --config-overrides threading** — **DONE**.
   `ParseConfigOverrides` enforces an explicit-allow-list (model /
   provider / temperature / max_tokens). `cortex journal replay
   --config-overrides=... --execute --class=reflect` re-invokes Reflect
   with the override, computes Jaccard@K against the original ranking,
   and emits one `replay.counterfactual` journal entry per source.
3. **retrieval_stats.json / daemon_state.json removal** — **STILL
   DEFERRED**. These remain because removing them requires migrating
   the watch UI off the snapshot files (see `cmd/cortex/commands/
   watch_state.go`). `daemon_state.json` is also a runtime heartbeat
   that's not really a journal-replaceable concept. The journal
   projections already exist; what's missing is the UI migration.
4. **Dream's direct-storage fallback removal** — **DONE**. The
   conditional `d.journalDir == "" && d.storage != nil` fallback in
   `internal/cognition/dream.go` is removed. Tests that exercised it
   pass `storage = nil` so the change is a no-op for them.
5. **Wire git and project Dream sources to observer** — **DONE**.
   `GitSource` emits `observation.git_commit` per commit returned by
   Sample. `ProjectSource` emits `observation.project_file` per file
   that contributes regions (deduped within the Sample). The
   `observation.project_file` entry type was added for this.
6. **Strict cross-class source-offset verification** — **PARTIAL**.
   `cortex journal verify` is now strict for entries whose payload
   declares the source's writer-class (today: `replay.counterfactual`
   via `SourceClass`). Other types still resolve permissively but the
   verifier now counts cross-class ambiguity so the operator sees the
   signal-to-noise ratio. Full strictness for every writer-class would
   still need class-tagged offsets in the entry envelope.
7. **think.topic_weight granular emission** — **DONE**.
   `updateTopicWeights` diffs old vs new weights and emits one
   `think.topic_weight` entry per material change (>= 0.05). The
   processor registers a validating no-op projector so the cursor
   advances during ingest; storage-side per-topic history is a future
   add-on.
