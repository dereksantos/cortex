# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2025-01-15

### Added
- Initial release of Cortex, an intelligent development context memory system
- Event sourcing architecture with SQLite database
- Fast event capture with <10ms performance target
- Local LLM integration with Ollama for semantic analysis
- Intelligent insight categorization (decisions, patterns, insights, strategies)
- Background async processing with queue management
- Comprehensive CLI interface with 28 commands
- Full-text search with FTS5 support
- Knowledge graph with entities and relationships
- Real-time status monitoring for Claude Code
- Auto-initialization with environment detection
- Privacy-first design - all processing happens locally

### Core Features
- **Fast Capture**: Sub-10ms event capture for AI tool hooks
- **Semantic Analysis**: Local LLM distinguishes important events from noise
- **Pattern Recognition**: Automatic detection of recurring development patterns
- **Decision Tracking**: Capture and analyze architectural decisions
- **Knowledge Graph**: Structured entity and relationship storage
- **Privacy-First**: Zero telemetry, all processing local with Ollama
- **Zero-Friction**: Silent failure design, doesn't interrupt AI tools
- **Single Binary**: ~14MB static binary with zero dependencies

### CLI Commands
- `cortex init [--auto]` - Initialize project with auto-detection
- `cortex capture` - Fast event capture (used by hooks)
- `cortex daemon` - Background async processor
- `cortex search <query>` - Full-text search across events and insights
- `cortex insights [category]` - View categorized insights
- `cortex entities [type]` - Browse knowledge graph entities
- `cortex graph <type> <name>` - Show entity relationships
- `cortex stats` - Database and system statistics
- `cortex info` - System info and model recommendations
- `cortex test [type]` - Test LLM analysis functionality
- `cortex session-start` - Session initialization hook
- `cortex inject-context` - Context injection for AI prompts
- `cortex overview` - Visual summary of captured knowledge
- `cortex cli` - Slash command router for Claude Code

### Integrations
- **Claude Code**: PostToolUse, SessionStart, UserPromptSubmit hooks
- **Cursor IDE**: LSP adapter (basic implementation)
- **Generic**: stdin/stdout interface for any AI tool

### Technical
- Built with Go 1.21+
- SQLite with event sourcing pattern
- File-based queue system for reliability
- Atomic file operations (temp + rename pattern)
- Graceful degradation when Ollama unavailable
- Deduplication (30s window per file)
- 5 parallel async workers for LLM processing
- Configurable via JSON
- Cross-platform: macOS, Linux, Windows

---

For more details about changes, see the [commit history](https://github.com/dereksantos/cortex/commits/main).
