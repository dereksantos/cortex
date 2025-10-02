# Contributing to Agentic Context Capture

Thank you for your interest in contributing to Agentic Context Capture! This document provides guidelines and information for contributors.

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
   git clone https://github.com/dereksantos/agentic-context-capture.git
   cd agentic-context-capture
   ```

2. **Install development dependencies**
   ```bash
   make install-dev
   # or manually:
   pip install -e ".[dev]"
   pre-commit install
   ```

3. **Verify installation**
   ```bash
   make test
   context-capture --help
   ```

### Development Environment

- **Python**: 3.8+ required
- **Code Style**: We use `black`, `isort`, and `flake8`
- **Type Checking**: `mypy` for type safety
- **Testing**: `pytest` with coverage reporting

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
   make format      # Format code
   make lint        # Check style and quality
   make type-check  # Type checking
   make test        # Run tests
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
make test

# Run with coverage
make test-cov

# Run specific test file
pytest tests/test_capture.py

# Run with specific marker
pytest -m integration
```

### Writing Tests

- Place tests in the `tests/` directory
- Use descriptive test names: `test_capture_should_filter_routine_commands`
- Mock external dependencies (Ollama, file system when appropriate)
- Include both unit and integration tests
- Aim for high coverage on critical paths

Example test structure:
```python
def test_capture_should_skip_routine_commands():
    """Test that routine bash commands are filtered out."""
    capture = ContextCapture()
    event_data = {
        'tool_name': 'Bash',
        'tool_input': {'command': 'ls -la'}
    }

    assert capture.quick_filter(event_data) is True
```

## 📐 Code Style Guidelines

### Python Code Style

- **Line Length**: 88 characters (Black default)
- **Imports**: Use `isort` for import organization
- **Docstrings**: Google-style docstrings for all public functions
- **Type Hints**: Use type hints for all function signatures
- **Variable Names**: Descriptive names, avoid abbreviations

### Example Function

```python
def process_insight_batch(
    insights: List[Dict[str, Any]],
    threshold: float = 0.5
) -> List[ProcessedInsight]:
    """
    Process a batch of insights for storage.

    Args:
        insights: List of raw insight dictionaries
        threshold: Minimum importance threshold for processing

    Returns:
        List of processed insights ready for storage

    Raises:
        ProcessingError: When insight processing fails
    """
    processed = []

    for insight in insights:
        if insight.get('importance', 0.0) >= threshold:
            processed.append(ProcessedInsight.from_dict(insight))

    return processed
```

### Documentation Style

- **README**: Keep main README concise and focused
- **Docstrings**: Explain the "why" not just the "what"
- **Code Comments**: Use sparingly, prefer self-documenting code
- **Type Hints**: Comprehensive type annotations

## 🏗️ Architecture Guidelines

### Package Structure

```
context_capture/
├── core/          # Core functionality (capture, processing, queue)
├── agents/        # Intelligent agents (reflection, synthesis, audit)
├── llm/           # LLM integration and providers
├── utils/         # Utilities (config, status, storage)
└── integrations/  # External tool integrations
```

### Design Principles

1. **Separation of Concerns**: Each module has a single responsibility
2. **Dependency Injection**: Use configuration objects and dependency injection
3. **Error Handling**: Graceful degradation, never interrupt user workflow
4. **Performance**: Optimize for the capture path (<50ms requirement)
5. **Privacy**: All processing must be local and secure

### Adding New Features

1. **Core Features**: Add to appropriate `core/` module
2. **Intelligence Features**: Create new agent in `agents/`
3. **LLM Providers**: Add to `llm/providers.py`
4. **Integrations**: Add to `integrations/`

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
- Python: [e.g., 3.9.0]
- Package Version: [e.g., 0.1.0]
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

1. Update version in `pyproject.toml`
2. Update `CHANGELOG.md`
3. Run full test suite
4. Create release PR
5. Tag release after merge
6. Publish to PyPI

## 📞 Getting Help

If you need help with contributing:

1. **Check Documentation**: Look at existing docs first
2. **Search Issues**: Someone might have asked already
3. **Ask Questions**: Use GitHub Discussions for questions
4. **Be Specific**: Provide context and details

Thank you for contributing to Agentic Context Capture! 🎉