# Cortex Product Documentation

**A self-contained coding harness with a continuous learning loop**

Cortex owns the coding loop (`cortex code` / `cortex repl` / `cortex
run`) and captures development decisions, patterns, and corrections as
each turn runs. Instead of re-discovering context every session, your
next turn starts with what cortex already knows.

---

## What is Cortex?

A coding harness's foreground turn budget is mostly spent rebuilding
context from scratch — files re-read, decisions re-discovered, prior
corrections re-explained. Cortex compresses this load.

Cortex is a coding harness that owns both the foreground loop and a
background learning loop. The foreground path is mechanical and bounded
(<20ms target on Reflex); the agentic modes (Reflect, Resolve, Think,
Dream, Digest) run off-path and feed Reflex via cached artifacts. The
ABR metric quantifies how close fast-path quality gets to full-path
quality as Think accumulates session context.

## How It Reduces Costs

1. **Cheap background processing**: Think and Dream modes run on small
   local models (Ollama) during spare cycles and idle periods,
   extracting durable insights at minimal cost.
2. **Pre-computed context**: Background processing populates caches
   (topic weights, reranking, entity relationships) so foreground
   turns don't recompute them at query time.
3. **Fewer tokens at query time**: Instead of re-reading files and
   re-discovering decisions, the model receives a compact, pre-ranked
   context injection — the right information in fewer tokens.

```
You: "How should I handle authentication?"
Cortex: [injects] "Previous decision: JWT with refresh tokens (see auth discussion from Dec 15)"
Model: "Based on your previous decision to use JWT..."
```

**Key features:**

- **Single binary**: One Go binary, no Python / Node / Docker.
- **Local-first**: All processing happens locally (Ollama, SQLite).
- **Bounded cognition**: Think and Dream operate under inverse-budget
  models, so background work is predictable.
- **Eval-driven**: Every cell of every eval lands in a unified
  `cell_results.jsonl` sink alongside SWE-bench / NIAH / LongMemEval /
  MTEB.

---

## Quick Start

### Prerequisites

- Go 1.21+ (for building)
- [Ollama](https://ollama.ai) with `llama3.1:8b` (or larger) and
  `nomic-embed-text` pulled, *or* `ANTHROPIC_API_KEY` exported.

### Installation (2 minutes)

```bash
# Clone and build
git clone https://github.com/dereksantos/cortex.git
cd cortex
go build -o cortex ./cmd/cortex

# Initialize in a project
./cortex init

# Start background processor
./cortex daemon &

# Use the coding harness
./cortex code "Add a function that returns the Fibonacci sequence."
```

`./cortex install` exists as a compatibility verb that ensures
`.cortex/` is initialized and reports local LLM availability. It no
longer wires anything into an external editor.

### Verify Installation

```bash
# Check status
./cortex status

# After some captures, view insights via the search view
./cortex search --type=insights

# Search context
./cortex search "authentication"
```

---

## Commands Reference

### Lifecycle

| Command | Description |
|---------|-------------|
| `cortex init [--auto]` | Initialize Cortex in current directory |
| `cortex install` | Ensure `.cortex/` exists + report LLM availability |
| `cortex uninstall [--purge]` | Remove `.cortex/` data (requires `--purge` to delete) |
| `cortex daemon` | Run the background processor (dashboard at `:9090`) |
| `cortex status [--system|--memory|--json|--expand]` | Status / dashboards |
| `cortex projects` | List registered projects in `~/.cortex/projects.json` |

### Coding harness

| Command | Description |
|---------|-------------|
| `cortex repl` | Interactive coding loop |
| `cortex code "prompt"` | One-shot coding turn against the cwd |
| `cortex run --type=<turn|eval|think|dream|capture>` | Run a DAG by type |

### Search & query

| Command | Description |
|---------|-------------|
| `cortex search "query"` | Cognitive retrieval over captured context |
| `cortex search --type=recent` | Recent events |
| `cortex search --type=insights [cat]` | Insights (was: standalone `insights`) |
| `cortex search --type=entities [type]` | Knowledge-graph entities |
| `cortex search --type=graph <type> <name>` | Entity relationships |

### Memory ops

| Command | Description |
|---------|-------------|
| `cortex capture --type=decision --content="..."` | Record an event from the CLI |
| `cortex forget <id>` | Mark context as outdated |
| `cortex journal {ingest|rebuild|replay|verify|show|tail|migrate}` | Journal ops |
| `cortex ingest` | Drain queue → DB |
| `cortex analyze [limit]` | Post-hoc LLM analysis on stored events |
| `cortex feed <path>` | Seed knowledge from files / dirs |

### Evals & dev

| Command | Description |
|---------|-------------|
| `cortex eval [-s scenario.yaml -m model]` | Run an eval scenario |
| `cortex measure [--self-eval]` | Prompt-quality measurement primitives |
| `cortex calibrate` | Recompute per-op p50 cost hints |
| `cortex tools` | Generate / verify `tools.json` |
| `cortex prune` | Manage context size relative to project |
| `cortex reembed` | Re-generate embeddings with current model |
| `cortex embed` | One-off embedding helper |

### Command Details

#### `cortex daemon`

Runs the background processor:

```bash
./cortex daemon &   # background
./cortex daemon     # foreground (see logs)
```

The daemon:
- Polls the queue every 5 seconds
- Runs LLM analysis on captured events
- Executes cognitive modes (Think, Dream) opportunistically
- Writes state to `.cortex/daemon_state.json` for the status line
- Serves a dashboard at `http://localhost:9090`

#### `cortex eval`

Run the evaluation framework:

```bash
./cortex eval -s scenario.yaml -m claude-haiku-4-5-20251001
./cortex eval suite mechanic        # mechanic fixture suite
./cortex eval suite journeys        # journey fixture suite
./cortex eval benchmark swebench    # wrapped SWE-bench
```

---

## Configuration

Cortex stores configuration in `.cortex/config.json`:

```json
{
  "context_dir": "/path/to/project/.cortex",
  "project_root": "/path/to/project",
  "skip_patterns": [".git", "node_modules", "venv", "*.lock"],
  "ollama_url": "http://localhost:11434",
  "ollama_model": "qwen2.5:3b",
  "anthropic_model": "claude-haiku-4-5-20251001"
}
```

### Configuration Options

| Key | Default | Description |
|-----|---------|-------------|
| `context_dir` | `<project>/.cortex` | Where Cortex stores data |
| `project_root` | `<cwd>` | Project root for path resolution |
| `skip_patterns` | `[".git", "node_modules", ...]` | Patterns to exclude from capture |
| `ollama_url` | `http://localhost:11434` | Ollama API endpoint |
| `ollama_model` | `qwen2.5:3b` | Model for Ollama analysis |
| `anthropic_model` | `claude-haiku-4-5-20251001` | Model for Anthropic API |

### Environment Variables

| Var | Effect |
|-----|--------|
| `ANTHROPIC_API_KEY` | When set, the LLM client routes through Anthropic; embeddings still go through Ollama |
| `OPENROUTER_API_KEY` | When set, the unified LLM client can route through OpenRouter |
| `CORTEX_NO_TELEMETRY` | Suppress per-invocation `TelemetryRow` writes to `cell_results.jsonl` |

### LLM Priority

1. `ANTHROPIC_API_KEY` set → Anthropic (`claude-haiku-4-5`)
2. Else if Ollama at `http://localhost:11434` → Ollama (`qwen2.5:3b` default)
3. Else → mechanical-only mode (Reflex; Reflect/Dream skipped)

---

## Data Storage

Cortex stores per-project state under `.cortex/`:

```
.cortex/
├── config.json              # Project config
├── daemon_state.json        # Daemon state for status line
├── queue/                   # File-based event queue
│   ├── pending/             # Awaiting daemon pickup
│   └── processed/           # Drained (gzip-rotated)
├── journal/                 # Append-only event log per writer-class
│   ├── capture/
│   ├── observation/
│   ├── dream/
│   ├── reflect/
│   ├── resolve/
│   ├── think/
│   ├── feedback/
│   └── eval/
├── db/                      # Derived state + telemetry
│   ├── cortex.db            # SQLite (events, insights, entities, relationships)
│   ├── cell_results.jsonl   # Unified eval + CLI invocation sink
│   ├── dag_traces.jsonl     # DAG runtime telemetry
│   └── op_cost_hints.json   # Per-op p50 cost hints
└── logs/                    # Diagnostic logs
```

### Backup and Restore

```bash
# Backup
cp -r .cortex .cortex.backup

# Restore
rm -rf .cortex && mv .cortex.backup .cortex
```

The journal is the source of truth — derived state (`db/`,
`storage/projections/`) can be regenerated with `cortex journal
rebuild`.

---

## Architecture Overview

### Data Flow

```
cortex code / repl / run (foreground)
        │
        ↓ events emitted in-process
Journal (.cortex/journal/<class>/*.jsonl)
        │
        ↓ Daemon picks up
cortex daemon
        │
        ├─→ Store in SQLite (events table)
        │
        └─→ LLM Analysis (if available)
                │
                ↓
        Insights, Entities, Relationships
```

### Cognitive Modes

Cortex uses a cognitive architecture inspired by human information processing:

**Retrieval path (synchronous):**

| Mode | Latency | Purpose |
|------|---------|---------|
| Reflex | <20ms target | Fast mechanical search (embeddings, tags, recency) |
| Reflect | 200ms+ | LLM reranking, contradiction detection |
| Resolve | 50-100ms | Decide: inject now, wait, or queue |

**Background processing (asynchronous):**

| Mode | When | Purpose |
|------|------|---------|
| Think | Active periods | Learn session patterns, warm caches |
| Dream | Idle periods | Explore codebase, extract insights |
| Digest | Periodically | Deduplicate and consolidate insights |

**Two retrieval modes:**

- **Fast** (mid-session): Reflex → Resolve → Inject (uses cached Reflect results)
- **Full** (session start): Reflex → Reflect → Resolve → Inject (sync, higher accuracy)

The key insight: as Think accumulates session context, Fast mode
quality approaches Full mode quality. ABR = quality(Fast+Think) /
quality(Full) is the metric that tracks this convergence.

---

## Troubleshooting

### Common Issues

#### "cortex: command not found"

```bash
# Check if in PATH
which cortex

# Or use relative path
./cortex status

# Or add to PATH
export PATH="$PATH:$(pwd)"
```

#### "Ollama not available"

```bash
# Check if Ollama is running
curl http://localhost:11434/api/tags

# Start Ollama
ollama serve

# Pull a model
ollama pull qwen2.5:3b
```

#### "No events captured"

```bash
# Test capture manually
echo '{"event_type":"tool_use","tool_name":"Test"}' | ./cortex capture

# Check journal
ls .cortex/journal/capture/
```

#### "Daemon not running"

```bash
# Check if running
ps aux | grep "cortex daemon"

# Start daemon
./cortex daemon &

# Check status
./cortex status
```

#### "Database locked"

```bash
# Stop all Cortex processes
killall cortex

# Restart daemon
./cortex daemon &
```

### Diagnostics

```bash
# System info and LLM status
./cortex status --system

# Database statistics (raw JSON)
./cortex status --json

# Small-LLM-generated state summary (Ollama; mechanical fallback)
./cortex status --expand

# Check daemon state file directly
cat .cortex/daemon_state.json

# Open the dashboard
open http://localhost:9090
```

### Reset Everything

```bash
# Backup first
cp -r .cortex .cortex.backup

# Remove all data and reinitialize
./cortex uninstall --purge
./cortex init
./cortex daemon &
```

---

## Product Roadmap

### Current

- [x] Single-binary coding harness (`cortex code` / `cortex repl` / `cortex run`)
- [x] Fast event capture (`<20ms` target)
- [x] SQLite storage with event sourcing on top of the journal
- [x] Ollama / Anthropic / OpenRouter LLM providers
- [x] Five cognitive modes (Reflex, Reflect, Resolve, Think, Dream) plus Digest
- [x] Unified `cell_results.jsonl` eval sink (mechanic, journeys,
      legacy-cognition, SWE-bench, NIAH, LongMemEval, MTEB)
- [x] DAG runtime + op registry (`pkg/cognition/dag/`)

### In flight

See `docs/simplification-audit.md` for the current audit. Major slices:
- DAG ops K (`value.score`, `value.detect_contradiction`,
  `decide.should_capture`, `model.predict_next`, `decide.plan`) wired
  into `buildTurnChain`.
- Eval migration E — every legacy / journey / mechanic scenario goes
  through the v2 format + 9-principle quality assessment.

### Future

- [ ] Embedding model upgrade (`all-MiniLM-L12-v2`) + re-embedding migration
- [ ] `sqlite-vec` for indexed vector search
- [ ] Multi-project / team-shared context database
- [ ] Web UI / context-quality analytics dashboard

---

## Development

### Building

```bash
go build -o cortex ./cmd/cortex
```

### Testing

```bash
go test ./...
```

### Evaluation

```bash
# Mechanic suite (fast)
./cortex eval suite mechanic

# Journeys suite
./cortex eval suite journeys

# Specific scenario
./cortex eval -s test/evals/v2/some-scenario.yaml -m claude-haiku-4-5-20251001
```

### Project Structure

```
cortex/
├── cmd/cortex/          # CLI entry point
├── internal/            # Private implementation
│   ├── capture/         # Fast event capture
│   ├── cognition/       # Cognitive mode implementations
│   ├── processor/       # Background daemon
│   ├── journal/         # Append-only event log (source of truth)
│   ├── storage/         # SQLite-backed derived state
│   ├── harness/         # Coding harness wiring
│   ├── eval/v2/         # v2 eval runner + unified cell_results.jsonl sink
│   └── eval/benchmarks/ # Wrapped benchmarks (SWE-bench, NIAH, LongMemEval, MTEB)
├── pkg/                 # Public API
│   ├── cognition/dag/   # DAG engine + op registry
│   ├── cognition/       # Mode interfaces
│   ├── config/          # Configuration
│   ├── events/          # Event types
│   └── llm/             # LLM providers (Anthropic, Ollama, OpenRouter)
└── test/evals/          # Evaluation scenarios
```

---

## License

MIT License - see [LICENSE](LICENSE)
