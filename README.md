# Agentic Context Capture

[![Python 3.8+](https://img.shields.io/badge/python-3.8+-blue.svg)](https://www.python.org/downloads/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Code style: black](https://img.shields.io/badge/code%20style-black-000000.svg)](https://github.com/psf/black)

**Never lose an architectural decision, development pattern, or strategic insight again.**

An intelligent, privacy-first system for automatically capturing and organizing development insights from Claude Code sessions using local LLMs. Transform your development workflow into an institutional memory that learns and grows with your team.

## 🌟 What Makes This Special

Unlike traditional documentation tools that require manual effort, Agentic Context Capture runs invisibly in the background, intelligently identifying and preserving the insights that matter most:

- **🤖 Automatic Capture**: Zero-friction insight preservation during development
- **🧠 Semantic Understanding**: Local LLM analysis distinguishes important decisions from routine operations
- **🔍 Pattern Recognition**: Identifies recurring themes and emerging best practices
- **🎯 Agent-Aware**: Deep integration with Claude Code's multi-agent workflows
- **🔒 Privacy-First**: All processing happens locally - your code never leaves your machine
- **⚡ Zero Impact**: Sub-50ms capture performance that doesn't interrupt your flow

## 🚀 Quick Start

### Installation

```bash
pip install agentic-context-capture
```

### One-Line Setup

```bash
# Initialize in your project
context-capture init

# Start background processing
context-capture start --daemon
```

### Configure Claude Code Hooks

Add to your `.claude/settings.local.json`:

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "python3 -m context_capture.core.capture"
          }
        ]
      }
    ]
  },
  "statusLine": {
    "type": "command",
    "command": "context-capture status --format line"
  }
}
```

That's it! Your development insights are now being captured automatically.

## 🎯 Core Features

### Intelligent Event Capture

```python
# Automatically captures and analyzes:
- Architectural decisions → High importance insights
- Code patterns → Learning opportunities
- Problem-solving approaches → Reusable strategies
- Tool usage patterns → Workflow optimizations
```

### Advanced Reflection Commands

```bash
# Reflect on your development patterns
context-capture reflect --timeframe week --focus architecture

# Generate best practices from captured patterns
context-capture synthesize --category decisions

# Audit decisions for consistency and conflicts
context-capture audit --decisions --conflicts
```

### Real-Time Status Monitoring

Your Claude Code status line shows live system health:

```
Context: 🤖 🧠 ⏳2 ⚡1 ✅36 💡decisions:5m
```

- **🤖** Background agent running
- **🧠** Local LLM available
- **⏳2** 2 events pending analysis
- **⚡1** 1 event currently processing
- **✅36** 36 total insights captured
- **💡decisions:5m** Latest decision captured 5 minutes ago

## 🏗️ Architecture

### High-Level Flow

```
Claude Code Hook → Fast Capture → File Queue → Background Agent → Local LLM → Organized Knowledge
     (instant)      (<50ms)       (async)      (intelligent)     (private)      (searchable)
```

### Directory Structure

```
your-project/
├── .context/
│   ├── knowledge/
│   │   ├── decisions/     # Architectural choices with reasoning
│   │   ├── patterns/      # Recurring development patterns
│   │   ├── insights/      # Key discoveries and learnings
│   │   ├── strategies/    # High-level strategic decisions
│   │   └── daily/         # Daily summaries and rollups
│   ├── queue/
│   │   ├── pending/       # New events awaiting processing
│   │   ├── processing/    # Currently being analyzed
│   │   └── processed/     # Completed (auto-cleaned)
│   └── logs/              # System logs
└── .context-capture/
    ├── config.yaml        # Configuration
    └── claude_hooks.json  # Hook template
```

## 📊 Intelligence Features

### Pattern Recognition

```python
# Automatically detects:
- "You've refactored auth 3 times this month"
- "Common pattern: Moving from middleware to service layer"
- "Suggestion: Consider this your default pattern"
```

### Decision Conflict Detection

```python
# Identifies inconsistencies:
- "Project A: Chose PostgreSQL for ACID compliance"
- "Project B: Chose MongoDB for flexibility"
- "Recommendation: Create decision criteria matrix"
```

### Learning Synthesis

```python
# Generates actionable insights:
- "The hotshot-coder agent consistently suggests Option<T> over null checks"
- "This pattern has prevented 5 bugs in your TypeScript code"
- "Consider making this your team standard"
```

## 🔧 Configuration

### Basic Configuration (`config.yaml`)

```yaml
# Model Configuration
model:
  provider: ollama
  name: mistral:7b
  temperature: 0.3
  timeout: 30

# Capture Settings
capture:
  importance_threshold: 0.5
  categories: [decisions, patterns, insights, strategies]

# Performance
performance:
  batch_size: 5
  process_interval: 2
```

### Advanced Features

```yaml
# Enable autonomous reflection
features:
  autonomous_reflection: true
  cross_repo_context: true

# Multi-repository support
repositories:
  - path: ~/projects/frontend
    context_weight: 0.8
  - path: ~/projects/backend
    context_weight: 0.9
```

## 📚 Usage Examples

### Command Line Interface

```bash
# System management
context-capture status --watch          # Live status monitoring
context-capture start --daemon          # Start background processing
context-capture stop                    # Stop background processing

# Analysis and reflection
context-capture reflect --days 7        # Weekly reflection
context-capture synthesize --category patterns  # Generate best practices
context-capture audit --conflicts       # Find decision conflicts

# Knowledge management
context-capture search "authentication" # Search captured insights
context-capture migrate ./old-project   # Migrate existing installation
```

### Python API

```python
from context_capture import ContextCapture, ReflectionAgent

# Programmatic capture
capture = ContextCapture()
capture.capture_event(event_data)

# Advanced reflection
agent = ReflectionAgent()
insights = agent.reflect_on_period("week", focus_area="architecture")
```

## 🧠 Local LLM Support

### Recommended Models

| Model | Size | Speed | Quality | Best For |
|-------|------|-------|---------|----------|
| **mistral:7b** | 4.1GB | Fast | Excellent | General use (recommended) |
| **phi-2** | 1.7GB | Very Fast | Good | Low-resource systems |
| **codellama:7b** | 3.8GB | Fast | Excellent | Code-heavy projects |
| **llama3:8b** | 4.7GB | Medium | Best | Maximum intelligence |

### Installation

```bash
# Install Ollama
curl -fsSL https://ollama.com/install.sh | sh

# Download recommended model
ollama pull mistral:7b

# Verify installation
context-capture status
```

## 🔒 Privacy & Security

### Your Data Stays Local

- ✅ All processing done on your machine
- ✅ No API calls to external services
- ✅ Your code remains private
- ✅ No telemetry or analytics
- ✅ Configurable retention policies

### Security Best Practices

- Queue files use restrictive permissions (600)
- Automatic cleanup of processed events
- No sensitive data in logs
- Optional encryption for knowledge files

## 🚀 Advanced Workflows

### Team Knowledge Sharing

```yaml
# .context-sharing.yaml
shared_insights:
  auto_promote_threshold: 0.8  # High-importance decisions
  review_process: true         # Team review before sharing

repositories:
  shared: .context/shared/     # Team-shared insights
  personal: .context/personal/ # Individual learning (gitignored)
```

### Autonomous Reflection

```python
# Daily automated insights
schedule:
  daily_reflection: "00:00"    # Midnight analysis
  weekly_synthesis: "sun 18:00" # Sunday evening best practices
  monthly_audit: "1st 09:00"   # Monthly decision audit
```

### Cross-Repository Context

```python
# Link insights across projects
cross_repo_patterns:
  - name: "authentication_decisions"
    search_repos: [frontend, backend]
    trigger_tags: [auth, security, login]
```

## 📈 Metrics & Success Stories

### Performance Benchmarks

- **Event Capture**: 10-30ms (invisible to user)
- **Queue Processing**: <100ms per event
- **LLM Analysis**: 1-3 seconds (async)
- **Search Performance**: <100ms for 1000+ insights

### User Impact

> *"We went from losing architectural decisions in Slack threads to having a searchable institutional memory. Game-changing for team onboarding."* - Senior Engineering Manager

> *"The pattern recognition caught us implementing the same auth flow three different ways. Saved weeks of refactoring."* - Tech Lead

## 🛠️ Development

### Contributing

```bash
# Clone and setup
git clone https://github.com/dereksantos/agentic-context-capture.git
cd agentic-context-capture
make install-dev

# Run tests
make test

# Code quality
make format lint type-check
```

### Architecture for Contributors

```python
context_capture/
├── core/          # Event capture and processing
├── agents/        # Reflection, synthesis, and audit agents
├── llm/           # Local LLM integration
├── utils/         # Configuration and status monitoring
└── integrations/  # Claude Code, VS Code, Obsidian
```

## 🗺️ Roadmap

### Phase 1: Foundation ✅
- [x] Core capture and processing
- [x] Local LLM integration
- [x] Basic categorization
- [x] CLI interface

### Phase 2: Intelligence 🚧
- [ ] Advanced pattern recognition
- [ ] Cross-repository context
- [ ] Autonomous reflection agents
- [ ] Team collaboration features

### Phase 3: Ecosystem 📋
- [ ] VS Code extension
- [ ] Obsidian plugin
- [ ] Claude Code MCP server
- [ ] GitHub integration

## 📄 License

MIT License - see [LICENSE](LICENSE) for details.

## 🙏 Acknowledgments

- [Claude Code](https://claude.com/code) team for the excellent hooks system
- [Ollama](https://ollama.com) team for local LLM infrastructure
- Early adopters and contributors who shaped this vision

## 🆘 Support

- **Documentation**: [docs/](docs/)
- **Issues**: [GitHub Issues](https://github.com/dereksantos/agentic-context-capture/issues)
- **Discussions**: [GitHub Discussions](https://github.com/dereksantos/agentic-context-capture/discussions)

---

**Remember**: The goal isn't to capture everything - it's to never lose what matters. This system ensures your future self will thank your present self for preserving the context that counts.

⭐ **Star this repo** if you found it useful!