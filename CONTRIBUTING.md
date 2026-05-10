# Contributing to Cortex

Thank you for your interest in contributing to Cortex! This document provides guidelines and information for contributors.

## 🌟 Ways to Contribute

- **Bug Reports**: Found a bug? Please report it!
- **Feature Requests**: Have an idea for improvement? We'd love to hear it!
- **Code Contributions**: Submit pull requests with bug fixes or new features
- **Documentation**: Help improve our documentation
- **Testing**: Test the system in different environments
- **Community**: Help answer questions and support other users

## 🚀 Getting Started

### Development Setup

1. **Clone the repository**
   ```bash
   git clone https://github.com/dereksantos/cortex.git
   cd cortex
   ```

2. **Install dependencies**
   ```bash
   go mod download
   ```

3. **Build and test**
   ```bash
   go build -o cortex ./cmd/cortex
   go test ./...
   ./cortex --help
   ```

### Development Environment

- **Go**: 1.25+ required (see `go.mod`)
- **Code Style**: Use `gofmt` for formatting, `golint` for linting
- **Testing**: Standard Go testing with `go test`
- **Tools**: [Ollama](https://ollama.ai) for testing LLM features

## 🔧 Development Workflow

### Making Changes

1. **Create a feature branch**
   ```bash
   git checkout -b feature/your-feature-name
   ```

2. **Make your changes**
   - Write clean, readable code
   - Add tests for new functionality
   - Update documentation as needed
   - Follow existing code patterns

3. **Run quality checks**
   ```bash
   go fmt ./...     # Format code
   go vet ./...     # Check for issues
   go test ./...    # Run tests
   ```

4. **Commit your changes**
   ```bash
   git add .
   git commit -m "feat: add new feature description"
   ```

5. **Push and create PR**
   ```bash
   git push origin feature/your-feature-name
   # Create pull request on GitHub
   ```

### Commit Message Convention

We follow [Conventional Commits](https://www.conventionalcommits.org/):

- `feat:` New features
- `fix:` Bug fixes
- `docs:` Documentation changes
- `style:` Code style changes (formatting, etc.)
- `refactor:` Code refactoring
- `test:` Adding or modifying tests
- `chore:` Maintenance tasks

Examples:
```
feat: add cross-repository context bridging
fix: resolve queue processing deadlock
docs: update installation instructions
```

## 🧪 Testing

### Running Tests

```bash
# Run all tests
go test ./...

# Run with coverage
go test -cover ./...

# Run specific package tests
go test ./internal/storage

# Run with verbose output
go test -v ./...
```

### Writing Tests

- Place tests alongside source files (`*_test.go`)
- Use descriptive test names: `TestCaptureShouldFilterRoutineCommands`
- Use table-driven tests when appropriate
- Mock external dependencies (Ollama, file system)
- Include both unit and integration tests
- Aim for high coverage on critical paths

Example test structure:
```go
func TestCaptureShouldSkipRoutineCommands(t *testing.T) {
    capture := capture.New(config.Default())

    event := &events.Event{
        ToolName: "Bash",
        ToolInput: map[string]interface{}{
            "command": "ls -la",
        },
    }

    if !event.ShouldCapture([]string{"node_modules"}) {
        t.Error("Expected routine command to be filtered")
    }
}
```

## 📐 Code Style Guidelines

### Go Code Style

- **Formatting**: Use `gofmt` - no exceptions
- **Naming**: Follow Go conventions (MixedCaps for exported, mixedCaps for unexported)
- **Comments**: Document all exported functions, types, and packages
- **Error Handling**: Always check errors, return them up the call stack
- **Simplicity**: Prefer simple, clear code over clever code

### Example Function

```go
// ProcessInsightBatch processes a batch of insights for storage.
// It filters insights below the importance threshold and returns
// only those that should be stored.
func ProcessInsightBatch(insights []*Insight, threshold float64) ([]*ProcessedInsight, error) {
    if threshold < 0 || threshold > 1 {
        return nil, fmt.Errorf("threshold must be between 0 and 1, got %.2f", threshold)
    }

    var processed []*ProcessedInsight

    for _, insight := range insights {
        if insight.Importance >= threshold {
            p, err := NewProcessedInsight(insight)
            if err != nil {
                return nil, fmt.Errorf("failed to process insight: %w", err)
            }
            processed = append(processed, p)
        }
    }

    return processed, nil
}
```

### Documentation Style

- **README**: Keep main README concise and focused
- **Docstrings**: Explain the "why" not just the "what"
- **Code Comments**: Use sparingly, prefer self-documenting code
- **Type Hints**: Comprehensive type annotations

## 🏗️ Architecture Guidelines

### Package Structure

```
cortex/
├── cmd/cortex/          # CLI entry point
├── internal/            # Internal packages
│   ├── capture/         # Event capture (<10ms)
│   ├── storage/         # SQLite + event sourcing
│   ├── queue/           # File-based queue
│   └── processor/       # Async LLM processor
├── pkg/                 # Public packages
│   ├── events/          # Event format
│   ├── config/          # Configuration
│   └── llm/             # Ollama client
├── integrations/        # AI tool adapters
│   └── claude/          # Claude Code integration
├── scripts/             # Build/release scripts
└── Formula/             # Homebrew formula
```

### Design Principles

1. **Simplicity**: Prefer simple, direct solutions
2. **Performance**: <10ms event capture requirement
3. **Error Handling**: Graceful degradation, silent failure for hooks
4. **Privacy**: All processing is local (Ollama)
5. **Single Binary**: Zero dependencies for end users

### Adding New Features

1. **Core Features**: Add to appropriate `internal/` package
2. **CLI Commands**: Add to `cmd/cortex/main.go`
3. **AI Tool Integrations**: Add to `integrations/`
4. **Public APIs**: Add to `pkg/` (used by integrations)

## 🐛 Bug Reports

### Before Reporting

1. **Search existing issues** to avoid duplicates
2. **Test with latest version** to ensure bug still exists
3. **Minimal reproduction case** helps us fix faster

### Bug Report Template

```markdown
**Bug Description**
Clear description of the issue

**Steps to Reproduce**
1. Step one
2. Step two
3. Step three

**Expected Behavior**
What should happen

**Actual Behavior**
What actually happens

**Environment**
- OS: [e.g., macOS 12.0]
- Go Version: [e.g., 1.21.0]
- Cortex Version: [e.g., 0.1.0]
- Claude Code Version: [if applicable]

**Additional Context**
Logs, screenshots, or other helpful information
```

## 💡 Feature Requests

### Feature Request Template

```markdown
**Feature Description**
Clear description of the proposed feature

**Use Case**
Why is this feature needed? What problem does it solve?

**Proposed Solution**
How should this feature work?

**Alternatives Considered**
Other approaches you've thought about

**Additional Context**
Mock-ups, examples, or other helpful information
```

## 📚 Documentation

### Types of Documentation

1. **API Documentation**: Docstrings in code
2. **User Guide**: How to use features
3. **Architecture**: System design and decisions
4. **Contributing**: This document

### Documentation Guidelines

- **User-Focused**: Write for the user, not the developer
- **Examples**: Include practical examples
- **Up-to-Date**: Keep docs synchronized with code
- **Clear Structure**: Use headings and organization

## 🔍 Code Review Process

### For Contributors

- **Small PRs**: Keep pull requests focused and small
- **Clear Description**: Explain what and why, not just how
- **Tests Included**: New features need tests
- **Documentation Updated**: Update docs for user-facing changes

### Review Criteria

- **Functionality**: Does it work as intended?
- **Quality**: Is the code clean and maintainable?
- **Performance**: Does it meet performance requirements?
- **Security**: Are there any security concerns?
- **Documentation**: Is it properly documented?

## 🌍 Community

### Communication Channels

- **GitHub Issues**: Bug reports and feature requests
- **GitHub Discussions**: General discussion and questions
- **Pull Requests**: Code contributions and reviews

### Code of Conduct

- **Be Respectful**: Treat everyone with respect and kindness
- **Be Constructive**: Provide helpful feedback and suggestions
- **Be Patient**: Remember that everyone is learning
- **Be Inclusive**: Welcome contributors of all backgrounds and skill levels

## 🚀 Release Process

### Versioning

We follow [Semantic Versioning](https://semver.org/):
- **MAJOR**: Incompatible API changes
- **MINOR**: New functionality, backwards compatible
- **PATCH**: Bug fixes, backwards compatible

### Release Checklist

1. Update version in `cmd/cortex/main.go`
2. Update `CHANGELOG.md`
3. Run full test suite (`go test ./...`)
4. Build for all platforms (`scripts/build-all.sh`)
5. Create release PR
6. Tag release after merge
7. GitHub Actions will create release and build artifacts

## 📞 Getting Help

If you need help with contributing:

1. **Check Documentation**: Look at existing docs first
2. **Search Issues**: Someone might have asked already
3. **Ask Questions**: Use GitHub Discussions for questions
4. **Be Specific**: Provide context and details

Thank you for contributing to Cortex! 🎉