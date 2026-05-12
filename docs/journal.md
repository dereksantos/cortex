# The Journal

**Draft v0.2** — *Cortex's commitment to event-sourcing*

> **Implementation status (2026-05-12)**: v0.1 of the journal architecture is implemented on branch `feat/journal`. All 33 slices in [`journal-implementation-plan.md`](./journal-implementation-plan.md) landed across 12 buckets. See [`journal-design-log.md`](./journal-design-log.md) for the deltas discovered during implementation — most notably, Cortex's "read side" is **not SQLite** but **JSONL-with-in-memory-indexes** in `internal/storage`. The CQRS commitment is unchanged; the engine on the read side just happens to be a different file format.

## Thesis

The journal is not a queue redesign. It is the commitment to event-sourcing that the rest of Cortex's cognitive architecture already implicitly assumes. Without it, the claims about reconstructibility, time-travel, provenance, eval reproducibility, contradiction tracking, ABR, auto-tuning, and learning-from-corrections are aspirational. With it, they are mechanical.

Memory is the floor. Learning is what the floor enables. A search engine over a snapshot stores what the user said; a journal records what the system saw, what it thought, and what feedback it got — all on one ledger, all linked by offset. Cortex without the journal is a Claude Code add-on. Cortex with the journal is a learning harness.

## Why now

Today's `.cortex/queue/` directory contains 1,870 files in `processed/`, one tiny JSON file per event, plus a `pending/` and `processing/` directory expressing three states via filesystem renames. `CleanProcessed` deletes the historical record on demand. The design wanted to be a journal but kept getting treated as a queue: durable on the way in, disposable on the way out. The proper noun forces the design to honor what the data is.

Two existing JSONL artifacts — `.cortex/db/cell_results.jsonl` and the `retrieval_stats.json` / `daemon_state.json` write paths — are proto-journal entries for an as-yet-unbuilt journal. The journal model is the principled version of an instinct already validated in the codebase.

## The CQRS commitment

- **Journal = write side.** JSONL, append-only, durable, portable, human-readable. Source of truth.
- **Storage layer = read side.** Regeneratable, optimized for query. Never authoritative.

The read side is `internal/storage`: in-memory indexes hydrated from per-derivation JSONL projection files (events.jsonl, observations.jsonl, contradictions.jsonl, retrievals.jsonl, session_context.jsonl, feedback.jsonl, eval_cell_results.jsonl). It is not SQLite — that was an assumption in v0.1 of this doc that didn't survive implementation. The CQRS property holds either way: `cortex journal rebuild` drops every derived projection and reproduces it by replaying the journal.

Why files (not a SQL engine) for the write side:
- **Append + fsync** is the OS's hot path. A SQL engine would do this internally with more layers of indirection.
- **Per-writer-class isolation** is just per-directory. Capture (per-hook, separate process) doesn't contend with Dream (daemon, batchy). A shared SQL engine would force single-writer or per-process connection overhead.
- **Inspectability** — `jq`, `grep`, `tail -f`, `wc -l` all work. The user can audit Cortex's thinking with standard Unix tools. `cortex journal show <offset>` and `cortex journal tail` are convenience layers on top of this.
- **Standard ops apply** — segment rotation, gzip on closure, archival, rsync to backup, all just work on files.

## The ten principles

1. **CQRS, explicit.** Journal = write side. Storage layer = read side (JSONL-with-in-memory-indexes, not SQLite). The read side is regeneratable; the journal is not.
2. **The journal contains inputs AND decisions AND corrections.** Raw events (`capture.event`), derivations (`dream.insight`, `reflect.rerank`, `resolve.retrieval`, `think.topic_weight`), and grading (`feedback.correction`, `feedback.confirmation`, `feedback.retraction`, `eval.cell_result`). Each derivation/grade entry references its sources by offset. Provenance is structural, not metadata.
3. **External substrates stay external.** Claude transcripts, user memory files, git, project docs — observed and recorded as `observation.X` entries at content-hash + time, not copied wholesale. Producers retain ownership.
4. **fsync is per-writer-class.** Input boundary (`capture/`) fsyncs every entry — input loss is permanent. Cognitive modes (`dream/`, `reflect/`, `resolve/`, `think/`) fsync per batch — derivation loss is recoverable via replay.
5. **Retractions are append-only entries.** `/cortex-forget` writes a `feedback.retraction` referencing the offset to forget. Journal never edits; indexer respects retractions when projecting to SQLite. Audit trail intact.
6. **Local-only by default; jq-readable by default.** Privacy and trust are design invariants. JSONL, no encryption unless opt-in, no remote sync unless explicit.
7. **Indexer runs in-daemon AND as one-shot CLI.** Capture is daemon-independent. The daemon is a convenience for low-latency indexing; CLI commands can `cortex ingest` before reading. Daemon-down ≠ system-down.
8. **Counterfactual replay is a first-class operation.** `cortex journal replay --config-overrides=...` re-runs past inputs through a new prompt/model/budget and emits comparable derivations to a side table. This is what makes auto-tuning evals mechanical instead of aspirational.
9. **Segment-per-writer-class.** `journal/capture/NNNN.jsonl`, `journal/dream/NNNN.jsonl`, etc. Logical journal is the topologically-ordered stream. Single-writer-with-lock within each class. No broker, no daemon dependency for capture.
10. **Claude transcripts are upstream of last resort.** Cortex's journal is canonical-for-Cortex, not canonical-for-the-universe. Catastrophic loss of Cortex's journal can be partially recovered by Dream replaying claude_history against its existing substrate.

## Writer-class taxonomy

The logical journal is partitioned on disk by *who wrote it*. Each writer-class owns one directory and one set of entry types.

| Class | Directory | Entry types | fsync mode | Subsumes |
|---|---|---|---|---|
| **capture** | `journal/capture/` | `capture.event` | per entry | `.cortex/queue/{pending,processing,processed}/` |
| **observation** | `journal/observation/` | `observation.claude_transcript`, `observation.git_commit`, `observation.memory_file` | per batch | (new) |
| **dream** | `journal/dream/` | `dream.insight` | per batch | direct SQLite writes from Dream |
| **reflect** | `journal/reflect/` | `reflect.rerank` | per batch | direct SQLite writes from Reflect |
| **resolve** | `journal/resolve/` | `resolve.retrieval` | per batch | `.cortex/retrieval_stats.json` |
| **think** | `journal/think/` | `think.topic_weight`, `think.session_context` | per batch | `.cortex/daemon_state.json` |
| **feedback** | `journal/feedback/` | `feedback.correction`, `feedback.confirmation`, `feedback.retraction` | per entry | (new) |
| **eval** | `journal/eval/` | `eval.cell_result` | per batch | `.cortex/db/cell_results.jsonl` |

Mutual exclusivity: each entry type appears in exactly one writer-class. Collective exhaustion: every input, decision, and grade that flows through Cortex maps to exactly one of these eight classes.

## Entry schema discipline

Every entry is one line of JSONL with this envelope:

```json
{"type": "<class>.<kind>", "v": 1, "offset": <int>, "ts": "<rfc3339>", "sources": [<offset>...], "payload": {...}}
```

- **`type`** — `<writer-class>.<entry-kind>`. Dispatch key for the projection registry.
- **`v`** — schema version. Forward-compat: unknown versions log and skip; the indexer migration table handles known versions.
- **`offset`** — monotonic within a segment (segment-number + line-number); globally unique within a writer-class.
- **`ts`** — RFC3339. Ordering within a class is by offset; cross-class ordering uses `ts` with tolerance for near-equality.
- **`sources`** — list of upstream offsets this entry derives from. Empty for `capture.event` and `observation.*`. Required for derivations and feedback.
- **`payload`** — class-specific content. Schema documented per entry-kind.

Segments rotate at 10MB or 10,000 entries, whichever comes first. Naming: `0001.jsonl, 0002.jsonl, ...`, zero-padded to 4 digits, widening cleanly as needed.

## Replay, rebuild, counterfactual replay

Three modes, all driven by walking the journal in offset order:

**Rebuild** (`cortex journal rebuild`):
1. Truncate all derived tables in SQLite.
2. Walk the writer-class DAG in topological order: `capture` + `observation` → `dream`, `reflect`, `resolve`, `think` → `feedback`.
3. Within each class, replay entries in offset order, dispatching each to its projector.
4. Refresh FTS5 / vec indexes via existing `rebuildEventIndexes`.

Used after corruption, schema change, or to reset for evals.

**Verify** (`cortex journal verify`):
1. Each `sources` offset resolves to a real entry.
2. Projection row counts match journal entry counts (modulo retractions).
3. Cursor offset is consistent with the last successfully projected entry.

Used after migration and as a periodic health check.

**Counterfactual replay** (`cortex journal replay --config-overrides=...`):
1. Walk historical capture entries from offset A to B.
2. Re-run cognitive modes (Dream, Reflect, etc.) with overridden config — different model, different prompt, different budget.
3. Emit results to a comparison journal or side table, never overwriting the original derivations.
4. Diff against original; report which decisions changed.

This is the eval primitive that makes auto-tuning mechanical: you can ask "would the new prompt have made better decisions on last week's traffic?" and get a numeric answer without re-running the user's work.

## Operational invariants

- **`.cortex/` is gitignored.** Always. `cortex init` warns if not.
- **No remote upload by default.** CLI refuses to send journal contents anywhere. Opt-in flag required, explicit per command.
- **JSONL stays grep/jq-readable.** No binary framing, no encryption-by-default. The user can read what Cortex is recording.
- **Capture is daemon-independent.** The capture path appends to its segment regardless of daemon state. The indexer catches up when the daemon (or `cortex ingest`) runs.

## What this enables

The journal is the substrate for self-improvement. Without it, Cortex can be tuned by hand and tested forward — A/B in production, slow and noisy. With it, Cortex can be tuned by replay against its own history — counterfactual evaluation against a recorded ground truth. That's the difference between a tool and a learning system.

Specifically, each of these capabilities — claimed elsewhere in the architecture — depends mechanically on the journal:

- **Reconstructibility** of SQLite from raw inputs.
- **Time-travel** to a past state for debugging or eval reproducibility.
- **Provenance** for every derived row back to its source events.
- **Contradiction tracking** with a permanent record of how each contradiction was resolved.
- **ABR measurement** by comparing Fast and Full mode against the same historical state.
- **Auto-tuning evals** via counterfactual replay.
- **Learning from corrections** by linking `feedback.*` entries to the derivations they grade, producing a labeled training signal local to the project.

These are not features to be added later. They are properties that emerge when the substrate is correct.

## Implementation

See [`journal-implementation-plan.md`](./journal-implementation-plan.md) for the MECE slice plan and the I/O/C/M/C prompt for the implementation loop.

## References

- `CLAUDE.md` — the cognitive architecture this journal supports.
- `docs/learning-harness.md` — the framing this journal makes mechanical.
- `docs/wisdom-extraction.md` — what derivations produce, which the journal now records.
- `docs/control-plane.md` — the scheduler model the journal feeds.
- `docs/emergence-evals.md` — the eval methodology the journal makes reproducible.
