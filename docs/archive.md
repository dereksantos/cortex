# Archive — what Cortex was, before centering on `cmd/cortex`

> This document preserves the architecture and intent of the system as it
> existed up to June 2026, before the project was deliberately slimmed to
> center on the `cmd/cortex` coding harness. The code described here was
> removed in the `cleanup: center the project on cmd/cortex` commit and lives
> on in git history (branch `main` at and before that commit). Nothing here
> is current guidance — it is a record so the reasoning isn't lost.
>
> If you're looking for how the project works *today*, read
> [`../README.md`](../README.md) and [`../CLAUDE.md`](../CLAUDE.md).

## Why the slim-down

The project had grown two complete harness implementations and a large
research apparatus around them:

- **`cmd/cortex`** — the original harness + the `cortex` CLI (install,
  hooks, slash commands, search/insights/status, journal CLI, eval CLI).
- **`cmd/cortex`** — a newer, focused interactive coding agent.

Plus a ~27k-LOC eval framework, benchmark wrappers, a duplicate LLM layer,
and ~40 design docs. Roughly 47% of the Go code did not support `cmd/cortex`.
The decision was to keep `cmd/cortex` as the single harness and remove
everything that only existed to serve `cmd/cortex` or the eval framework —
recoverable from history if any of it is needed again.

**Removed:** `cmd/cortex` (+commands), `internal/eval/*` (framework +
SWE-bench / MTEB / NIAH / LongMemEval wrappers + v2/journey/codebase/
dagtrace/paired/mechanic runners), `internal/harness`, `internal/processor`,
`internal/repltui`, `internal/cognition/sources`, `internal/llm` (duplicate
of `pkg/llm`), `internal/web`, `pkg/models`, `pkg/registry`, `pkg/system`,
the `test/` eval/e2e scaffolding (~1,900 files), the Homebrew formula, the
Claude-Code plugin manifest, and the cortex-only scripts/CI workflows.

**Kept** (because `cmd/cortex` uses them): `internal/{capture, journal,
storage, study, measure, projectindex, projectscan, shellrisk, lineedit,
cognition, cognition/fractal}` and `pkg/{cliout, cognition, cognition/dag,
cognition/dag/ops, cognition/prompts, config, events, llm, secret}`.

---

## The original thesis (three claims)

Cortex was framed as "a general-purpose coding harness that leverages
multiple models, learns over time, and has bounded emergence." The eval
strategy was built around three claims (detailed in the since-removed
`eval-strategy.md` — see git history):

1. **Multi-model leverage.** Small model + Cortex matches or exceeds a
   bigger model alone, at lower cost. Metric: quality normalized by model
   size or dollars, never absolute pass-rate.
2. **Learning over time.** Same harness + model + sequential workload →
   quality on later sessions exceeds earlier ones. Metric: learning-curve
   slope.
3. **Bounded emergence.** DAG seed+grow+decay produces task-appropriate
   complexity — cheap tasks → small graphs, complex tasks → larger graphs,
   quality flattening at a knee. Metric: the budget–quality curve.

The working-memory direction (retained in `working-memory.md`,
`working-memory-study.md`) is the live continuation of claims 1–2 inside
`cmd/cortex`.

## The capture pipeline

```
Capture → Filter → Store → Retrieve → Inject
   │         │        │         │         │
  <20ms    Signal   SQLite   Embeddings  Format
  hooks    vs noise  + vec    + rerank   for LLM
```

- **Capture**: record events (<20ms target), append to the journal.
- **Filter**: extract durable context — decisions, corrections, patterns.
- **Store**: immutable event log + embeddings for semantic search.
- **Retrieve**: fast mechanical lookup (embeddings) + optional LLM rerank.
- **Inject**: format context for the active model.

`cmd/cortex` retains a focused form of this: per-turn structural capture +
async distillation into the journal, and turn-start retrieval injected via
an ephemeral system block (see CLAUDE.md).

## The five cognitive modes

The historical cognitive architecture, inspired by human information
processing. The current direction views these as compositions of DAG nodes
(the DAG-protocol design docs are in git history), but the mode framing
drove most of the original code.

| Mode | Type | Speed | When |
|------|------|-------|------|
| **Reflex** | Mechanical | <20ms target | Every retrieval — embeddings, tags, recency |
| **Reflect** | Agentic | 200ms+ | Sync at session start, async mid-session — LLM rerank, contradiction detection |
| **Resolve** | Agentic | 50–100ms | After results — inject now / wait / queue |
| **Think** | Background | Bounded by spare cycles | Active periods — budget *decays* with activity |
| **Dream** | Background | Bounded by MaxBudget | Idle periods — budget *grows* with idle time |

Two architectural commitments distinguished it from sleep-time / CoALA /
A-Mem prior art:

- **Inverse activity gradient**: Think runs at reduced budget while you're
  busy; Dream grows with idle time, capped. Both bounded by design.
- **Mechanical foreground with a latency target**: Reflex keeps the
  critical path fast (no LLM call); agentic modes run off-path and feed
  Reflex via cached artifacts (`CachedReflect`, `TopicWeights`,
  `ProactiveQueue`).

**Dream sources** (the removed `internal/cognition/sources`) sampled
project files, stored events, Claude history, and git commits/diffs to
produce new embeddings, insights, entity relationships, and a proactive
injection queue.

## Journal — CQRS event-sourcing (retained, still source of truth)

Append-only JSONL per writer-class is canonical; the storage layer
(in-memory indexes + projection JSONL) is regeneratable from the journal.
See `journal.md` (retained). Eight writer-classes existed; `cmd/cortex`
exercises `capture`, `observation`, and the feedback/think paths.

| Class | Entry types | fsync |
|---|---|---|
| capture | `capture.event` | per entry |
| observation | `observation.{claude_transcript,git_commit,memory_file}` | per batch |
| dream | `dream.insight` | per batch |
| reflect | `reflect.rerank` | per batch |
| resolve | `resolve.retrieval` | per batch |
| think | `think.{topic_weight,session_context}` | per batch |
| feedback | `feedback.{correction,confirmation,retraction}` | per entry |
| eval | `eval.cell_result` | per batch |

Invariants (still enforced for the retained journal code): local-only by
default (`journal.AssertLocalOnly`), `.cortex/` in `.gitignore`,
jq-readable plain JSONL, closed segments gzippable.

## The `cortex` CLI surface (removed)

The removed `cmd/cortex` binary exposed:

- **Lifecycle**: `setup`, `init`, `install`, `uninstall`, `status`
- **Coding harness**: `repl`, `code "..."`, `run --type={turn|eval|think|dream|capture}`
- **Search/query**: `search`, `search --type={recent|insights|entities|graph}`
- **Memory ops**: `capture`, `forget`, `journal {ingest|rebuild|replay|verify|show|tail|migrate}`
- **Evals**: `eval`, `measure`, `calibrate`
- **Claude Code host integration**: `SessionStart` / `UserPromptSubmit` /
  `PostToolUse` hooks + `/cortex*` slash commands + a status line, declared
  in `.claude-plugin/plugin.json`.

`cmd/cortex` replaces the coding-harness verbs with its own (see README);
the capture/journal/search *libraries* survive, but the standalone CLI
over them and the Claude-Code host wiring were removed.

## The eval framework (removed)

`internal/eval/*` + `test/evals/` implemented a three-tier strategy
(documented in the since-removed `eval-strategy.md`; see git history):

| Tier | Job | Metric of record |
|---|---|---|
| 1. Baseline | Competence on standard benchmarks | Pass-rate normalized by model size / dollars / wall-clock |
| 2. Thesis | The three claims | Curves and deltas, not single numbers |
| 3. Regression | Catch silent UX degradation | Cheap pass/fail thresholds |

It wrapped SWE-bench, LongMemEval, NIAH, and MTEB; ran a 40-scenario v2
coding suite plus a library-service multi-session corpus; and recorded
results to SQLite + `cell_results.jsonl`. The **Agentic Benefit Ratio**
(ABR = quality(Fast+Think) / quality(Full)) was an early headline metric,
later retired in favor of the budget–quality curve; the pre-DAG baseline
of ABR 0.586 was recorded in `eval-journal.md` (now in git history).

`cmd/cortex` keeps a small, focused replacement: `cortex study-eval` measures
the study tool's latency / coverage / groundedness over a fixture set.

## DAG protocol + cognition stack (removed 2026-06-27)

The seed+grow+decay execution model — a DAG grows from a tiny seed under a
decaying budget, with no upfront emission and no separate planner; spawning,
a depth cap, and budget exhaustion are the bounds. Most nodes are narrow
small-LLM micro-calls; planning emerges from composition. The design docs
(`dag-protocol.md`, `dag-build-plan.md`, `bootstrap-dag-plan.md`) are in
git history.

After the memory-as-tools pivot left the DAG with a single live consumer
(Discord's "continue this change vs. start a new one?" classifier), the whole
stack was deleted: `pkg/cognition/dag` (engine + op registry), the
`internal/cognition` retrieve/Dream/Think/Reflect subsystem built on it, and
`internal/study` (the blind-sampling study engine the DAG's structural ops
imported). `internal/cognition` could not be left dormant — it was built on the
DAG types and stopped compiling without them. The route-message classifier was
reimplemented as a ~70-line standalone LLM call in `cmd/cortex/discord.go`
(`classifyRoute`/`parseRouteDecision`), preserving the bias-to-continue
fail-safe. Net: −18,350 src / −11,029 test LOC. The capture/storage write-side
(`internal/storage`, `capture.NewWithStorage`) is kept dormant as the substrate
a future semantic `Reflect` profile would project into (see
`docs/study-subagent.md`).

## Roadmap planning doc (archived 2026-07-10)

`docs/roadmap-2026-06-23.md` was the first tidy pass over four roadmap items
(web search, tidy-first extraction, working memory, edit-diff UX), grounded in
the codebase as it stood on 2026-06-23 — a 4,371-line `cmd/cortex/main.go`
monolith with a parallel `internal/harness`. Everything it planned either
shipped in a different shape or was deliberately deferred: the extraction was
superseded by `engine-unification.md` + `study-subagent.md` (shipped); the
working-memory item landed as the two-zone context architecture
(`context-architecture.md`); web search and the small-batches system prompt
landed as specified; the edit-diff UX remains deferred. The live roadmap is
the root [`ROADMAP.md`](../ROADMAP.md); the doc is in git history.

## Distribution (removed)

- **Homebrew** (`Formula/cortex.rb`) — `--HEAD` installs of the `cortex`
  binary; referenced the (already-retired) `cortex daemon`.
- **Claude-Code plugin** (`.claude-plugin/plugin.json`) — hooks + slash
  commands + status line shelling out to `cortex`.
- **CI**: `release.yml` (cross-platform `cortex` binaries) and `eval.yml`
  (eval runs). `test.yml` was repointed to build `cmd/cortex`.

No Cortex distribution pipeline exists yet; rebuild one when Cortex is
ready to ship.
