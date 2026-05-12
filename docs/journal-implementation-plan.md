# Journal — Implementation Plan

**Status**: pre-committed for loop execution. See [`journal.md`](./journal.md) for the architecture and the ten principles.

This document is the stable, executable counterpart to the architecture. It contains:

1. The I/O/C/M/C prompt for the implementation agent.
2. The MECE bucket partition.
3. The 33-slice checklist.
4. The dependency graph between buckets.
5. The loop kickoff command.

If the loop fails partway, this artifact stays put. `git reset --hard` to this commit on `feat/journal` to return to a clean retry state.

---

## I/O/C/M/C prompt for the implementation agent

### IDENTITY
Senior Go engineer working on Cortex. Event-sourcing / CQRS literate. Defensive about input-path durability. Terse. Never break the build. Never break capture.

### OBJECTIVE
Land the full journal architecture on `feat/journal` in an isolated worktree. Done when:

- Every slice in the F/C/O/D/R/Z/T/B/E/X/I/W checklist (below) is checked.
- `cortex journal verify` passes against the migrated journal.
- `cortex journal rebuild` reproduces SQLite from the journal alone, matching projection-row counts.
- `cortex journal replay` runs end-to-end against a historical capture range with config overrides and emits comparable results.
- All existing tests pass; new tests cover each writer-class projection and each cross-class command.
- `docs/journal.md`, `CLAUDE.md`, and the design-log entry reflect the landed system.

### CONTEXT
- Repo root: `/Users/dereksantos/eng/projects/cortex`. Worktree: `/Users/dereksantos/eng/projects/cortex-journal`. Branch: `feat/journal` (off `main`).
- Files in scope at start: `internal/queue/queue.go`, `internal/capture/capture.go`, `internal/processor/processor.go`, `internal/storage/storage.go` (read `StoreEvent`, `rebuildEventIndexes`), `internal/cognition/` (Dream, Reflect, Resolve, Think entry points), `internal/eval/v2/persist*.go`, `cmd/cortex/commands/` for new subcommands.
- Architecture: ten principles + writer-class taxonomy in `docs/journal.md`. Read it first.
- Entry envelope:
  ```json
  {"type": "<class>.<kind>", "v": 1, "offset": <int>, "ts": "<rfc3339>", "sources": [<offset>...], "payload": {...}}
  ```
- Segment rotation: 10MB or 10,000 entries; `NNNN.jsonl` zero-padded, widens cleanly.
- `.cortex/` is gitignored. The current `.cortex/queue/processed/` holds ~1,870 historical events to migrate.
- Testing: standard library `testing` only. Table-driven `t.Run`. Per `CLAUDE.md`.

### METHOD
1. Each loop iteration: pick the **next unchecked slice** respecting the bucket dependency graph below. Implement only that slice.
2. Run `go build ./... && go test ./...`. If green, commit (single conventional-commit message per slice). If red, stay on the slice next iteration and fix.
3. **D / R / Z / T are entry-emission + projection pairs** — the writer-class is broken between halves. The pair's two slices must land in back-to-back iterations; don't start a new bucket between them.
4. Never delete a legacy artifact (`internal/queue/`, `retrieval_stats.json`, `daemon_state.json`, `cell_results.jsonl`, `.cortex/queue/`) until its replacement projection passes `cortex journal verify` against the migrated data.
5. After each commit, write a one-line status: `slice [bucket].[N]/33: <name> — build/tests [g|r] — next: [bucket].[N+1]`.
6. Stop the loop when all 33 slices are checked and the OBJECTIVE criteria are met. Terminate the cron (or omit `ScheduleWakeup` in dynamic mode).

### CONTRACT
- Branch: `feat/journal` in worktree at `../cortex-journal`.
- One commit per slice. Conventional messages (`feat(journal): ...`, `refactor(capture): ...`, `chore(journal): migrate processed/ events`).
- Each commit independently buildable. Each commit passes `go build ./... && go test ./...`.
- New package: `internal/journal/` with `writer.go`, `reader.go`, `cursor.go`, `segment.go`, `entry.go`, `registry.go` and `*_test.go` for each.
- New commands under `cmd/cortex/commands/journal*.go`: `ingest`, `rebuild`, `replay`, `verify`, `migrate`, `show`, `tail`.
- Removed by end of loop: `internal/queue/` package, `.cortex/queue/` directory, `retrieval_stats.json`, `daemon_state.json`, `cell_results.jsonl` dual-write path.
- Docs updated: `docs/journal.md` reflects landed state; `CLAUDE.md` updated; new `docs/journal-design-log.md` capturing the architectural pivot.

---

## MECE bucket partition

Partitioning by **writer-class** (one bucket per `journal/<class>/`) plus four cross-cutting buckets for what doesn't belong to any single class. Each entry type, each migration, each command lives in exactly one bucket.

| Bucket | Surface | Owns |
|---|---|---|
| **F**. Foundation | `internal/journal/` | Writer (configurable fsync + per-segment lock), reader, cursor, indexer skeleton, projection registry, entry-type+version dispatch, CLI scaffold |
| **C**. Capture class | `journal/capture/` | `capture.event` entries, capture path migration, queue removal, ingest one-shot, rebuild for capture |
| **O**. Observation class | `journal/observation/` | `observation.{claude_transcript,git_commit,memory_file}` — content-hash + pointer, never substrate copy |
| **D**. Dream class | `journal/dream/` | `dream.insight` entries with source offsets; Dream stops writing SQLite directly; projection materializes insights |
| **R**. Reflect class | `journal/reflect/` | `reflect.rerank` entries with contradictions detected; projection to rerank cache + contradictions table |
| **Z**. Resolve class | `journal/resolve/` | `resolve.retrieval` entries; subsumes `retrieval_stats.json`; projection to stats tables |
| **T**. Think class | `journal/think/` | `think.topic_weight` / `think.session_context`; subsumes `daemon_state.json`; projection to session tables |
| **B**. feedBack class | `journal/feedback/` | `feedback.{correction,confirmation,retraction}` referencing graded derivation offsets; wires `/cortex-correct`, `/cortex-forget` |
| **E**. Eval class | `journal/eval/` | `eval.cell_result`; subsumes `cell_results.jsonl`; projection to existing eval tables |
| **X**. Cross-class ops | `cmd/cortex/commands/journal_*` | `rebuild` (DAG-ordered replay), `replay --config-overrides` (counterfactual eval primitive), `verify` (offset-ref integrity) |
| **I**. Inspection & privacy | tooling | `show <offset>`, `tail`, segment gzip on closure, local-only enforcement, `.gitignore` warning |
| **W**. Writing & docs | `docs/` + `CLAUDE.md` | `docs/journal.md` updates, `CLAUDE.md` update, design-log entry |

**MECE proof.** *Mutual exclusivity*: each entry type appears in exactly one writer-class; each command lives in exactly one bucket; no two buckets touch the same file. *Collective exhaustion*: each of the ten principles in `journal.md` maps to ≥1 bucket — P1→F+X, P2→C+D+R+Z+T+B, P3→O, P4→F (capability) + each class (choice), P5→B, P6→F+I, P7→F+C, P8→X, P9→C+O+D+R+Z+T+B+E, P10→O.

---

## Dependency graph

```
       F (foundation)
       │
       ├── C (capture)  ────────────────────────────────────────┐
       │                                                         │
       └── O (observation) ──────────────┐                       │
                                          │                      │
                       ┌──────────────────┼──────────────────┐   │
                       ▼                  ▼                  ▼   │
                       D (dream)          R (reflect)        Z (resolve)
                       │                  │                  │
                       │                  │                  T (think) ─┐
                       │                  │                  │           │
                       └────────┬─────────┴─────────┬────────┘           │
                                ▼                   ▼                    │
                                B (feedback)        E (eval) ────────────┤
                                │                                         │
                                └─────────────────────────────────────────┤
                                                                          ▼
                                                                  X (cross-class)
                                                                          │
                                                                          ▼
                                                                   I (inspection)
                                                                          │
                                                                          ▼
                                                                    W (writing/docs)
```

- **F** is the strict prerequisite for everything.
- **C** unblocks all downstream work (every derivation depends on capture entries existing).
- **O** is required by **D** and **R** (which read observations).
- **D / R / Z / T** are independent of each other after **C** and **O** are in. Could parallelize; loop runs them serially.
- **B** depends on at least one derivation class shipping (it grades them).
- **E** is independent after **F** but conventionally landed after **B** since `eval.cell_result` shares grading semantics.
- **X / I / W** are last — they assume all writer-classes exist.

Loop bucket order: **F → C → O → D → R → Z → T → B → E → X → I → W**.

---

## The 33-slice checklist

Each slice = one commit. Build + tests green at end. Conventional commit message. Pairs in D/R/Z/T land back-to-back.

### F. Foundation
- [ ] **F1**. Journal package skeleton — `Entry`, `Segment`, `Writer` (configurable fsync mode + per-segment lock), `Reader` (yields entry+offset), `Cursor`. Unit tests cover round-trip, segment rotation at size/count, fsync mode toggle.
- [ ] **F2**. Indexer — tails segments, dispatches by `{type, v}` via projection registry, advances cursor on success. CLI scaffolding for `cortex journal {rebuild,replay,verify,show,tail,migrate,ingest}` as not-implemented stubs.
- [ ] **F3**. Entry-type + version registry — registry maps `{type, version}` → projector. Unknown types log and skip (forward-compat).

### C. Capture
- [ ] **C1**. Capture writes to `journal/capture/` — replace `internal/capture/capture.go:writeToQueue` with journal writer (fsync per entry). Capture tests adapted.
- [ ] **C2**. Indexer registers `capture.event` projector → `storage.StoreEvent`. Processor tests adapted.
- [ ] **C3**. `cortex ingest` one-shot mode — runs indexer until caught up to journal tail, exits.
- [ ] **C4**. `cortex journal migrate` — packs `.cortex/queue/processed/*.json` (ID-timestamp ordered) into capture segments. Verifies count.
- [ ] **C5**. `cortex journal rebuild` (capture only) — truncate events + FTS5, replay, refresh via `rebuildEventIndexes`. Verify count.
- [ ] **C6**. Remove `internal/queue/` package and `.cortex/queue/` directory. Update all callers and tests.

### O. Observation
- [ ] **O1**. Observation entry types + schema — `observation.{claude_transcript,git_commit,memory_file}`. Payload: source URI + content-hash + size + ts. No substrate copy.
- [ ] **O2**. Dream sources (`claude_history.go`, etc.) emit observation entries on read. Idempotent via content-hash.
- [ ] **O3**. Observation projection — SQLite traceability + dedup table.

### D. Dream — entry-emission + projection (back-to-back)
- [ ] **D1**. Dream emits `dream.insight` entries with `sources` offsets. Stops writing SQLite directly.
- [ ] **D2**. Dream projection materializes insights to existing SQLite tables. Newer entries supersede older via `sources` chain; SQLite holds the latest pointer, journal preserves history.

### R. Reflect — entry-emission + projection (back-to-back)
- [ ] **R1**. Reflect emits `reflect.rerank` entries — rerank decision + contradictions detected + source offsets.
- [ ] **R2**. Reflect projection — rerank cache + contradictions table.

### Z. Resolve — entry-emission + projection (back-to-back)
- [ ] **Z1**. Resolve emits `resolve.retrieval` entries — query, results returned, injected vs skipped, source offsets. Migrates existing `retrieval_stats.json` into journal.
- [ ] **Z2**. Resolve projection — stats tables. `retrieval_stats.json` deleted after verify.

### T. Think — entry-emission + projection (back-to-back)
- [ ] **T1**. Think emits `think.topic_weight` and `think.session_context` entries. Migrates `daemon_state.json` into journal.
- [ ] **T2**. Think projection — session context tables. `daemon_state.json` deleted after verify.

### B. feedBack
- [ ] **B1**. Feedback entry types — `feedback.{correction,confirmation,retraction}` with `graded_offset` field referencing the graded derivation.
- [ ] **B2**. Wire `/cortex-correct`, `/cortex-forget` (and the slash-command equivalents under `.claude/commands/`) to emit feedback entries.
- [ ] **B3**. Feedback projection — updates derivation rows (superseded / retracted / confirmed flags). Journal never edits; SQLite holds the latest pointer.

### E. Eval
- [ ] **E1**. Eval entries (`eval.cell_result`) written via journal. Migrate existing `cell_results.jsonl`.
- [ ] **E2**. Update `internal/eval/v2/persist*.go` to write to journal. Remove the dual-write path.

### X. Cross-class ops
- [ ] **X1**. `cortex journal rebuild` walks the full writer-class DAG (capture+observation → derivations → feedback) in topological order, refreshing all projections.
- [ ] **X2**. `cortex journal replay --config-overrides=...` re-runs cognitive modes against historical inputs with overridden model/prompt/budget. Emits to a side comparison table. **This is the counterfactual eval primitive.**
- [ ] **X3**. `cortex journal verify` — every `sources` offset resolves; entry counts match projection row counts; cursor consistent.

### I. Inspection & privacy
- [ ] **I1**. `cortex journal show <offset>` and `cortex journal tail` commands.
- [ ] **I2**. Segment gzip on close; reader handles `.jsonl` and `.jsonl.gz`.
- [ ] **I3**. Privacy guardrails — CLI refuses to upload journal contents from any command; `cortex init` warns if `.cortex/` is not in `.gitignore`.

### W. Writing & docs
- [ ] **W1**. `docs/journal.md` updated to reflect any deltas discovered during implementation.
- [ ] **W2**. `CLAUDE.md` updated — journal-as-source-of-truth section; deprecate references to `.cortex/queue/`, `retrieval_stats.json`, `daemon_state.json`.
- [ ] **W3**. `docs/journal-design-log.md` — narrative of decisions made during implementation, what changed from the plan, what was discovered.

**Total: 33 slices, 12 buckets.**

---

## Loop kickoff

Self-paced (slice durations vary widely; fixed interval wastes wake-ups):

```
/loop implement the full journal architecture in worktree per docs/journal-implementation-plan.md;
one slice per iteration in bucket order F→C→O→D→R→Z→T→B→E→X→I→W;
D/R/Z/T pairs land back-to-back; commit each green slice;
stop when all 33 checked and OBJECTIVE criteria met
```

### Retry semantics

If the loop fails partway:
1. `git log --oneline feat/journal` shows which slices landed.
2. `git reset --hard <commit-of-this-doc>` returns to the pre-implementation state.
3. Re-launch the loop with the same kickoff. The agent walks the checklist from the start, skipping slices already done (it can read its own commit log to know).

The two pre-committed docs (`journal.md`, `journal-implementation-plan.md`) are the stable reference — the slice plan does not change during implementation. Any deltas discovered get logged in `docs/journal-design-log.md` (slice W3) and the docs are updated at the end (slice W1).
