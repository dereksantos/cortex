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

#### Option 1: Homebrew (macOS/Linux) - Recommended

```bash
# Install from tap (once available)
brew tap dereksantos/cortex
brew install cortex

# Or install directly
brew install --HEAD https://raw.githubusercontent.com/dereksantos/cortex/main/Formula/cortex.rb
```

#### Option 2: Install Script

```bash
# Clone and install
git clone https://github.com/dereksantos/cortex.git
cd cortex
./scripts/install.sh
```

#### Option 3: Build from Source

```bash
# Requires Go 1.21+
git clone https://github.com/dereksantos/cortex.git
cd cortex
go build -o cortex ./cmd/cortex
```

### Quick Setup (Automatic)

The easiest way to get started:

```bash
cd your-project
cortex init --auto
cortex daemon
```

This automatically:
- ✅ Detects Claude Code and configures hooks
- ✅ Checks Ollama availability
- ✅ Validates your model installation
- ✅ Provides actionable next steps

### Manual Configuration (Optional)

If you prefer manual setup or use a different AI tool:

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

## 📖 Usage

### CLI Commands

```bash
# Initialize Cortex in current project
cortex init [--auto]         # Use --auto for automatic setup

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

# View insights extracted by LLM
cortex insights [category] [limit]

# Browse entities in the knowledge graph
cortex entities [type]

# Show entity relationships
cortex graph <type> <name>

# View database statistics
cortex stats

# Show status (for status line)
cortex status

# Show help
cortex help
```

### Examples

```bash
# Auto-setup in a new project
$ cortex init --auto
✅ Cortex initialized successfully!
🔍 Auto-detecting environment...
✅ Detected Claude Code
✅ Ollama is running
✅ Model 'mistral:7b' is available

# Search for authentication-related decisions
$ cortex search "auth"
Found 2 results:

1. [claude] Write - 2025-10-04 00:00
   Added JWT authentication middleware

2. [cursor] Edit - 2025-10-04 00:01
   Updated authentication routes

# View extracted insights
$ cortex insights decision
📊 decision Insights:

1. [decision] Chose JWT for stateless authentication ⭐⭐⭐⭐⭐
   Tags: [authentication, security, jwt]
   2025-10-04 00:15

2. [decision] Selected SQLite for event sourcing ⭐⭐⭐⭐
   Tags: [storage, database, architecture]
   2025-10-04 00:12

# Browse entities in knowledge graph
$ cortex entities pattern
🔍 Entities:

1. [pattern] Event Sourcing
   First seen: 2025-10-03, Last seen: 2025-10-04

2. [pattern] Async Processing
   First seen: 2025-10-03, Last seen: 2025-10-04

# View entity relationships
$ cortex graph decision "JWT authentication"
🌐 Knowledge Graph for: JWT authentication (decision)
First seen: 2025-10-04
Last seen: 2025-10-04

Relationships (3):
1. authentication -[implements]-> JWT authentication
2. JWT authentication -[affects]-> API security
3. middleware -[uses]-> JWT authentication

# Check statistics
$ cortex stats
{
  "total_events": 47,
  "by_source": {
    "claude": 32,
    "cursor": 15
  },
  "total_entities": 24,
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

### ✅ Completed (v0.1.0)

- [x] Fast event capture (<10ms)
- [x] SQLite event sourcing storage
- [x] Ollama LLM integration
- [x] Async processor with goroutines (5 workers)
- [x] Full-text search
- [x] Claude Code integration
- [x] Knowledge graph layer (entities, relationships, insights)
- [x] Auto-setup command (`cortex init --auto`)
- [x] Cross-platform builds (macOS, Linux, Windows)
- [x] Homebrew formula
- [x] Generic stdin/stdout integration

### 🚧 In Progress

- [ ] Enhanced status line with Python compatibility
- [ ] Vector embeddings (semantic search)
- [ ] Cursor LSP adapter

### 📋 Planned

- [ ] Multi-project support
- [ ] Graph visualization (web UI for relationships)
- [ ] Team collaboration (cloud sync - optional)
- [ ] Web UI dashboard
- [ ] GitHub Actions integration
- [ ] VS Code extension

## 📄 License

MIT License - see [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- Built with [Go](https://go.dev)
- LLM processing powered by [Ollama](https://ollama.ai)
- Designed for [Claude Code](https://claude.ai/code)

---

**Built with Claude Code** 🤖

If you find Cortex useful, please star the repo and share with your team!
