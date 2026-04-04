# Cortex

A context broker that captures development insights and injects them into AI coding assistants.

## Problem

AI coding assistants waste tokens. Every session re-discovers past decisions, re-reads files already understood, and re-establishes context that existed yesterday. This token waste compounds: longer projects mean more redundant context, higher costs, and slower responses.

- **Re-discovered decisions**: "We use JWT" gets explained to the LLM session after session
- **Redundant file reads**: The same architecture files get read and re-read across sessions
- **Repeated context**: Corrections, patterns, and constraints are re-stated manually
- **Cross-tool fragmentation**: Context built in Claude Code is invisible to Cursor and vice versa
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

```bash
go build ./cmd/cortex
./cortex install
./cortex daemon &
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

## Why This Matters

**Local models for background processing.** Think and Dream modes use small, cheap models (Ollama, local inference) for background work. The expensive frontier model is only needed at query time, and even then it receives pre-computed context that reduces the tokens it needs to process.

**Compounding returns over sessions.** Each session captures decisions, corrections, and patterns. The next session starts with that context already available. Over weeks and months, the token savings compound as less and less needs to be re-established.

**Multi-agent amortization.** In multi-agent and factory workflows, context computed once by Cortex is shared across all agents via MCP. Instead of each agent independently building context (multiplying token costs), they share a single pre-computed context pool.

## Cognitive Architecture

Cortex uses five cognitive modes, inspired by how humans process information:

| Mode | Type | Speed | Purpose |
|------|------|-------|---------|
| Reflex | Mechanical | <20ms | "What feels related?" - embeddings, tags, recency |
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
│   ├── capture/         # Fast event capture (<20ms)
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

Cortex stores data in `~/.cortex/` (global) and `.context/` (per-project).

LLM providers:
- **Anthropic**: Set `ANTHROPIC_API_KEY`
- **Ollama**: Local inference at `http://localhost:11434`

## Current Status

~75% complete. See [ROADMAP.md](ROADMAP.md) for details.

Key metrics from initial evaluation:
- 87% pass rate across cognitive mode tests
- <20ms Reflex latency (target met)
- ABR 0.77 (Fast mode achieves 77% of Full mode quality)

### Differentiation from Native AI Memory

| Capability | Claude Code Auto-Memory | Cortex |
|---|---|---|
| Basic recall | Yes | Yes |
| Semantic retrieval | No (flat text) | Yes (embeddings + multi-signal) |
| Cross-tool | No (Claude Code only) | Yes (MCP server) |
| Measurable quality | No | Yes (ABR metric + eval framework) |
| Budget-bounded processing | No | Yes (Think/Dream inverse models) |
| Token cost reduction | No (context grows linearly) | Yes (pre-computed, compounding) |
| Entity graph | No | Yes (structured relationships) |

## Documentation

- [CLAUDE.md](CLAUDE.md) - Developer guide for AI assistants
- [ABSTRACT.md](ABSTRACT.md) - Implementation paper with evaluation results
- [OnContextEvolution.md](OnContextEvolution.md) - Theoretical foundations
- [ROADMAP.md](ROADMAP.md) - Development status and gaps

## Development

```bash
go build ./cmd/cortex    # Build
go test ./...            # Run tests
go fmt ./...             # Format code
```

Testing uses standard library only—no testify or external assertion libraries.

## License

MIT
