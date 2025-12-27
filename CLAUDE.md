# Cortex

A context broker that captures development insights and injects them into AI coding assistants.

## Problem

AI coding assistants forget. Every session starts fresh:
- Decisions made yesterday are unknown today
- Corrections must be repeated ("No, we use Zustand not Redux")
- Architectural constraints get violated
- Patterns aren't consistently applied

## Solution: The Context Pipeline

```
Capture → Filter → Store → Retrieve → Inject
   │         │        │         │         │
  <10ms    Signal   SQLite   Embeddings  Format
  hooks    vs noise  + vec    + rerank   for LLM
```

**Capture**: Hook into AI tools (Claude Code, Cursor), record events without blocking (<10ms)

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

## Cognitive Architecture

Cortex uses five cognitive modes, inspired by how humans process information:

```
┌─────────────────────────────────────────────────────────────┐
│  REFLEX (Mechanical)                                        │
│  "What feels related?"                                      │
│  • Embeddings similarity, tag matching, recency             │
│  • 5-10ms, always runs                                      │
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
| Reflex | Mechanical | 5-10ms | Every retrieval |
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
| Reflex | Precision@K, recall, latency <10ms |
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
