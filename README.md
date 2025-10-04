# Cortex

**Never lose a development decision, code pattern, or architectural insight again.**

Cortex is an intelligent, privacy-first system that automatically captures and organizes development context from AI coding sessions using local LLMs. Transform your development workflow into an institutional memory that learns and grows with your projects.

[![Go](https://img.shields.io/badge/go-1.21+-00ADD8?logo=go)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## 🌟 What Makes Cortex Special

Unlike traditional documentation tools that require manual effort, Cortex runs invisibly in the background:

- **🤖 Automatic Capture**: Zero-friction insight preservation during development (<10ms)
- **🧠 Semantic Understanding**: Local LLM analysis distinguishes important decisions from routine operations
- **🔍 Intelligent Search**: Full-text search across all captured development context
- **🎯 AI Tool Agnostic**: Works with Claude Code, Cursor, Copilot, and any AI coding tool
- **🔒 Privacy-First**: All processing happens locally with Ollama - your code never leaves your machine
- **⚡ Zero Impact**: Sub-10ms event capture that doesn't interrupt your workflow
- **📦 Single Binary**: No dependencies, no complex setup - just one Go binary

## 🚀 Quick Start

### Prerequisites

- Go 1.21+ (for building from source)
- [Ollama](https://ollama.ai) with a model installed (e.g., `mistral:7b`)

### Installation

```bash
# Clone the repository
git clone https://github.com/dereksantos/cortex.git
cd cortex

# Build the binary
go build -o cortex ./cmd/cortex

# Initialize in your project
./cortex init

# Start the background processor
./cortex daemon
```

### Configure Your AI Tool

#### Claude Code

Add to your `.claude/settings.local.json`:

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/path/to/cortex capture"
          }
        ]
      }
    ]
  },
  "statusLine": {
    "type": "command",
    "command": "/path/to/cortex status"
  }
}
```

#### Generic (Any AI Tool)

Pipe events to Cortex via stdin:

```bash
echo '{"tool_name":"Edit","tool_input":{"file":"main.go"},"tool_result":"success"}' | cortex capture
```

That's it! Your development insights are now being captured automatically.

## 📖 Usage

### CLI Commands

```bash
# Initialize Cortex in current project
cortex init

# Capture an event (used by AI tool hooks)
cortex capture

# Start background processor (analyzes events with LLM)
cortex daemon

# Process queue manually (one-time)
cortex process

# Search your development context
cortex search "authentication decisions"

# Show recent events
cortex recent [N]

# View database statistics
cortex stats

# Show status (for status line)
cortex status

# Show help
cortex help
```

### Examples

```bash
# Search for authentication-related decisions
$ cortex search "auth"
Found 2 results:

1. [claude] Write - 2025-10-04 00:00
   Added JWT authentication middleware

2. [cursor] Edit - 2025-10-04 00:01
   Updated authentication routes

# View recent activity
$ cortex recent 5
Recent 5 events:

1. [claude] Edit - 2025-10-04 00:05
   File: api/routes.go

2. [cursor] Write - 2025-10-04 00:03
   File: auth/middleware.go

# Check statistics
$ cortex stats
{
  "total_events": 47,
  "by_source": {
    "claude": 32,
    "cursor": 15
  },
  "total_insights": 12,
  "oldest_event": "2025-10-03T12:00:00Z",
  "newest_event": "2025-10-04T00:05:23Z"
}
```

## 🏗️ Architecture

```
┌─────────────────────────────────────┐
│  AI Tool (Claude/Cursor/Copilot)    │
└─────────────────────────────────────┘
           ↓ PostToolUse Hook
┌─────────────────────────────────────┐
│  cortex capture (<10ms)              │
└─────────────────────────────────────┘
           ↓ Queue to disk
┌─────────────────────────────────────┐
│  File Queue (.context/queue/)        │
└─────────────────────────────────────┘
           ↓ Every 5 seconds
┌─────────────────────────────────────┐
│  cortex daemon (processor)           │
└─────────────────────────────────────┘
           ↓ Event sourcing
┌─────────────────────────────────────┐
│  SQLite Storage (immutable events)   │
└─────────────────────────────────────┘
           ↓ Async analysis
┌─────────────────────────────────────┐
│  Ollama LLM (5 parallel workers)     │
└─────────────────────────────────────┘
           ↓ Extract insights
┌─────────────────────────────────────┐
│  Knowledge Graph + Search            │
└─────────────────────────────────────┘
```

### Event Flow

1. **Capture**: AI tool hook sends event → `cortex capture` → File queue (<10ms)
2. **Process**: Daemon reads queue → Stores in SQLite (event sourcing)
3. **Analyze**: LLM extracts insights → Categories, importance, tags
4. **Query**: Search/ask commands → Full-text search + semantic understanding

## 🎯 Core Features

### Event Sourcing

All events are stored immutably in SQLite:
- **Never lose context**: Complete audit trail of all development decisions
- **Time travel**: Replay events to understand how decisions evolved
- **Multiple projections**: Different views of the same events

### Intelligent Analysis

Ollama-powered LLM analysis extracts:
- **Decisions**: Architectural choices, technology selections
- **Patterns**: Code patterns, design approaches
- **Insights**: Problem-solving strategies, lessons learned
- **Strategies**: Development methodologies, workflows

### Privacy-First

- ✅ All processing happens locally (Ollama)
- ✅ No data leaves your machine
- ✅ No telemetry, no tracking
- ✅ Your code, your context, your control

## 📂 Project Structure

```
cortex/
├── cmd/cortex/          # CLI entry point
├── internal/            # Core implementation
│   ├── capture/         # Fast event capture (<10ms)
│   ├── storage/         # SQLite + event sourcing
│   ├── queue/           # File-based queue manager
│   └── processor/       # Async LLM processor
├── pkg/                 # Public packages
│   ├── events/          # Generic event format
│   ├── config/          # Configuration
│   └── llm/             # Ollama client
└── integrations/        # AI tool adapters
    ├── claude/          # Claude Code integration
    ├── cursor/          # Cursor (planned)
    └── generic/         # Generic stdin/stdout
```

## 🔧 Configuration

Cortex stores configuration in `.context/config.json`:

```json
{
  "context_dir": "/path/to/.context",
  "project_root": "/path/to/project",
  "skip_patterns": [".git", "node_modules", "venv"],
  "ollama_url": "http://localhost:11434",
  "ollama_model": "mistral:7b",
  "enable_graph": true,
  "enable_vector": false
}
```

## 🤝 Contributing

Contributions are welcome! This project is in active development.

### Development Setup

```bash
# Clone and build
git clone https://github.com/dereksantos/cortex.git
cd cortex
go build -o cortex ./cmd/cortex

# Run tests
go test ./...

# Format code
go fmt ./...
```

## 📊 Roadmap

- [x] Fast event capture (<10ms)
- [x] SQLite event sourcing storage
- [x] Ollama LLM integration
- [x] Async processor with goroutines
- [x] Full-text search
- [x] Claude Code integration
- [ ] Knowledge graph queries
- [ ] Vector embeddings (semantic search)
- [ ] Cursor LSP adapter
- [ ] Multi-project support
- [ ] Team collaboration (cloud sync)
- [ ] Web UI dashboard

## 📄 License

MIT License - see [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- Built with [Go](https://go.dev)
- LLM processing powered by [Ollama](https://ollama.ai)
- Designed for [Claude Code](https://claude.ai/code)

---

**Built with Claude Code** 🤖

If you find Cortex useful, please star the repo and share with your team!
