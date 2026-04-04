# Cortex Product Documentation

**A shared context pipeline that reduces token costs over time**

Cortex captures development decisions, patterns, and corrections from your AI coding sessions and injects them back when relevant. Instead of re-discovering context every session, your AI assistant starts with what it already knows.

---

## What is Cortex?

AI coding assistants waste tokens re-discovering decisions, re-reading files, and re-establishing context that existed in previous sessions. This token waste compounds over time: longer projects mean more redundant context, higher costs, and slower responses.

Cortex is a shared context pipeline that captures development insights and injects them via semantic retrieval, so the expensive frontier model receives pre-computed context instead of rebuilding it from scratch. It works across AI tools (Claude Code, Cursor, any MCP client), uses budget-bounded cognitive modes for background processing, and measures quality through the ABR metric.

## How It Reduces Costs

1. **Cheap background processing**: Think and Dream modes run on small local models (Ollama) during spare cycles and idle periods, extracting durable insights at minimal cost
2. **Pre-computed context**: Background processing populates caches (topic weights, reranking, entity relationships) so the frontier model doesn't have to compute them at query time
3. **Fewer tokens at query time**: Instead of re-reading files and re-discovering decisions, the LLM receives a compact, pre-ranked context injection -- the right information in fewer tokens

```
You: "How should I handle authentication?"
Cortex: [injects] "Previous decision: JWT with refresh tokens (see auth discussion from Dec 15)"
AI: "Based on your previous decision to use JWT..."
```

**Key features:**

- **Zero friction**: Captures automatically via Claude Code hooks (<20ms, imperceptible)
- **Privacy first**: All processing happens locally (Ollama, SQLite)
- **Single binary**: No Python, Node, or Docker dependencies
- **Cognitive modes**: Fast mechanical retrieval + background agentic processing

---

## Quick Start

### Prerequisites

- Go 1.21+ (for building)
- [Ollama](https://ollama.ai) with a model (optional, enables LLM analysis)

### Installation (2 minutes)

```bash
# Clone and build
git clone https://github.com/dereksantos/cortex.git
cd cortex
go build -o cortex ./cmd/cortex

# Install hooks for Claude Code
./cortex install

# Start background processor
./cortex daemon &

# Use Claude Code normally - Cortex captures automatically
```

That's it. Cortex is now running.

### Verify Installation

```bash
# Check status
./cortex status

# After some Claude Code usage, view insights
./cortex insights

# Search your context
./cortex search "authentication"
```

---

## Commands Reference

### Core Commands

| Command | Description |
|---------|-------------|
| `cortex install` | Configure Claude Code hooks and slash commands |
| `cortex uninstall [--purge]` | Remove hooks; `--purge` deletes all data |
| `cortex daemon` | Start background processor (run in background with `&`) |
| `cortex status` | Show current state (used by status line) |
| `cortex watch` | Live dashboard of cognitive mode activity |

### Query Commands

| Command | Description |
|---------|-------------|
| `cortex search <query>` | Search insights and events |
| `cortex insights [category] [limit]` | List extracted insights |
| `cortex recent [N]` | Show N most recent events (default: 10) |
| `cortex entities [type]` | Browse knowledge graph entities |
| `cortex graph <type> <name>` | Show entity relationships |
| `cortex overview` | Summary of all captured context |

### Manual Capture

| Command | Description |
|---------|-------------|
| `cortex capture` | Capture event from stdin (used by hooks) |
| `cortex capture --type=decision --content="..."` | Record decision explicitly |
| `cortex capture --type=correction --content="..."` | Record correction explicitly |
| `cortex forget <id-or-description>` | Mark context as outdated |

### Processing Commands

| Command | Description |
|---------|-------------|
| `cortex ingest` | Move queued events to database (no LLM) |
| `cortex analyze [N]` | Run LLM analysis on N recent events |
| `cortex process` | Ingest + analyze (backward compatibility) |

### Development Commands

| Command | Description |
|---------|-------------|
| `cortex init [--auto]` | Initialize in current project |
| `cortex info` | System information and diagnostics |
| `cortex stats` | Database statistics (JSON) |
| `cortex test [type]` | Test LLM analysis |
| `cortex eval [options]` | Run evaluation framework |
| `cortex version` | Show version |

### Command Details

#### `cortex install`

Configures Claude Code integration:

```bash
./cortex install
# Output:
# Detected Claude Code at ~/.claude/
# Created .claude/settings.local.json with hooks
# Created .claude/commands/cortex.md
# Checking LLM availability...
# Ollama model qwen2.5:3b available
# Installation complete!
```

Creates:
- `.claude/settings.local.json` - Lifecycle hooks (SessionStart, PreToolUse, UserPromptSubmit, PostToolUse, Stop, Notification, SubagentComplete)
- `.claude/commands/cortex.md` - `/cortex` slash command
- `.claude/commands/cortex-recall.md` - `/cortex-recall` slash command
- `.claude/commands/cortex-decide.md` - `/cortex-decide` slash command
- `.claude/commands/cortex-correct.md` - `/cortex-correct` slash command
- `.claude/commands/cortex-forget.md` - `/cortex-forget` slash command

#### `cortex daemon`

Runs the background processor:

```bash
./cortex daemon &   # Run in background
./cortex daemon     # Run in foreground (see logs)
```

The daemon:
- Polls queue every 5 seconds
- Runs LLM analysis on captured events
- Executes cognitive modes (Think, Dream) opportunistically
- Writes state to `.cortex/daemon_state.json` for status line

#### `cortex watch`

Live dashboard showing cognitive mode activity:

```bash
./cortex watch              # Animated live view
./cortex watch --no-animate # Single snapshot
./cortex watch --json       # JSON output
```

Flags:
- `--json` - Machine-readable output
- `--no-animate` - Static snapshot
- `--retrieval-only` - Show only retrieval stats
- `--background-only` - Show only daemon stats

#### `cortex eval`

Run the evaluation framework:

```bash
./cortex eval -p anthropic              # Use Claude Haiku
./cortex eval -p ollama -m qwen2:0.5b   # Fast local model
./cortex eval --dry-run                 # Mock provider (instant)
./cortex eval --cognition               # Run cognitive mode evals
./cortex eval --e2e                     # Full pipeline evals
```

---

## Slash Commands

After running `cortex install`, these slash commands are available in Claude Code:

| Command | Description |
|---------|-------------|
| `/cortex <query>` | Search for relevant context |
| `/cortex-recall <topic>` | Detailed recall on a topic |
| `/cortex-decide <decision>` | Record an architectural decision |
| `/cortex-correct <correction>` | Record a correction (e.g., "we use X not Y") |
| `/cortex-forget <id>` | Mark context as outdated |

### Examples

```
/cortex authentication
→ Shows previous auth-related decisions and patterns

/cortex-decide Use JWT with refresh tokens for stateless auth
→ Records this decision for future sessions

/cortex-correct We use Zustand, not Redux
→ Records correction, will surface when Redux is mentioned

/cortex-recall error handling
→ Detailed summary of error handling patterns
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
  "anthropic_model": "claude-haiku-4-5-20251001",
  "enable_graph": true,
  "enable_vector": true
}
```

### Configuration Options

| Option | Default | Description |
|--------|---------|-------------|
| `context_dir` | `./.cortex` | Where Cortex stores data |
| `project_root` | Current directory | Project root for relative paths |
| `skip_patterns` | Common ignores | Patterns to skip during capture |
| `ollama_url` | `http://localhost:11434` | Ollama API endpoint |
| `ollama_model` | `qwen2.5:3b` | Model for local LLM analysis |
| `anthropic_model` | `claude-haiku-4-5-20251001` | Model for Anthropic API |
| `enable_graph` | `true` | Enable knowledge graph extraction |
| `enable_vector` | `true` | Enable vector embeddings for semantic search |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Anthropic API key (enables Claude for analysis) |

### LLM Priority

Cortex checks LLMs in this order:
1. Anthropic (if `ANTHROPIC_API_KEY` is set)
2. Ollama (if running and model available)
3. No LLM (mechanical-only mode)

---

## Data Storage

All data is stored in `.cortex/` within your project:

```
.cortex/
├── config.json         # Configuration
├── db/
│   └── events.db       # SQLite database (events, insights, entities)
├── queue/
│   ├── pending/        # Captured events awaiting processing
│   ├── processing/     # Currently being processed
│   └── processed/      # Completed events
├── daemon_state.json   # Current daemon/cognitive mode state
├── session.json        # Session context (topic weights, cache)
└── logs/               # Optional log files
```

### Database Schema

The SQLite database contains:

- `events` - Immutable event log (tool uses, captures)
- `insights` - LLM-extracted insights with categories
- `entities` - Knowledge graph nodes (decisions, patterns, etc.)
- `relationships` - Knowledge graph edges
- `events_fts` - Full-text search index

### Backup and Restore

```bash
# Backup
cp -r .cortex .cortex.backup

# Restore
rm -rf .cortex
cp -r .cortex.backup .cortex
```

---

## Integration with Claude Code

### How It Works

Cortex integrates via Claude Code lifecycle hooks:

```
SessionStart     → cortex session-start         (initialize session)
PreToolUse       → cortex inject-context --pre-tool  (proactive injection before writes)
UserPromptSubmit → cortex inject-context         (inject relevant context)
PostToolUse      → cortex capture                (capture events)
Stop             → cortex stop                   (cleanup)
Notification     → cortex capture --notification (capture notifications)
SubagentComplete → cortex capture --subagent     (capture subagent results)
```

Plus a status line that shows current state:

```
statusLine → cortex status
```

### Hooks Configuration

The `cortex install` command creates `.claude/settings.local.json`:

```json
{
  "hooks": {
    "SessionStart": [{
      "hooks": [{"type": "command", "command": "./cortex session-start"}]
    }],
    "PreToolUse": [{
      "matcher": "Write|Edit",
      "hooks": [{"type": "command", "command": "./cortex inject-context --pre-tool"}]
    }],
    "UserPromptSubmit": [{
      "hooks": [{"type": "command", "command": "./cortex inject-context"}]
    }],
    "PostToolUse": [{
      "matcher": "Write|Edit|Bash|Read|Grep|Glob",
      "hooks": [{"type": "command", "command": "./cortex capture"}]
    }],
    "Stop": [{
      "hooks": [{"type": "command", "command": "./cortex stop"}]
    }],
    "Notification": [{
      "hooks": [{"type": "command", "command": "./cortex capture --notification"}]
    }],
    "SubagentComplete": [{
      "hooks": [{"type": "command", "command": "./cortex capture --subagent"}]
    }]
  },
  "statusLine": {
    "type": "command",
    "command": "./cortex status --format=claude"
  }
}
```

### Status Line Icons

The status line shows current cognitive mode:

| Icon | Mode | Meaning |
|------|------|---------|
| `◌` | Cold start | No data yet |
| `✓` | Ready | Normal operation |
| `⏸` | Stopped | Daemon not running |
| `◐` | Think | Learning session patterns |
| `☁` | Dream | Exploring codebase |
| `⚡` | Reflex | Fast mechanical search |
| `◑` | Reflect | Evaluating relevance |
| `▸` | Resolve | Deciding what to inject |
| `✦` | Insight | Discovered something new |
| `~` | Digest | Consolidating insights |

---

## Architecture Overview

### Data Flow

```
AI Tool (Claude Code)
        │
        ↓ PostToolUse hook
cortex capture (<20ms)
        │
        ↓ Atomic file write
File Queue (.cortex/queue/pending/)
        │
        ↓ Daemon polls every 5s
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

**Retrieval Path (synchronous):**

| Mode | Latency | Purpose |
|------|---------|---------|
| Reflex | <20ms | Fast mechanical search (embeddings, tags, recency) |
| Reflect | 200ms+ | LLM reranking, contradiction detection |
| Resolve | 50-100ms | Decide: inject now, wait, or queue |

**Background Processing (asynchronous):**

| Mode | When | Purpose |
|------|------|---------|
| Think | Active periods | Learn session patterns, warm caches |
| Dream | Idle periods | Explore codebase, extract insights |
| Digest | Periodically | Deduplicate and consolidate insights |

**Two retrieval modes:**

- **Fast** (mid-session): Reflex -> Resolve -> Inject (uses cached Reflect results)
- **Full** (session start): Reflex -> Reflect -> Resolve -> Inject (sync, higher accuracy)

The key insight: as Think accumulates context, Fast mode quality approaches Full mode quality.

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
# Check hooks are configured
cat .claude/settings.local.json

# Test capture manually
echo '{"tool_name":"Test"}' | ./cortex capture

# Check queue
ls .cortex/queue/pending/
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
./cortex info

# Database statistics
./cortex stats

# Check daemon state
cat .cortex/daemon_state.json

# Watch cognitive modes live
./cortex watch
```

### Reset Everything

```bash
# Backup first
cp -r .cortex .cortex.backup

# Remove all data
./cortex uninstall --purge

# Reinitialize
./cortex install
./cortex daemon &
```

---

## Product Roadmap

### Current (v0.1)

- [x] Fast event capture (<20ms)
- [x] SQLite storage with event sourcing
- [x] Ollama and Anthropic LLM support
- [x] Claude Code integration (hooks, status line, slash commands)
- [x] Knowledge graph (entities, relationships)
- [x] Cognitive modes (Reflex, Reflect, Resolve, Think, Dream, Digest)
- [x] Evaluation framework

### Near Term

- [x] Vector embeddings for semantic search
- [ ] Embedding model upgrade (all-MiniLM-L12-v2) + re-embedding migration
- [ ] sqlite-vec for indexed vector search
- [ ] MCP server for cross-tool access
- [ ] Expanded hook coverage (PreToolUse, Notification, SubagentComplete)
- [ ] MEMORY.md as DreamSource (complement Claude Code Auto-Memory)

### Future

- [ ] Multi-project support
- [ ] Team-shared context database
- [ ] Web UI / context quality analytics dashboard
- [ ] VS Code extension
- [ ] HTTP hook handler for direct daemon delivery
- [ ] AST-based DreamSource (inspired by Aider's repo map)

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
# Dry run (no LLM)
./cortex eval --dry-run

# With Ollama
./cortex eval -p ollama -m qwen2:0.5b

# With Claude
./cortex eval -p anthropic
```

### Project Structure

```
cortex/
├── cmd/cortex/          # CLI entry point
├── internal/            # Private implementation
│   ├── capture/         # Fast event capture
│   ├── cognition/       # Cognitive mode implementations
│   ├── processor/       # Background daemon
│   ├── queue/           # File-based queue
│   └── storage/         # SQLite storage
├── pkg/                 # Public packages
│   ├── cognition/       # Cognitive mode interfaces
│   ├── config/          # Configuration
│   ├── events/          # Event types
│   └── llm/             # LLM providers
├── integrations/        # Tool integrations
│   ├── claude/          # Claude Code adapter
│   └── cursor/          # Cursor adapter
└── test/evals/          # Evaluation scenarios
```

---

## License

MIT License - see [LICENSE](LICENSE)

---

**Built with Go. Privacy-first. Single binary.**
