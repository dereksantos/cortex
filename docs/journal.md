# The Journal

**Draft v0.3** — *Cortex's commitment to event-sourcing*

> **Reconciliation note (2026-07-24)**: an audit found this doc had drifted
> substantially from the code — the eight-class taxonomy below had grown to
> thirteen defined classes (six live, seven dormant since the mechanical
> cognition pipeline was removed), two of the live classes turned out to
> write to the *machine*-level `~/.cortex/journal/` rather than the
> project-level tree the doc assumed, several linked planning docs had been
> deleted, and the gzip-on-close claim didn't match `Writer.Close()`'s
> actual behavior. This revision reconciles all of that against
> `internal/journal/*.go` and the call sites in `cmd/cortex`; see the
> Writer-class taxonomy section for the current census.

> **Historical note:** Daemon mentions below ("indexer runs in-daemon",
> "capture is daemon-independent", `.cortex/daemon_state.json`, etc.)
> predate the May 2026 daemon retirement (see `docs/archive.md`). Cortex is
> now the single long-lived REPL process described in `CLAUDE.md` — there is
> no daemon and no `cortex journal ingest`/`rebuild`/`verify`/`replay` CLI
> today (`cortex learn` and `study` read the journal directly instead, via
> `grep`/targeted reads rather than a rebuilt index). Capture stays
> "host-process-independent" — it appends to its own segment regardless of
> what else is running — and the design properties this doc argues for
> (single-writer per class, no broker, segment-per-writer-class) are
> unchanged; the specific rebuild/replay machinery described later in this
> doc is retained as design intent, not shipped surface.

> **Implementation status (2026-05-12)**: v0.1 of the journal architecture was implemented on branch `feat/journal`. Most notably, Cortex's "read side" is **not SQLite** but **JSONL-with-in-memory-indexes** in `internal/storage`. The CQRS commitment is unchanged; the engine on the read side just happens to be a different file format. (The slice-by-slice implementation plan and design log that were once linked here have since been deleted from the tree; the deltas they recorded are folded into this doc.)

## Thesis

The journal is not a queue redesign. It is the commitment to event-sourcing that the rest of Cortex's cognitive architecture already implicitly assumes. Without it, the claims about reconstructibility, time-travel, provenance, eval reproducibility, contradiction tracking, ABR, auto-tuning, and learning-from-corrections are aspirational. With it, they are mechanical.

Memory is the floor. Learning is what the floor enables. A search engine over a snapshot stores what the user said; a journal records what the system saw, what it thought, and what feedback it got — all on one ledger, all linked by offset. Cortex without the journal is a Claude Code add-on. Cortex with the journal is a learning harness.

## Why now

Today's `.cortex/queue/` directory contains 1,870 files in `processed/`, one tiny JSON file per event, plus a `pending/` and `processing/` directory expressing three states via filesystem renames. `CleanProcessed` deletes the historical record on demand. The design wanted to be a journal but kept getting treated as a queue: durable on the way in, disposable on the way out. The proper noun forces the design to honor what the data is.

Two existing JSONL artifacts — `.cortex/db/cell_results.jsonl` and the `retrieval_stats.json` / `daemon_state.json` write paths — are proto-journal entries for an as-yet-unbuilt journal. The journal model is the principled version of an instinct already validated in the codebase.

## The CQRS commitment

- **Journal = write side.** JSONL, append-only, durable, portable, human-readable. Source of truth.
- **Storage layer = read side.** Regeneratable, optimized for query. Never authoritative.

The read side is `internal/storage`: in-memory indexes hydrated from per-derivation JSONL projection files (events.jsonl, observations.jsonl, contradictions.jsonl, retrievals.jsonl, session_context.jsonl, feedback.jsonl, eval_cell_results.jsonl). It is not SQLite — that was an assumption in v0.1 of this doc that didn't survive implementation. The CQRS property holds in principle either way: a rebuild would drop every derived projection and reproduce it by replaying the journal — though as the Replay/rebuild section below spells out, that rebuild is a designed capability, not a shipped `cortex journal rebuild` command.

Why files (not a SQL engine) for the write side:
- **Append + fsync** is the OS's hot path. A SQL engine would do this internally with more layers of indirection.
- **Per-writer-class isolation** is just per-directory. Capture (per-turn, foreground) doesn't contend with a background loop run (batchy, off the hot path). A shared SQL engine would force single-writer or per-process connection overhead.
- **Inspectability** — `jq`, `grep`, `tail -f`, `wc -l` all work today. The user can audit Cortex's thinking with standard Unix tools without any dedicated journal CLI.
- **Standard ops apply** — segment rotation, archival, rsync to backup, all just work on files. Closing a segment (`Writer.Close()`) only fsyncs and releases the lock; gzip is a separate, explicit step (`journal.CompactSegment`, `internal/journal/compact.go`) that compresses one fully-closed, non-active segment to `.jsonl.gz` and removes the original — idempotent, and never run against the writer's current segment.

## The ten principles

1. **CQRS, explicit.** Journal = write side. Storage layer = read side (JSONL-with-in-memory-indexes, not SQLite). The read side is regeneratable; the journal is not.
2. **The journal contains inputs AND decisions AND corrections.** Raw events (`capture.event`) and derivations/grades that reference their sources by offset. Today the live derivation/grade classes are `study.result`, `eval.cell_result`, `model.failure`, `model.substitution`, and `loop.run`; the original `dream.*` / `reflect.*` / `resolve.*` / `think.*` / `feedback.*` classes this principle was written against are defined but dormant — see Writer-class taxonomy. Provenance is structural, not metadata.
3. **External substrates stay external.** Claude transcripts, user memory files, git, project docs would be observed and recorded as `observation.X` entries at content-hash + time, not copied wholesale — the `observation` writer-class (`internal/journal/observation.go`) is defined but has no live writer today. Producers retain ownership either way.
4. **fsync is per-writer-class.** Input boundary (`capture/`) fsyncs every entry — input loss is permanent. Every other live class today (`eval/`, `study/`, `model/`, `loop/`, `landscape/`) fsyncs per batch — derivation loss is recoverable by re-running whatever produced it.
5. **Retractions are append-only entries.** Historically, a `/cortex-forget` slash command wrote a `feedback.retraction` referencing the offset to forget — the append-only *pattern* is the durable idea, but that command and the `feedback` class it drove are both gone. Forgetting today goes through the `memory_forget` tool over the separate model-driven memory store (`docs/memory-tools.md`), not a journal projection.
6. **Local-only by default; jq-readable by default.** Privacy and trust are design invariants. JSONL, no encryption unless opt-in, no remote sync unless explicit.
7. **Indexer runs in-daemon AND as one-shot CLI.** *(Historical — written before the May 2026 daemon retirement; see the note above. There is no daemon and no `cortex journal ingest`/`rebuild` CLI today — `study` and `cortex learn` read the journal directly instead. The underlying point survives: capture never blocks on anything reading it downstream.)*
8. **Counterfactual replay is a first-class operation.** *(Design intent, not shipped.)* `replay.counterfactual` (`internal/journal/replay.go`) is a defined-but-dormant entry type with no live writer and no `cortex journal replay` CLI. The idea — re-run past inputs through a new prompt/model/budget and diff the derivations — is what would make auto-tuning evals mechanical instead of aspirational; it isn't wired up yet.
9. **Segment-per-writer-class.** `journal/capture/NNNN.jsonl`, `journal/eval/NNNN.jsonl`, etc. at the project level, plus `journal/loop/NNNN.jsonl` and `journal/landscape/NNNN.jsonl` at the machine level (see Directory layout below). Logical journal is the topologically-ordered stream. Single-writer-with-lock within each class. No broker, no daemon dependency for capture.
10. **Claude transcripts are upstream of last resort.** Cortex's journal is canonical-for-Cortex, not canonical-for-the-universe. Catastrophic loss of Cortex's journal could in principle be partially recovered by replaying Claude Code transcripts against Cortex's existing substrate — no such replay path is built today.

## Writer-class taxonomy

The logical journal is partitioned on disk by *who wrote it*. Each writer-class owns one directory and one set of entry types. The `Type*` constants live in `internal/journal/*.go`; a class counts as **live** here if some non-test code outside `internal/journal` actually constructs and appends that entry type today.

### Live classes

| Class | Root | Directory | Entry types | fsync | Written by | When |
|---|---|---|---|---|---|---|
| **capture** | project | `.cortex/journal/capture/` | `capture.event` | per entry | `internal/capture/capture.go:228` | every captured turn event (files edited, commands run, final answer) |
| **eval** | project | `.cortex/journal/eval/` | `eval.cell_result` | per batch | `cmd/cortex/session_runtime.go` (`emitSessionMetrics`) | end of each REPL session — per-session telemetry, not the old eval-grid framework that type once served |
| **study** | project | `.cortex/journal/study/` | `study.result` | per batch | `cmd/cortex/study_eval.go` (`emitStudyResult`) | each `cortex study-eval` rep |
| **model** | project | `.cortex/journal/model/` | `model.failure`, `model.substitution` | per batch | `cmd/cortex/heal.go` (`journalModelFailure`); `cmd/cortex/preflight.go` (`reportSubstitution`) | a role's model errors mid-session and self-heals; preflight substitutes an unserved curated model at startup |
| **loop** | **machine** (`~/.cortex/journal/`) | `journal/loop/` | `loop.run` | per batch | `cmd/cortex/loop_run.go`, `cmd/cortex/serve_scheduler.go` (`journal.AppendLoopRun`) | each background loop firing (e.g. `cortex learn`), across all projects on the machine |
| **landscape** | **machine** (`~/.cortex/journal/`) | `journal/landscape/` | `landscape.scan` | per batch | `cmd/cortex/scan.go`, `internal/tools/scan_landscape.go` (`journal.AppendLandscapeScan`) | `cortex scan` / the `scan_landscape` tool discovering projects under configured roots |

### Defined but dormant

These writer-classes' Go types and constructors still exist in `internal/journal/`, but nothing outside the package (and its tests) constructs one — they stopped firing when the mechanical retrieve/rerank/Dream cognition pipeline was deleted (see `docs/memory-tools.md`, `docs/archive.md`). Kept for history and as a landing spot if a future consumer needs the shape:

| Class | Entry types | Historical role |
|---|---|---|
| **observation** | `observation.claude_transcript`, `observation.git_commit`, `observation.memory_file`, `observation.project_file` | external-substrate references at content-hash + time |
| **dream** | `dream.insight`, `dream.session_digest` | background insight derivation |
| **reflect** | `reflect.rerank` | retrieval re-ranking |
| **resolve** | `resolve.retrieval` | retrieval resolution |
| **think** | `think.topic_weight`, `think.session_context`, `think.session_summary`, `think.accumulator_update`, `think.accumulator_compact` | salience/topic weighting |
| **feedback** | `feedback.correction`, `feedback.confirmation`, `feedback.retraction` | corrections against derivations, incl. the old `/cortex-forget` |
| **replay** | `replay.counterfactual` | counterfactual re-execution (see principle 8 above) |

That's 6 live classes + 7 dormant classes = 13 defined writer-classes today, against the 8 this section originally described. There is no "collective exhaustion" invariant to state anymore — the taxonomy is whatever `internal/journal` currently defines, and liveness is a fact about call sites, not a design constant. Mutual exclusivity still holds: each entry type belongs to exactly one writer-class.

## Directory layout

Not every class lives under the project. Most do — `.cortex/journal/<class>/` next to the rest of the project's `.cortex/` state, resolved from `ContextDir()`. Two classes are explicitly **machine-level**, resolved via `internal/userhome` (`~/.cortex/journal/<class>/`, or `$CORTEX_HOME/journal/<class>/` if set) instead of the project's `.cortex/`:

- **loop** (`internal/journal/loop.go:80`) — loop firings aren't scoped to one project; the scheduler and the web UI's run-history view want one machine-wide stream.
- **landscape** (`internal/journal/landscape.go:60`) — scan results describe the registry of projects on the machine, so they can't live inside any single project's tree.

Every other live class (`capture`, `eval`, `study`, `model`) is project-scoped, one `.cortex/journal/` tree per project, matching the rest of this document's per-project framing.

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

> **Status: design intent, not shipped.** There is no `cortex journal`
> subcommand today — no `rebuild`, `verify`, `replay`, `show`, or `tail`.
> `internal/journal.Indexer`/`Registry` (the projector machinery this
> section assumes) are only exercised by `internal/journal`'s own tests; no
> production call site builds one. The three modes below describe the
> design this package's types were built to support — kept because the
> types (`replay.counterfactual`, the registry's version-migration contract)
> are still the right shape if this gets wired up. Today's actual journal
> readers (`study`, `cortex learn`) work directly over the raw JSONL with
> `grep`/targeted reads, not through a rebuilt index.

Three modes, all driven by walking the journal in offset order:

**Rebuild** (`cortex journal rebuild`, unimplemented):
1. Truncate all derived state.
2. Walk the writer-class DAG in topological order: `capture` + `observation` → `dream`, `reflect`, `resolve`, `think` → `feedback`, `eval`.
3. Within each class, replay entries in offset order, dispatching each to its projector. `eval.cell_result` entries (`journal.EvalCellResultPayload`; see `internal/journal/eval.go`) are today written straight to the journal by their emitter and carry no projector — the `internal/eval/v2` grid framework that once projected them to SQLite + `cell_results.jsonl` was removed.
4. Refresh FTS5 / vec indexes via existing `rebuildEventIndexes`.

Used after corruption, schema change, or to reset for evals.

**Verify** (`cortex journal verify`, unimplemented):
1. Each `sources` offset resolves to a real entry.
2. Entry types whose payload declares the source's writer-class (today: `replay.counterfactual` via `SourceClass`) get a *strict* check — the offset must exist in that class. Other types resolve permissively and the verifier counts cross-class ambiguity so the operator sees the SNR.
3. Cursor offset is consistent with the last successfully projected entry.

Used after migration and as a periodic health check.

**Counterfactual replay** (`cortex journal replay --config-overrides=K=V[,K=V]... [--execute]`, unimplemented):
1. Walk historical entries in the chosen class (`--class=reflect` for the implemented path) from offset A to B.
2. Without `--execute`, emit a `replay.counterfactual` entry with `status="planned"` per source — a non-destructive scheduling primitive.
3. With `--execute`, re-invoke the matching cognitive mode against an LLM provider built from the overrides (model/provider/temperature/max_tokens) and emit `status="executed"` with the new ranking + Jaccard@K vs the original. Results land in `journal/replay/`, never overwriting the original derivations.
4. Allowed overrides are explicit-allow-listed at parse time; shell-meta and control characters in values are rejected.

This is the eval primitive that makes auto-tuning mechanical: you can ask "would the new prompt have made better decisions on last week's traffic?" and get a numeric answer without re-running the user's work. Dream/Resolve/Think classes currently emit only planned-status entries; their cognition re-invocation is a follow-up that does not require journal-layer changes.

## Operational invariants

- **`.cortex/` is gitignored.** Always. `cortex init` warns if not.
- **No remote upload by default.** CLI refuses to send journal contents anywhere. Opt-in flag required, explicit per command.
- **JSONL stays grep/jq-readable.** No binary framing, no encryption-by-default. The user can read what Cortex is recording.
- **Capture is host-process-independent.** The capture path appends to its segment regardless of what else is running — there is no daemon for it to depend on today (see the historical note above).

## What this enables

The journal is the substrate for self-improvement. Without it, Cortex can be tuned by hand and tested forward — A/B in production, slow and noisy. With it, Cortex can be tuned by replay against its own history — counterfactual evaluation against a recorded ground truth. That's the difference between a tool and a learning system.

Specifically, each of these capabilities — claimed elsewhere in the architecture — depends mechanically on the journal:

- **Reconstructibility** of derived state from raw inputs.
- **Time-travel** to a past state for debugging or eval reproducibility.
- **Provenance** for every derived row back to its source events.
- **Contradiction tracking** with a permanent record of how each contradiction was resolved.
- **Auto-tuning evals** via counterfactual replay.
- **Learning from corrections** by linking grading entries to the derivations they grade, producing a labeled training signal local to the project.

These are not features to be added later — they are properties that emerge when the substrate is correct. Several of them (reconstructibility via rebuild, auto-tuning via counterfactual replay) still need the rebuild/replay machinery described above to actually be wired up; today they're true of the entry format and offset/`sources` structure, not yet of a shipped CLI.

## Implementation

The original slice-by-slice implementation plan and design log this section
pointed to have been deleted from the tree (the work they tracked landed;
see the reconciliation note at the top of this doc and `git log` for the
history). What's current: the writer/reader mechanics in
`internal/journal/*.go`, the live writer call sites listed under
Writer-class taxonomy above, and `docs/memory-tools.md` for the
model-driven system (memory tools + `study`) that superseded the mechanical
cognition pipeline this journal originally fed.

## References

- [`CLAUDE.md`](../CLAUDE.md) — the current architecture this journal supports.
- [`docs/memory-tools.md`](./memory-tools.md) — the model-driven memory/study system live today; supersedes the mechanical dream/reflect/resolve/think pipeline this journal was originally built to feed.
- [`docs/archive.md`](./archive.md) — why the mechanical cognition pipeline (and the writer-classes it drove) was removed, and what `cmd/cortex` replaced it with.
- [`docs/learning-loop.md`](./learning-loop.md) — `cortex learn` and the loop scheduler, the main consumer of `loop.run` today.
