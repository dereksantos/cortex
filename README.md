# Cortex

[![CI](https://github.com/dereksantos/cortex/actions/workflows/test.yml/badge.svg)](https://github.com/dereksantos/cortex/actions/workflows/test.yml)
[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**A coding harness with a continuous learning loop.** Cortex owns the
coding loop (`cortex code` / `cortex repl` / `cortex run`) and runs
*Think* and *Dream* in the background between turns. Small local models
do the continuous integration work; a frontier model can be invoked at
query time and receive pre-computed context.

> **Status: Experimental.** The core capture → store → retrieve → inject
> pipeline works and is in daily use on the author's machine, but Cortex
> is a research-grade tool, not a polished product. Pre-DAG-protocol
> baselines record ABR 0.586 (recorded in `docs/eval-journal.md`); the
> DAG ops landing under audit slice K + the eval migration under E are
> what move that upward. Small Ollama models (≤3B params) have measured
> below the floor for insight extraction — `llama3.1:8b` or larger, or
> `ANTHROPIC_API_KEY`, is recommended. Expect rough edges, breaking
> changes, and bugs that may require reading code to diagnose.

## What Cortex Does

Cortex performs **continuous context integration** as part of its own
coding loop — observing events as they happen, reconciling new
information against existing knowledge, and surfacing relevant context
on the next turn. It runs as a single binary; the REPL hosts the
long-lived process and drives the background cognition (Think, Dream)
on its idle hook. Decisions, patterns, and corrections are stored
through fast mechanical retrieval (Reflex) backed by an asynchronous
agentic loop (Reflect, Resolve, Think, Dream).

The role sits within the framework of cognitive architectures for
language agents (CoALA) and is closely related to sleep-time /
background-agent patterns (Letta) and memory-evolution approaches
(A-Mem). Two architectural commitments shape Cortex's particular
implementation:

**Inverse activity gradient on background processing.** Think runs
while you're actively working at *reduced* budget, doing only what
spare cycles allow. Dream runs while you're idle and *grows* with idle
time, capped at a configurable maximum. This refines the binary
active/idle split common in sleep-time approaches with a smooth
response to host activity. Both modes are bounded by design.

**Mechanical foreground with a latency target.** Reflex aims to keep
retrieval on the critical path fast — <20ms is the design target, with
no LLM call on the foreground path. Agentic processing (Reflect,
Resolve, Think, Dream) runs off-path and feeds Reflex via cached
artifacts. This is an active engineering target, not a number that's
been pinned across the eval corpus.

Full positioning — lineage, concurrent work, falsification — is in
[docs/learning-harness.md](docs/learning-harness.md).

## Problem

Coding harnesses waste tokens. Every session re-discovers past
decisions, re-reads files already understood, and re-establishes
context that existed yesterday. This token waste compounds: longer
projects mean more redundant context, higher costs, slower responses.

- **Re-discovered decisions**: "We use JWT" gets explained turn after turn
- **Redundant file reads**: The same architecture files get read and re-read
- **Repeated context**: Corrections, patterns, constraints re-stated manually
- **No measurement**: No way to know if injected context actually reduces downstream token use

Cortex addresses this with a shared context pipeline that reduces token
costs over time through semantic retrieval, budget-bounded cognitive
modes, and the ABR metric for measurable context quality.

## Solution: A Context Pipeline That Reduces Token Costs Over Time

```
Capture → Filter → Store → Retrieve → Inject
   │         │        │         │         │
  <20ms    Signal   SQLite   Embeddings  Format
  hooks    vs noise  + vec    + rerank   for LLM
```

**Capture**: Record events to the append-only journal (capture writer-class), fsync per entry.

**Filter**: Extract durable context—decisions, corrections, patterns. Ignore noise.

**Store**: Immutable event log + embeddings for semantic search.

**Retrieve**: Fast mechanical lookup (embeddings) + optional LLM reranking.

**Inject**: Format context for consumption by the current turn.

## Quick Start

**Prerequisites:**
- Go 1.25+
- Either Ollama at `http://localhost:11434` with `llama3.1:8b` (or larger) and `nomic-embed-text` pulled, **or** `ANTHROPIC_API_KEY` exported. Capture and search work without an LLM; Reflect/Dream and insight extraction need one. Models smaller than ~3B have measured below the task floor — see [docs/eval.md](docs/eval.md).

```bash
go build -o bin/cortex ./cmd/cortex
./bin/cortex init                # one-time per project: creates .cortex/, registers project
./bin/cortex repl                # interactive coding loop; hosts the background ingest + Think/Dream
./bin/cortex code "..."          # one-shot coding turn rooted in the cwd
```

`cortex install` exists as a compatibility verb (it now just ensures
`.cortex/` is initialized and reports local LLM availability — no
external editor / hook wiring is done).

## Multi-Agent / CI Setup

Cortex's capture and search paths work as standalone CLI tools. Any
external process can drive them, which is enough to give multiple
agents a shared context pool over the same `.cortex/` directory.

```bash
go build -o bin/cortex ./cmd/cortex
./bin/cortex init                                                 # one-time per project
./bin/cortex capture --type=decision --content="Use PostgreSQL"   # record an event
./bin/cortex ingest                                               # drain journal → DB
./bin/cortex search "database"
```

The journal (`.cortex/journal/<class>/`) uses per-segment flock for
cross-process capture safety; storage hydrates from JSONL projection
files. Concurrent writers may hit brief locks under heavy parallel
writes. `cortex init` does not require any external editor / IDE
installation.

## Cognitive Architecture

Cortex uses five cognitive modes, inspired by how humans process information:

| Mode | Type | Speed | Purpose |
|------|------|-------|---------|
| Reflex | Mechanical | <20ms target | "What feels related?" - embeddings, tags, recency |
| Reflect | Agentic | 200ms+ | "Is this actually relevant?" - LLM reranking |
| Resolve | Agentic | 50-100ms | "Should I act now or wait?" - injection decisions |
| Think | Background | Bounded | Active-period learning using spare cycles |
| Dream | Background | Bounded | Idle-period exploration and discovery |

### Retrieval Modes

```
Fast (mid-session):     Reflex → Resolve → Inject
                                   ↑
                         (cached Reflect results)

Full (session start):   Reflex → Reflect → Resolve → Inject
```

**Fast mode**: Minimizes latency during active work. Reflect runs async and caches results.

**Full mode**: Used at session start when accuracy matters more than speed.

### Background Processing

Think and Dream use activity-based budgets:

| Mode | Activity Level | Budget |
|------|----------------|--------|
| Think | High (busy) | Low (spare cycles only) |
| Think | Low (winding down) | Higher |
| Dream | High (busy) | Skip entirely |
| Dream | Low (idle) | High (capped) |

## CLI Commands

### Lifecycle

```bash
cortex init              # Initialize .cortex/ in current project
cortex install           # Ensure .cortex/ exists + report LLM availability
cortex uninstall         # Remove .cortex/ data (--purge required to delete)
cortex status            # One-line status; --system | --memory | --json | --expand
```

### Coding harness

```bash
cortex repl              # Interactive coding loop
cortex code "..."        # One-shot coding turn
cortex run --type=turn   # Run a DAG by type (turn | eval | think | dream | capture)
```

### Search & query

```bash
cortex search "query"                 # Cognitive retrieval over captured context
cortex search --type=recent           # Recent events (replaces the old `recent` command)
cortex search --type=insights [cat]   # Show insights (was: `insights`)
cortex search --type=entities [type]  # Knowledge-graph entities (was: `entities`)
cortex search --type=graph <type> <n> # Entity relationships (was: `graph`)
```

### Memory ops

```bash
cortex capture --type=decision --content="..."   # Record an event from the CLI
cortex forget <id>                               # Mark context as outdated
cortex journal {ingest|rebuild|replay|verify|show|tail|migrate}
```

### Evals

```bash
cortex eval              # Run a scenario or suite
cortex measure           # Measure prompt quality for small context windows
cortex calibrate         # Recompute per-op p50 cost hints from dag_traces.jsonl
```

## Project Structure

```
cortex/
├── cmd/cortex/          # CLI entry point
├── internal/            # Private implementation
│   ├── capture/         # Fast event capture (<20ms target)
│   ├── cognition/       # Five cognitive modes
│   ├── storage/         # SQLite + search
│   ├── processor/       # Async event processing
│   ├── journal/         # Append-only event log (source of truth)
│   ├── eval/v2/         # v2 eval runner + unified cell_results.jsonl sink
│   └── eval/benchmarks/ # Wrapped benchmarks (SWE-bench, NIAH, LongMemEval, MTEB)
└── pkg/                 # Public API
    ├── cognition/dag/   # DAG engine + op registry
    ├── cognition/       # Mode interfaces
    ├── config/          # Configuration
    ├── events/          # Event types
    └── llm/             # LLM providers (Anthropic, Ollama, OpenRouter)
```

## Configuration

Cortex stores data in `~/.cortex/` (global, project registry) and
`.cortex/` (per-project, captured events + embeddings + queue).

LLM providers — selection is via the `ANTHROPIC_API_KEY` env var (set → Anthropic; unset → Ollama):
- **Ollama**: local inference at `http://localhost:11434`. Free. Recommended models: `llama3.1:8b` for analysis and `nomic-embed-text` for embeddings. Smaller models have measured below the task floor; the configured `ollama_model` in `.cortex/config.json` must actually be pulled or the REPL's background cognition will silently produce zero insights.
- **Anthropic**: set `ANTHROPIC_API_KEY`. Uses `claude-haiku-4-5` for analysis. Embeddings still go through Ollama (`nomic-embed-text`); there is no Anthropic embedding fallback.
- **No LLM**: capture and search still work; Reflect/Dream and insight extraction are skipped.

## Current Status

Active development. See [ROADMAP.md](ROADMAP.md) for the full breakdown
and [`docs/simplification-audit.md`](docs/simplification-audit.md) for
the in-flight simplification-to-cortex-only work.

Key metrics from cognitive evaluation:
- Reflex latency <20ms — design target, not yet reliably pinned in
  real-world sessions
- Pre-DAG-protocol baseline ABR = 0.586 (recorded in
  `docs/eval-journal.md`)

### Known Limitations

- **Embedding bootstrap is brittle.** `cortex reembed` requires existing embeddings; events captured before `nomic-embed-text` is pulled don't get backfilled. Pull the embedding model before the first capture.
- **Background cognition fails silently on missing models.** If the configured `ollama_model` isn't pulled, the REPL's ingest goroutine still drains events but Think/Dream produce zero insights and zero embeddings. Check `cortex status --system` if insights aren't appearing.
- **Provider selection is env-driven, not config-driven.** The REPL must inherit `ANTHROPIC_API_KEY` in its environment to use Anthropic; restart it after exporting the key.

## Documentation

- [CLAUDE.md](CLAUDE.md) - Developer guide for AI assistants
- [ROADMAP.md](ROADMAP.md) - Development status and gaps
- [docs/abstract.md](docs/abstract.md) - Implementation paper with evaluation results
- [docs/context-evolution.md](docs/context-evolution.md) - Theoretical foundations
- [docs/product.md](docs/product.md) - Detailed product documentation
- [docs/eval.md](docs/eval.md) - Evaluation methodology
- [docs/simplification-audit.md](docs/simplification-audit.md) - Current simplification work

## Development

```bash
go build ./cmd/cortex    # Build
go test ./...            # Run tests
go fmt ./...             # Format code
```

Testing uses standard library only—no testify or external assertion libraries.

## License

MIT
