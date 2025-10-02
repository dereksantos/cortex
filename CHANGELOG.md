# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Initial release of agentic context capture system
- Core event capture with <50ms performance
- Local LLM integration with Ollama
- Intelligent insight categorization (decisions, patterns, insights, strategies)
- Background processing agent with queue management
- CLI interface with comprehensive commands
- Real-time status monitoring for Claude Code
- Advanced reflection agent for pattern analysis
- Synthesis agent for best practices generation
- Audit agent for decision consistency checking
- Cross-repository context support (planned)
- Team collaboration features (planned)

### Features
- **Fast Capture**: Sub-50ms event capture for Claude Code hooks
- **Semantic Analysis**: Local LLM distinguishes important events from noise
- **Pattern Recognition**: Automatic detection of recurring development patterns
- **Decision Tracking**: Capture and analyze architectural decisions
- **Knowledge Organization**: Structured storage in searchable markdown files
- **Privacy-First**: All processing happens locally
- **Zero-Friction**: Invisible background operation
- **Multi-Model Support**: Works with various local LLM providers

### CLI Commands
- `context-capture init` - Initialize project with context capture
- `context-capture start/stop` - Manage background processor
- `context-capture status` - System health monitoring
- `context-capture reflect` - Advanced pattern reflection
- `context-capture synthesize` - Generate best practices
- `context-capture audit` - Decision consistency analysis
- `context-capture search` - Search captured insights
- `context-capture migrate` - Migrate from existing installations

### Technical
- Modern Python packaging with `pyproject.toml`
- Comprehensive test suite with pytest
- Type hints throughout codebase
- Code quality tools (black, isort, flake8, mypy)
- Rich CLI with progress indicators
- Configurable via YAML
- Extensible agent architecture

## [0.1.0] - 2025-01-01

### Added
- Initial project structure and core functionality
- Migration from proof-of-concept to production-ready system
- Professional packaging and distribution setup

---

For more details about changes, see the [commit history](https://github.com/dereksantos/agentic-context-capture/commits/main).