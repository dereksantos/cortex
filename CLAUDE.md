# Cortex

A context broker that captures development insights and injects them into AI coding assistants.

## Problem

Native AI memory (e.g., Claude Code Auto-Memory) handles basic single-tool recall. Cortex addresses what native memory cannot:

- **Token waste**: Sessions re-discover decisions, re-read files, and re-establish context -- compounding costs over time
- **Scale**: Semantic retrieval via embeddings when insights grow to hundreds (vs flat-file context stuffing)
- **Cross-tool**: Portable context across Claude Code, Cursor, Copilot via MCP server
- **Measurability**: ABR metric + eval framework to quantify context quality
- **Budget-bounded intelligence**: Activity-aware Think/Dream modes with inverse budget models; local models preferred for background tasks

## Solution: The Context Pipeline

```
Capture → Filter → Store → Retrieve → Inject
   │         │        │         │         │
  <20ms    Signal   SQLite   Embeddings  Format
  hooks    vs noise  + vec    + rerank   for LLM
```

**Capture**: Hook into AI tools (Claude Code, Cursor), record events without blocking (<20ms target, <50ms acceptable)

**Filter**: Extract durable context - decisions, corrections, patterns. Ignore noise.

**Store**: Immutable event log + embeddings for semantic search

**Retrieve**: Fast mechanical lookup (embeddings) + optional LLM reranking

**Inject**: Format context for consumption by AI tools

## Quick Start with Claude Code

1. Build: `go build ./cmd/cortex`
2. Install: `./cortex install`
3. Start daemon: `./cortex daemon &`
4. Use Claude Code normally - context is captured automatically

### Slash Commands

- `/cortex <query>` - Search context
- `/cortex-recall <topic>` - Detailed recall
- `/cortex-decide <decision>` - Record decision
- `/cortex-correct <correction>` - Record correction
- `/cortex-forget <id>` - Remove outdated context

### Manual Commands

```bash
cortex search "authentication"   # Search for context
cortex insights                  # View extracted insights
cortex recent                    # Show recent events
cortex status                    # Check daemon status
```

## Multi-Agent / CI Setup

For projects with multiple AI agents (e.g., sprite.dev, parallel Claude Code sessions), use Cortex as a shared context layer without hooks or daemon:

1. Build: `go build -o bin/cortex ./cmd/cortex`
2. Init: `./bin/cortex init`
3. Add `.cortex/` to `.gitignore`

All agents share the same `.cortex/` directory — one agent's captured decisions are searchable by all others.

### Capture and search only (no hooks, no daemon)

```bash
# Record a decision or insight
./bin/cortex capture --type=decision --content="Use PostgreSQL for all storage"

# Search for relevant context
./bin/cortex search "database"

# Record a correction
./bin/cortex capture --type=correction --content="Use pgx not database/sql"
```

### Notes for automated environments

- **Binary path**: Check the binary into the repo (e.g., `bin/cortex`) or install to a fixed path so all agents find it
- **Shared `.cortex/`**: The journal (`.cortex/journal/<class>/`) uses per-segment flock for cross-process capture safety. Storage (read side) hydrates from JSONL projection files
- **No daemon needed**: Capture and search work standalone. The daemon adds background processing (Dream/Think) and the web dashboard, but is optional
- **No `~/.claude/` required**: `cortex init` and CLI commands work without Claude Code installed. Only `cortex install` requires it (sets up hooks)
- **Ingest after capture**: Run `./bin/cortex ingest` (or `./bin/cortex journal ingest` — lower-level, no embedding) to project journal entries into storage. Without the daemon, entries stay in the journal until ingest runs

## Journal — source of truth

Cortex uses CQRS event-sourcing. The journal (append-only JSONL per writer-class) is canonical; the storage layer (in-memory indexes + projection JSONL files) is regeneratable from the journal. See [`docs/journal.md`](docs/journal.md) for the architecture and [`docs/journal-implementation-plan.md`](docs/journal-implementation-plan.md) for the slice plan.

Eight writer-classes, each in its own directory:

| Class | Directory | Entry types | fsync |
|---|---|---|---|
| capture | `.cortex/journal/capture/` | `capture.event` | per entry |
| observation | `.cortex/journal/observation/` | `observation.{claude_transcript,git_commit,memory_file}` | per batch |
| dream | `.cortex/journal/dream/` | `dream.insight` | per batch |
| reflect | `.cortex/journal/reflect/` | `reflect.rerank` | per batch |
| resolve | `.cortex/journal/resolve/` | `resolve.retrieval` | per batch |
| think | `.cortex/journal/think/` | `think.{topic_weight,session_context}` | per batch |
| feedback | `.cortex/journal/feedback/` | `feedback.{correction,confirmation,retraction}` | per entry |
| eval | `.cortex/journal/eval/` | `eval.cell_result` | per batch |

CLI surface:
- `cortex journal ingest` — drain journal → storage (one-shot).
- `cortex journal rebuild` — truncate derived state, replay full DAG.
- `cortex journal replay [flags]` — counterfactual-eval primitive (skeleton; full overrides land in a follow-up).
- `cortex journal verify` — cursor + source-offset integrity, plus `.gitignore` privacy check.
- `cortex journal show <offset>` / `cortex journal tail` — inspection.
- `cortex journal migrate` — pack legacy `.cortex/queue/processed/*.json` into capture segments.

Invariants:
- **Local-only**: journal contents never leave the local machine by default. `journal.AssertLocalOnly(path)` is a code-review tripwire for outbound paths.
- **`.cortex/` in `.gitignore`**: enforced at `cortex init`; surfaced by `cortex journal verify` if drift occurs.
- **jq-readable**: plain JSONL, no encryption by default. `cat journal/**/*.jsonl | jq` always works.
- **Closed segments are gzippable**: `journal.CompactClosedSegments` shrinks closed segments ~10×; the reader handles both `.jsonl` and `.jsonl.gz` transparently.

## Cognitive Architecture

Cortex uses five cognitive modes, inspired by how humans process information:

```
┌─────────────────────────────────────────────────────────────┐
│  REFLEX (Mechanical)                                        │
│  "What feels related?"                                      │
│  • Embeddings similarity, tag matching, recency             │
│  • <20ms target, always runs                                │
└─────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────┐
│  REFLECT (Agentic)                                          │
│  "Is this actually relevant to the task?"                   │
│  • LLM reranking, cross-reference constraints               │
│  • Resolve contradictions, check temporal validity          │
│  • 200ms+, sync at session start, async mid-session         │
└─────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────┐
│  RESOLVE (Agentic)                                          │
│  "Should I act now or wait?"                                │
│  • Decide: inject immediately, wait for more context,       │
│    or queue for proactive injection on next hook            │
│  • Confidence thresholds, context completeness              │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│  THINK (Background, Active)                                 │
│  "Let me process this while you're busy"                    │
│  • Runs during active work using spare cycles               │
│  • Budget DECAYS with activity (busier = less capacity)     │
│  • Quick, bounded operations                                │
│  • Like humans: thinking while working exhausts resources   │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│  DREAM (Background, Idle)                                   │
│  "Now that you're resting, let me explore"                  │
│  • Runs during idle periods when resources available        │
│  • Budget GROWS with idle time (capped at MaxBudget)        │
│  • Deeper, exploratory operations                           │
│  • Like humans: dreaming happens when resting               │
└─────────────────────────────────────────────────────────────┘
```

| Mode | Type | Speed | When |
|------|------|-------|------|
| Reflex | Mechanical | <20ms (target) | Every retrieval |
| Reflect | Agentic | 200ms+ | Sync or async |
| Resolve | Agentic | 50-100ms | After results |
| Think | Background | Bounded | Active periods |
| Dream | Background | Bounded | Idle periods |

### Retrieval Modes

Two retrieval paths optimize for different scenarios:

```
Fast (mid-session):     Reflex → Resolve → Inject
                                    ↑
                          (cached Reflect results)
                        Reflect runs async for next time

Full (session start):   Reflex → Reflect → Resolve → Inject
                                    ↓
                          (sync, higher accuracy)
```

**Fast mode**: Minimizes latency during active work. Reflex returns immediately, Reflect runs in background and caches results for subsequent retrievals.

**Full mode**: Used at session start when accuracy matters more than speed. Runs the complete pipeline synchronously.

### Background Modes: Think vs Dream

Think and Dream are triggered opportunistically by Retrieve calls. Activity level determines which runs:

```
Retrieve() called
       │
   [main work: Reflex → (Reflect) → Resolve]
       │
   Check activity level
       │
       ├─ Active? → MaybeThink() - uses spare cycles
       │
       └─ Idle?   → MaybeDream() - deep exploration
```

Budget models are inverse:

| Mode | Activity Level | Budget |
|------|----------------|--------|
| Think | High (busy) | Low (spare cycles only) |
| Think | Low (winding down) | Higher |
| Dream | High (busy) | Skip entirely |
| Dream | Low (idle) | High (capped at MaxBudget) |

Both modes are bounded. Think by spare capacity, Dream by MaxBudget. Neither runs unbounded.

### Dream Sources

Dream explores diverse content via registered `DreamSource` implementations:

| Source | What it samples |
|--------|-----------------|
| Project | Random files (code, docs, configs) |
| Cortex | Stored events, insights, entities |
| Claude History | Session logs, conversations, tool uses |
| Git | Commits, diffs, blame history |

Dream outputs:
- **New embeddings** for unindexed content
- **New insights** (patterns, decisions, constraints)
- **Entity relationships** (connections between concepts)
- **Proactive queue** (items for Resolve to inject opportunistically)

### How Background Modes Influence Resolve

This is how agentic processing benefits mechanical retrieval:

**Think → Resolve** (via `SessionContext`):
| Field | Purpose |
|-------|---------|
| `CachedReflect` | Pre-computed reranking, makes Fast ≈ Full |
| `TopicWeights` | Boost results matching session patterns |
| `WarmCache` | Pre-fetched results for likely queries |
| `ResolvedContradictions` | Conflicts already figured out |

**Dream → Resolve** (via `ProactiveQueue`):
- Important discoveries to inject opportunistically
- Surfaces insights even when not directly queried
- "I found something you should know about"

The result: mechanical modes (Reflex, Resolve) run fast while benefiting from agentic processing (Think, Dream, Reflect) that runs in background.

### Bounded Intelligence: The Three Pillars

Cortex inverts the typical LLM pattern. Instead of letting LLMs decide what to fetch (unbounded exploration), Cortex:

1. Performs **mechanical retrieval first** (Reflex, <20ms)
2. Provides **pre-computed context** from background processing
3. Uses LLMs only with **bounded budgets** for specific tasks

> "The LLM must work with the data it is given to make resource consumption more predictable."

This is achieved through three pillars:

#### Budgeting System

Think and Dream use inverse budget models for predictable resource consumption:

**Think (Active Periods)**
```
ThinkBudget = MaxBudget × (1 - ActivityLevel)
```
- High activity (rapid queries) → low budget (spare cycles only)
- Low activity (pauses) → higher budget
- Default: MaxBudget=5, MinBudget=1, ActivityWindow=1min

**Dream (Idle Periods)**
```
DreamBudget = min(IdleTime × GrowthRate, MaxBudget)
```
- Short idle → small budget
- Long idle → capped at MaxBudget
- Default: MaxBudget=20, MinBudget=2, GrowthDuration=10min

#### Prompts

Each agentic mode uses a structured prompt that defines its "contract" with the LLM:

| Mode | Prompt Purpose | Output Format |
|------|----------------|---------------|
| Reflect | Rerank candidates, detect contradictions | JSON: `{ranking[], contradictions[], reasoning}` |
| Dream | Extract durable insights from content | JSON: `{content, category, importance, tags[]}` |

Prompt philosophy: Extract **DURABLE**, **ACTIONABLE**, **TEACHABLE** context. Graceful degradation when LLM unavailable.

#### Pre-computed Datasets

Think produces `SessionContext` that accelerates future retrievals:

| Field | Source | Consumer | Purpose |
|-------|--------|----------|---------|
| `TopicWeights` | Query pattern analysis | Resolve | Boost scores for session-relevant topics |
| `CachedReflect` | Background Reflect runs | Fast mode | Pre-computed reranking results |
| `ResolvedContradictions` | Reflect metadata | Resolve | Avoid re-resolving known conflicts |
| `ProactiveQueue` | Dream discoveries | Resolve | Opportunistic injection of insights |

This is how Fast mode approaches Full mode quality without blocking on LLM calls.

## Go Patterns

**Error handling**: Wrap with context, use `fmt.Errorf("failed to X: %w", err)`

**Naming**:
- Constructors: `NewXxx(cfg *config.Config)`
- Interfaces: `Provider`, `Storage` (noun, not IProvider)

**Package structure**:
- `cmd/` - CLI entry points
- `internal/` - Private implementation
- `pkg/` - Public API

**Testing**: Table-driven tests with `t.Run` subtests, standard library only

**LLM calls**: Use `pkg/llm.Provider` interface, support both Ollama and Anthropic

## Constraints

**Testing**: Use standard library `testing` package only
- Assertions: `t.Errorf`, `t.Fatalf`, `t.Fatal`
- No testify, assert, or external assertion libraries
- Table-driven tests with `t.Run` subtests
- Setup/teardown via `defer` (e.g., `defer os.RemoveAll(tempDir)`)

## Key Files

- `pkg/cognition/` - Cognitive mode interfaces (Reflex, Reflect, Resolve, Think, Dream)
- `pkg/llm/` - LLM providers
- `pkg/events/` - Event types
- `internal/capture/` - Fast event capture
- `internal/storage/` - SQLite + search
- `internal/eval/` - Eval framework (includes `cognition.go` for cognitive mode evals)
- `test/evals/scenarios/` - Test scenarios

## Cognition Evals

The cognitive architecture requires specialized evals:

### Eval Types

| Type | What it tests |
|------|---------------|
| Mode | Each cognitive mode in isolation |
| Session | Accumulation over multiple interactions |
| Benefit | Agentic Benefit Ratio (ABR = Fast+Think / Full) |
| Pipeline | End-to-end retrieval quality |
| Dream | Source coverage, insight quality |

### Key Metrics

**Agentic Benefit Ratio (ABR)**: Measures how well Think makes Fast mode perform like Full mode.

```
ABR = quality(Fast + Think) / quality(Full)

Goal: ABR → 1.0 as session progresses
```

**Per-mode metrics:**

| Mode | Metrics |
|------|---------|
| Reflex | Precision@K, recall, latency <20ms |
| Reflect | NDCG, contradiction detection |
| Resolve | Decision accuracy (inject/wait/queue) |
| Think | TopicWeight accuracy, cache hit rate |
| Dream | Source coverage, insights generated |

### Session Eval Example

```yaml
id: think-learns-patterns
type: session
name: "Think learns session patterns"
session_steps:
  - id: step1
    query:
      text: "How does authentication work?"
    expected_result_ids: ["auth_module", "jwt_handler"]

  - id: step2
    query:
      text: "Show me the login flow"
    expect_topic_weights:
      authentication: 0.7  # Think should learn this

  - id: step3
    query:
      text: "What about session tokens?"
    expect_cache_hit: true
    expect_quality_vs_full: ">= 0.9"
```

## Eval Commands

```bash
./cortex eval -p anthropic -v          # Run with Claude Haiku
./cortex eval -p ollama -m qwen2:0.5b  # Fast local model
./cortex eval --dry-run                # Mock provider (instant)
```
