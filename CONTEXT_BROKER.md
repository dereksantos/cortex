# Context Broker - Intelligent Context Retrieval System

An intelligent "librarian" agent that can instantly find and organize relevant context from captured knowledge for any request or query. The Context Broker acts as the bridge between your captured development insights and AI agents that need contextual information.

## Evolving Communication Architecture

The agentic workflow should evolve itself as the information it can access changes. Agentic processing of the information should compact older data and make current data more accurate.

## 🎯 Overview

The Context Broker is a privacy-first, intelligent context retrieval system that:

- **Instantly finds relevant context** from captured knowledge using semantic search
- **Supports both local and cloud models** with automatic fallback
- **Provides intelligent context injection** with multiple formatting options
- **Maintains complete privacy** when using local models
- **Offers comprehensive CLI tools** for easy interaction

## ⚡ Quick Start

### 1. Check System Status

```bash
./context_broker status
```

Expected output:
```
📊 Context Broker Status
==================================================
Status: healthy
Knowledge Base: .context
Privacy Level: local
🤖 Providers:
  ✅ ollama/mistral:7b (local)
```

### 2. Search for Context

```bash
./context_broker search "local LLMs performance"
```

Expected output:
```
🔍 Searching for: 'local LLMs performance'
✅ Found 1 relevant context items
📋 Summary: The key insight is that using local LLMs with Ollama...
```

### 3. Inject Context into Requests

```bash
echo "How should I choose between local and cloud models?" | ./context_broker inject
```

The broker will enhance your request with relevant context from your knowledge base.

## 🏗️ Architecture

The Context Broker consists of several integrated components:

### Core Components

1. **ContextBroker** - Main orchestrator for intelligent context retrieval
2. **SemanticSearchEngine** - Semantic search with embeddings and similarity matching
3. **ContextInjector** - Smart context formatting for different use cases
4. **ProviderRouter** - Intelligent provider selection and fallback
5. **Configuration System** - Centralized settings management

### Provider System

**Local Providers:**
- **OllamaProvider** - Privacy-first local model inference
- Models: mistral:7b, llama3:8b, codellama:7b, etc.
- Cost: $0 (local inference)
- Privacy: Complete (no data leaves your machine)

**Cloud Providers:**
- **AnthropicProvider** - High-quality cloud inference
- Models: claude-3-haiku, claude-3.5-sonnet, claude-3-opus
- Cost: ~$0.0003-0.015 per 1k tokens
- Privacy: Data sent to Anthropic

### Task-Specific Routing

The broker intelligently selects providers based on task type:

- **Broker tasks** → Fast local models (mistral:7b)
- **Capture tasks** → Balanced local/cloud selection
- **Analysis tasks** → High-capability cloud models

## 📚 Complete CLI Reference

### Context Broker Commands

```bash
# Search for relevant context
./context_broker search "your query here"
./context_broker search "machine learning" --type=insight --max-results=3

# Inject context into requests
./context_broker inject "your request" --agent-type=analysis
./context_broker inject --file=request.txt --output=enhanced.txt

# System management
./context_broker status
./context_broker update --force
./context_broker clear-cache

# Interactive mode
./context_broker interactive
```

### Configuration Commands

```bash
# View current configuration
./context_config show
./context_config show --format=json

# Modify settings
./context_config set broker.max_context_tokens 3000
./context_config set provider.prefer_local true
./context_config get broker.similarity_threshold

# Provider management
./context_config provider enable ollama
./context_config provider disable anthropic
./context_config api-key set anthropic

# Task model preferences
./context_config task-model set broker local
./context_config task-model set analysis cloud

# Configuration management
./context_config validate
./context_config export backup.json
./context_config import backup.json
./context_config reset all --force
```

## ⚙️ Configuration

### Broker Configuration

Located in `.context/broker_config.json`:

```json
{
  "knowledge_base_path": ".context",
  "privacy_level": "local",
  "max_context_tokens": 2000,
  "similarity_threshold": 0.7,
  "cache_ttl_seconds": 300,
  "enable_semantic_search": true,
  "supported_file_types": [".md", ".txt", ".json", ".py", ".js"]
}
```

### Provider Configuration

Located in `.context/provider_config.json`:

```json
{
  "prefer_local": true,
  "fallback_to_cloud": true,
  "ollama_enabled": true,
  "ollama_base_url": "http://localhost:11434",
  "anthropic_enabled": false,
  "broker_model_preference": "local",
  "analysis_model_preference": "cloud"
}
```

### Environment Variables

Override settings using environment variables:

```bash
export CONTEXT_BROKER_PRIVACY_LEVEL=local
export CONTEXT_BROKER_MAX_CONTEXT_TOKENS=3000
export CONTEXT_PROVIDER_PREFER_LOCAL=true
export ANTHROPIC_API_KEY=your_api_key_here
```

## 🔍 Search and Indexing

### Supported File Types

The broker automatically indexes these file types:
- **Markdown** (`.md`) - Documentation, insights, decisions
- **Text** (`.txt`) - Plain text notes and documentation
- **JSON** (`.json`) - Structured data and captured events
- **Code** (`.py`, `.js`, `.go`, `.rs`) - Source code with insights

### Search Features

- **Semantic Search** - Understands context and meaning, not just keywords
- **Similarity Scoring** - Ranks results by relevance (0-100%)
- **Type Filtering** - Search specific content types (decisions, insights, patterns)
- **Automatic Indexing** - Watches for new files and updates index
- **Fast Performance** - Cached embeddings for instant searches

### Knowledge Organization

Recommended directory structure:
```
.context/
├── decisions/           # Architectural decisions
├── insights/           # Key learnings and discoveries
├── patterns/           # Development patterns and best practices
├── strategies/         # Strategic approaches and methodologies
└── daily/             # Daily summaries and quick notes
```

## 🚀 Advanced Usage

### Custom Context Injection

**For Claude Code Agents:**
```bash
./context_broker inject --agent-type=claude-code "Help me implement authentication"
```

**Minimal Injection:**
```bash
./context_broker inject --minimal "Quick question about APIs"
```

**File-based Processing:**
```bash
./context_broker inject --file=agent_request.txt --output=enhanced_request.txt
```

### Search Strategies

**High-precision search:**
```bash
./context_broker search "authentication patterns" --threshold=0.8 --max-results=3
```

**Broad exploration:**
```bash
./context_broker search "performance" --threshold=0.5 --max-results=10
```

**Type-specific search:**
```bash
./context_broker search "database design" --type=decision
```

### Interactive Mode

Start an interactive session for rapid context exploration:

```bash
./context_broker interactive
```

Interactive commands:
- `search <query>` - Search for context
- `status` - Show system status
- `update` - Update knowledge index
- `clear` - Clear cache
- `help` - Show help
- `quit` - Exit

## 🔒 Privacy and Security

### Privacy Levels

**Local (Recommended):**
- All processing done on your machine
- No data sent to external services
- Requires Ollama with local models
- Perfect for sensitive development contexts

**Cloud:**
- Uses external API services (Anthropic)
- Higher quality responses for complex tasks
- Requires API keys and internet connection
- Data sent to cloud providers

**Hybrid:**
- Intelligent routing based on task sensitivity
- Local for broker tasks, cloud for analysis
- Best of both worlds approach

### Data Handling

- **No persistent data collection** - Only processes your local knowledge
- **Configurable data paths** - Full control over where data is stored
- **Local embeddings** - Search embeddings cached locally
- **Secure API handling** - API keys stored in local config files only

## 🛠️ Development and Integration

### Programmatic Usage

```python
from context_capture.broker.core import ContextBroker
from context_capture.providers.router import ProviderRouter

# Create broker
router = ProviderRouter.create_default()
broker = ContextBroker(provider_router=router)

# Search for context
result = broker.get_relevant_context("machine learning patterns")

# Inject context
enhanced = broker.inject_context_for_agent(
    "Help me choose an ML framework",
    agent_type="analysis"
)
```

### Custom Providers

Extend the system with custom model providers:

```python
from context_capture.providers.base import ModelProvider

class CustomProvider(ModelProvider):
    def generate(self, prompt: str, **kwargs) -> Optional[str]:
        # Your custom implementation
        pass

    def is_available(self) -> bool:
        # Health check logic
        pass
```

### Hook Integration

Integrate with Claude Code hooks for automatic context injection:

```bash
# In your project's .claude_hooks
echo 'context_broker inject' > pre_agent_hook
```

## 📊 Performance and Monitoring

### Performance Metrics

- **Search Speed**: 3-6 seconds for semantic search + AI summarization
- **Index Size**: ~100KB for 1000 documents
- **Memory Usage**: ~500MB for Ollama models
- **Response Quality**: 70-90% similarity scores for relevant contexts

### Monitoring Commands

```bash
# System health
./context_broker status

# Search statistics
./context_config show | grep -A5 "Search Engine"

# Provider health
./context_config provider status

# Cache status
./context_broker status | grep "Cache Size"
```

### Troubleshooting

**Common Issues:**

1. **No providers available**
   ```bash
   # Check Ollama is running
   ollama serve &
   ./context_broker status
   ```

2. **Search returns no results**
   ```bash
   # Update knowledge index
   ./context_broker update --force
   # Lower similarity threshold
   ./context_config set broker.similarity_threshold 0.5
   ```

3. **Slow performance**
   ```bash
   # Clear cache
   ./context_broker clear-cache
   # Check provider health
   ./context_config provider status
   ```

## 🎯 Use Cases

### For Development Teams

- **Architectural Decision Support** - Retrieve past decisions for consistency
- **Pattern Recognition** - Find similar solutions to current problems
- **Knowledge Transfer** - Help new team members understand context
- **Code Review Enhancement** - Provide historical context for changes

### For Individual Developers

- **Learning Acceleration** - Build on previous insights and discoveries
- **Problem Solving** - Find solutions to similar past challenges
- **Documentation Enhancement** - Auto-enhance documentation with context
- **Productivity Boost** - Reduce time spent searching for information

### For AI Agents

- **Context-Aware Responses** - Provide agents with relevant project context
- **Consistent Guidance** - Ensure agents follow established patterns
- **Historical Learning** - Help agents learn from past decisions
- **Quality Improvement** - Enhanced responses through contextual knowledge

## 🔮 Future Enhancements

### Planned Features

- **Vector Database Integration** - Enhanced search performance
- **Multi-Language Support** - Context in multiple programming languages
- **Team Collaboration** - Shared knowledge bases
- **Advanced Analytics** - Usage patterns and knowledge gaps
- **Real-time Monitoring** - Live knowledge base updates

### Integration Opportunities

- **VS Code Extension** - Direct editor integration
- **GitHub Actions** - Automated context capture from commits
- **Slack/Discord Bots** - Team knowledge sharing
- **API Gateway** - RESTful API for external integrations

## 📄 License and Contributing

This Context Broker system is part of the Agentic Context Capture project. Contributions welcome for:

- Additional model providers
- Enhanced search algorithms
- CLI feature improvements
- Documentation and examples
- Performance optimizations

---

**Ready to get started?** Run `./context_broker status` to check your system health and begin using intelligent context retrieval for your development workflow!