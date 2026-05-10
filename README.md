# Cortex

[![CI](https://github.com/dereksantos/cortex/actions/workflows/test.yml/badge.svg)](https://github.com/dereksantos/cortex/actions/workflows/test.yml)
[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**A continuous learning harness.** Small local models *think* and *dream* in the background while the frontier model works in the foreground; Cortex is what they pass between each other.

> **Status: Experimental.** The core capture → store → retrieve → inject pipeline works and is in daily use on the author's machine, but Cortex is a research-grade tool, not a polished product. Cognitive eval reports ABR 0.77 (target 0.9). Slash-command UX, MCP cross-tool support, and Cursor integration are early. Small Ollama models (≤3B params) have measured below the floor for insight extraction — `llama3.1:8b` or larger, or `ANTHROPIC_API_KEY`, is recommended. Expect rough edges, breaking changes, and bugs that may require reading code to diagnose. Issues and PRs welcome.

## What Cortex Does

Cortex performs **continuous context integration** alongside your AI coding harness — observing events as they happen, reconciling new information against existing knowledge, and surfacing relevant context proactively. It runs as a single-binary daemon next to Claude Code, Cursor, or Aider, capturing decisions, patterns, and corrections via lightweight hooks and feeding them back through fast mechanical retrieval.

The role sits within the framework of cognitive architectures for language agents (CoALA) and is closely related to sleep-time / background-agent patterns (Letta) and memory-evolution approaches (A-Mem). Two architectural commitments shape Cortex's particular implementation:

**Inverse activity gradient on background processing.** Think runs while you're actively working at *reduced* budget, doing only what spare cycles allow. Dream runs while you're idle and *grows* with idle time, capped at a configurable maximum. This refines the binary active/idle split common in sleep-time approaches with a smooth response to host activity. Both modes are bounded by design.

**Mechanical foreground with a latency target.** Reflex aims to keep retrieval on the critical path fast — <20ms is the design target, with no LLM call on the foreground path. Agentic processing (Reflect, Resolve, Think, Dream) runs off-path and feeds Reflex via cached artifacts. The goal is to keep foreground latency low and predictable so quality can compound in the background; this is an active engineering target, not a number that's been pinned.

Small local models do the continuous integration work; the frontier model receives pre-computed context at query time. The full positioning — lineage, concurrent work, falsification — is in [docs/learning-harness.md](docs/learning-harness.md).

## Problem

AI coding assistants waste tokens. Every session re-discovers past decisions, re-reads files already understood, and re-establishes context that existed yesterday. This token waste compounds: longer projects mean more redundant context, higher costs, and slower responses.

- **Re-discovered decisions**: "We use JWT" gets explained to the LLM session after session
- **Redundant file reads**: The same architecture files get read and re-read across sessions
- **Repeated context**: Corrections, patterns, and constraints are re-stated manually
- **Cross-tool fragmentation**: Context built in one harness doesn't carry into another
- **No measurement**: No way to know if injected context actually reduces downstream token use

Cortex addresses this with a shared context pipeline that reduces token costs over time through semantic retrieval, cross-tool portability via MCP, budget-bounded cognitive modes, and the ABR metric for measurable context quality.

## Solution: A Context Pipeline That Reduces Token Costs Over Time

```
Capture → Filter → Store → Retrieve → Inject
   │         │        │         │         │
  <20ms    Signal   SQLite   Embeddings  Format
  hooks    vs noise  + vec    + rerank   for LLM
```

**Capture**: Hook into AI tools (Claude Code, Cursor), record events without blocking (<20ms target, <50ms acceptable)

**Filter**: Extract durable context—decisions, corrections, patterns. Ignore noise.

**Store**: Immutable event log + embeddings for semantic search

**Retrieve**: Fast mechanical lookup (embeddings) + optional LLM reranking

**Inject**: Format context for consumption by AI tools

## Quick Start with Claude Code

**Prerequisites:**
- Go 1.25+
- Either Ollama at `http://localhost:11434` with `llama3.1:8b` (or larger) and `nomic-embed-text` pulled, **or** `ANTHROPIC_API_KEY` exported. Capture and search work without an LLM; Reflect/Dream and insight extraction need one. Models smaller than ~3B have measured below the task floor — see [docs/eval.md](docs/eval.md).
- Claude Code CLI installed at `~/.claude/` (required for `cortex install`)

```bash
go build -o bin/cortex ./cmd/cortex
./bin/cortex install        # writes hooks to .claude/settings.local.json
./bin/cortex daemon &       # background processor; dashboard at :9090
```

Use Claude Code normally—context is captured automatically.

### Slash Commands

- `/cortex <query>` - Search context
- `/cortex-recall <topic>` - Detailed recall
- `/cortex-decide <decision>` - Record decision
- `/cortex-correct <correction>` - Record correction
- `/cortex-forget <id>` - Mark context as outdated

### Manual Commands

```bash
cortex search "authentication"   # Search for context
cortex insights                  # View extracted insights
cortex recent                    # Show recent events
cortex status                    # Check daemon status
```

## Multi-Agent / CI Setup

For projects with multiple AI agents or shared CI workflows, Cortex can run without hooks or a daemon — capture and search work standalone via the CLI:

```bash
go build -o bin/cortex ./cmd/cortex
./bin/cortex init                                                 # one-time per project
./bin/cortex capture --type=decision --content="Use PostgreSQL"   # called by agents
./bin/cortex ingest                                               # flush queue → DB
./bin/cortex search "database"
```

All agents pointed at the same `.cortex/` directory share context. SQLite WAL handles concurrent readers; concurrent writers may hit brief locks under heavy parallel writes. `cortex init` does not require Claude Code to be installed; only `cortex install` does.

## Why This Matters

**Local models for background processing.** Think and Dream modes use small local models (Ollama) for background work. The frontier model is invoked at query time and receives pre-computed context, so it can spend its tokens reasoning about the task rather than rebuilding context from scratch.

**Compounding returns over sessions.** Each session captures decisions, corrections, and patterns. The next session starts with that context already available. Over weeks and months, the token savings compound as less and less needs to be re-established.

**Multi-agent amortization.** In multi-agent and factory workflows, context computed once by Cortex is shared across all agents via MCP. The same pre-computed context pool serves every agent, so the cost of building it is paid once rather than per agent.

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

### Core

```bash
cortex install           # Configure Claude Code hooks
cortex uninstall         # Remove hooks (--purge to delete data)
cortex daemon            # Start background processor
cortex status            # Show status for status line
```

### Search & Query

```bash
cortex search "query"    # Search captured context
cortex insights [cat]    # Show insights by category
cortex recent [n]        # Show recent events
cortex entities [type]   # Browse knowledge graph entities
cortex graph <type> <n>  # Show entity relationships
```

### Development

```bash
cortex eval              # Run cognitive mode evaluations
cortex watch             # Live dashboard of cognitive modes
cortex test [type]       # Test LLM analysis
```

## Project Structure

```
cortex/
├── cmd/cortex/          # CLI entry point
├── internal/            # Private implementation
│   ├── capture/         # Fast event capture (<20ms target)
│   ├── cognition/       # Five cognitive modes
│   ├── storage/         # SQLite + search
│   └── processor/       # Async event processing
├── pkg/                 # Public API
│   ├── cognition/       # Mode interfaces
│   ├── config/          # Configuration
│   ├── events/          # Event types
│   └── llm/             # LLM providers (Anthropic, Ollama)
└── integrations/        # AI tool adapters
    ├── claude/          # Claude Code
    └── cursor/          # Cursor IDE
```

## Configuration

Cortex stores data in `~/.cortex/` (global, project registry) and `.cortex/` (per-project, captured events + embeddings + queue).

LLM providers — selection is via the `ANTHROPIC_API_KEY` env var (set → Anthropic; unset → Ollama):
- **Ollama**: local inference at `http://localhost:11434`. Free. Recommended models: `llama3.1:8b` for analysis and `nomic-embed-text` for embeddings. Smaller models have measured below the task floor; the configured `ollama_model` in `.cortex/config.json` must actually be pulled or the daemon will silently produce zero insights.
- **Anthropic**: set `ANTHROPIC_API_KEY`. Uses `claude-haiku-4-5` for analysis. Embeddings still go through Ollama (`nomic-embed-text`); there is no Anthropic embedding fallback.
- **No LLM**: capture and search still work; Reflect/Dream and insight extraction are skipped.

## Current Status

Active development. See [ROADMAP.md](ROADMAP.md) for the full breakdown.

Key metrics from cognitive evaluation:
- 87% pass rate across cognitive mode tests
- Reflex latency <20ms target — met on the eval corpus, not yet pinned in real-world sessions where 80–100ms hot-path warnings still appear
- ABR 0.77 (Fast mode achieves 77% of Full mode quality; target 0.9)

### Known Limitations

- **Embedding bootstrap is brittle.** `cortex reembed` requires existing embeddings; events captured before `nomic-embed-text` is pulled don't get backfilled on daemon restart. Pull the embedding model before the first capture.
- **Daemon fails silently on missing models.** If the configured `ollama_model` isn't pulled, the daemon processes events out of the queue but produces zero insights and zero embeddings. Check `cortex info` and `.cortex/logs/daemon.log` if insights aren't appearing.
- **Provider selection is env-driven, not config-driven.** The daemon must inherit `ANTHROPIC_API_KEY` in its environment to use Anthropic; restart it after exporting the key.
- **Cursor integration is design-only.** The `integrations/cursor/` adapter exists, but no IDE extension ships yet.
- **MCP server is unvalidated.** Wired up but not exercised against external clients beyond Claude Code.
- **Hook installation is per-project.** Sessions started in a project where `cortex install` hasn't been run silently capture nothing; no warning is surfaced at session start.

### Cortex alongside Claude Code Auto-Memory

Claude Code Auto-Memory provides flat-file recall scoped to Claude Code, covering basic decision and pattern persistence. Cortex extends the surface in a few directions:

- **Semantic retrieval** via embeddings and multi-signal scoring, in addition to flat-text matching
- **Cross-tool reach** via MCP, so context captured in one harness can serve another
- **Evaluation framework** with the ABR metric for measuring retrieval quality
- **Budget-bounded background processing** (Think/Dream inverse-gradient modes)
- **Entity graph** with structured relationships across captured events

Both can run together — Cortex reads `MEMORY.md` as one of its sources.

## Documentation

- [CLAUDE.md](CLAUDE.md) - Developer guide for AI assistants
- [ROADMAP.md](ROADMAP.md) - Development status and gaps
- [docs/abstract.md](docs/abstract.md) - Implementation paper with evaluation results
- [docs/context-evolution.md](docs/context-evolution.md) - Theoretical foundations
- [docs/product.md](docs/product.md) - Detailed product documentation
- [docs/eval.md](docs/eval.md) - Evaluation methodology

## Development

```bash
go build ./cmd/cortex    # Build
go test ./...            # Run tests
go fmt ./...             # Format code
```

Testing uses standard library only—no testify or external assertion libraries.

## License

MIT
