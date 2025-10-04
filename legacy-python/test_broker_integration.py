#!/usr/bin/env python3
"""
Comprehensive integration test for the Context Broker system.

Tests all components working together: providers, broker, search, injection, and configuration.
"""

import os
import sys
import time
import json
import logging
from pathlib import Path

# Add current directory to path for imports
sys.path.insert(0, os.path.dirname(__file__))

# Setup logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)


def test_configuration_system():
    """Test the configuration system."""
    print("🔧 Testing Configuration System")
    print("=" * 50)

    from context_capture.config.broker_config import BrokerConfig, BrokerSettings
    from context_capture.config.provider_config import ProviderConfig, ProviderSettings

    # Test broker config
    try:
        broker_config = BrokerConfig()
        print(f"✅ Broker config loaded from: {broker_config.config_path}")

        # Test setting update
        success = broker_config.update_setting("max_context_tokens", 2500)
        print(f"✅ Setting update: {success}")

        # Test validation
        errors = broker_config.settings.validate()
        print(f"✅ Validation: {len(errors) == 0} ({len(errors)} errors)")

    except Exception as e:
        print(f"❌ Broker config failed: {e}")
        return False

    # Test provider config
    try:
        provider_config = ProviderConfig()
        print(f"✅ Provider config loaded from: {provider_config.config_path}")

        # Test provider status
        status = provider_config.get_status()
        print(f"✅ Provider status: {status['valid']}")

    except Exception as e:
        print(f"❌ Provider config failed: {e}")
        return False

    return True


def test_provider_system():
    """Test the provider abstraction system."""
    print("\n🤖 Testing Provider System")
    print("=" * 50)

    from context_capture.providers.router import ProviderRouter
    from context_capture.providers.ollama_provider import OllamaProvider
    from context_capture.providers.anthropic_provider import AnthropicProvider

    try:
        # Test provider creation
        router = ProviderRouter.create_default()
        print(f"✅ Router created with {len(router._providers)} providers")

        # Test provider status
        status = router.get_provider_status()
        print(f"✅ Provider status: {status['total_providers']} total, {status['healthy_providers']} healthy")

        # Test provider selection
        for task_type in ["broker", "capture", "analysis"]:
            provider = router.get_provider_for_task(task_type=task_type)
            if provider:
                print(f"✅ {task_type} task: {provider.config.name}")
            else:
                print(f"❌ {task_type} task: No provider found")

        # Test generation
        result = router.generate_with_fallback(
            "Test prompt for provider system",
            task_type="broker",
            max_tokens=20,
            timeout=10
        )

        if result:
            print(f"✅ Generation test: {result[:50]}...")
            return True
        else:
            print("❌ Generation test failed")
            return False

    except Exception as e:
        print(f"❌ Provider system failed: {e}")
        return False


def test_search_engine():
    """Test the semantic search engine."""
    print("\n🔍 Testing Search Engine")
    print("=" * 50)

    from context_capture.broker.search import SemanticSearchEngine
    from context_capture.providers.router import ProviderRouter

    try:
        # Create test knowledge
        knowledge_path = Path(".context")
        test_file = knowledge_path / "test_knowledge.md"
        test_file.write_text("""# Test Knowledge

This is a test document about machine learning and artificial intelligence.
It discusses neural networks, deep learning, and natural language processing.

## Important Insights

Local models provide privacy benefits while cloud models offer more capabilities.
The choice depends on the specific use case and requirements.
""")

        # Test search engine
        router = ProviderRouter.create_default()
        search_engine = SemanticSearchEngine(
            knowledge_base_path=knowledge_path,
            provider_router=router
        )

        # Update index
        updated = search_engine.update_index()
        print(f"✅ Index update: {updated}")

        # Test search
        results = search_engine.search(
            query="machine learning privacy",
            max_results=5,
            min_similarity=0.5
        )

        print(f"✅ Search results: {len(results)} items found")
        if results:
            for i, result in enumerate(results[:2]):
                similarity = result.get("similarity", 0)
                content = result.get("content", "")[:100]
                print(f"  {i+1}. Similarity: {similarity:.1%} - {content}...")

        # Test statistics
        stats = search_engine.get_statistics()
        print(f"✅ Search stats: {stats['total_items']} items, {stats['cached_embeddings']} embeddings")

        return len(results) > 0

    except Exception as e:
        print(f"❌ Search engine failed: {e}")
        return False


def test_context_broker():
    """Test the full context broker functionality."""
    print("\n🧠 Testing Context Broker")
    print("=" * 50)

    from context_capture.broker.core import ContextBroker
    from context_capture.providers.router import ProviderRouter

    try:
        # Create broker
        router = ProviderRouter.create_default()
        broker = ContextBroker(
            provider_router=router,
            knowledge_base_path=Path(".context")
        )

        print(f"✅ Broker created: {broker}")

        # Test status
        status = broker.get_broker_status()
        print(f"✅ Broker status: {status['status']}")

        # Test knowledge base update
        updated = broker.update_knowledge_base(force=True)
        print(f"✅ Knowledge base update: {updated}")

        # Test context retrieval
        context_result = broker.get_relevant_context(
            query="How to choose between local and cloud models?",
            max_results=3,
            similarity_threshold=0.6
        )

        contexts = context_result.get("contexts", [])
        summary = context_result.get("summary", "")

        print(f"✅ Context retrieval: {len(contexts)} items")
        if summary:
            print(f"✅ Summary generated: {summary[:100]}...")

        # Test context injection
        if contexts:
            enhanced_request = broker.inject_context_for_agent(
                agent_request="What are the benefits of different model types?",
                agent_type="analysis"
            )

            injection_successful = len(enhanced_request) > len("What are the benefits of different model types?")
            print(f"✅ Context injection: {injection_successful}")

            if injection_successful:
                print(f"   Enhanced request length: {len(enhanced_request)} chars")

        return len(contexts) > 0

    except Exception as e:
        print(f"❌ Context broker failed: {e}")
        return False


def test_cli_functionality():
    """Test CLI functionality."""
    print("\n⚡ Testing CLI Functionality")
    print("=" * 50)

    import subprocess

    try:
        # Test broker CLI help
        result = subprocess.run(
            ["python3", "context_broker", "--help"],
            capture_output=True,
            text=True,
            timeout=10
        )

        if result.returncode == 0:
            print("✅ Broker CLI help works")
        else:
            print(f"❌ Broker CLI help failed: {result.stderr}")
            return False

        # Test config CLI help
        result = subprocess.run(
            ["python3", "context_config", "--help"],
            capture_output=True,
            text=True,
            timeout=10
        )

        if result.returncode == 0:
            print("✅ Config CLI help works")
        else:
            print(f"❌ Config CLI help failed: {result.stderr}")
            return False

        # Test broker status
        result = subprocess.run(
            ["python3", "context_broker", "status"],
            capture_output=True,
            text=True,
            timeout=30
        )

        if result.returncode == 0 and "Context Broker Status" in result.stdout:
            print("✅ Broker status command works")
        else:
            print(f"❌ Broker status failed: {result.stderr}")
            return False

        return True

    except Exception as e:
        print(f"❌ CLI testing failed: {e}")
        return False


def test_end_to_end_workflow():
    """Test a complete end-to-end workflow."""
    print("\n🎯 Testing End-to-End Workflow")
    print("=" * 50)

    try:
        # 1. Create sample knowledge
        knowledge_path = Path(".context")
        knowledge_path.mkdir(exist_ok=True)

        # Create decision document
        decision_file = knowledge_path / "decisions" / "model-selection.md"
        decision_file.parent.mkdir(exist_ok=True)
        decision_file.write_text("""# Model Selection Decision

## Context
We need to choose between local and cloud models for our context broker system.

## Decision
We decided to implement a hybrid approach:
- Use local models (Ollama) for privacy-sensitive broker tasks
- Use cloud models (Anthropic) for complex analysis tasks
- Provide automatic fallback between providers

## Rationale
- Local models provide privacy and cost benefits
- Cloud models offer superior capabilities for complex analysis
- Hybrid approach gives users flexibility and reliability

## Outcome
The implementation allows task-specific provider selection with intelligent routing.
""")

        # Create insight document
        insight_file = knowledge_path / "insights" / "performance-insights.md"
        insight_file.parent.mkdir(exist_ok=True)
        insight_file.write_text("""# Performance Insights

## Key Findings

### Local Model Performance
- Mistral 7B provides excellent response quality for simple tasks
- Average response time: 3-8 seconds
- Memory usage: ~6GB VRAM
- Cost: $0/request (local inference)

### Cloud Model Performance
- Haiku 3.5 provides fast, high-quality responses
- Average response time: 1-3 seconds
- Cost: ~$0.0003/request for typical queries
- Better at complex reasoning and analysis

### Recommendations
- Use local models for frequent, simple tasks (broker, search)
- Use cloud models for infrequent, complex tasks (analysis, synthesis)
- Cache results to minimize cloud API calls
""")

        print("✅ Created sample knowledge base")

        # 2. Initialize and test the full system
        from context_capture.broker.core import ContextBroker
        from context_capture.providers.router import ProviderRouter

        router = ProviderRouter.create_default()
        broker = ContextBroker(provider_router=router, knowledge_base_path=knowledge_path)

        # 3. Update knowledge base
        updated = broker.update_knowledge_base(force=True)
        print(f"✅ Knowledge base indexed: {updated}")

        # 4. Test context search for a realistic query
        test_query = "What factors should I consider when choosing between local and cloud models for my AI application?"

        context_result = broker.get_relevant_context(
            query=test_query,
            max_results=5,
            similarity_threshold=0.5
        )

        contexts = context_result.get("contexts", [])
        summary = context_result.get("summary", "")

        print(f"✅ Found {len(contexts)} relevant contexts")
        if summary:
            print(f"✅ Generated summary: {summary[:150]}...")

        # 5. Test context injection for an agent request
        agent_request = """I'm building an AI-powered application and need to decide on the model architecture.
Should I use local models, cloud APIs, or a hybrid approach? What are the key considerations?"""

        enhanced_request = broker.inject_context_for_agent(
            agent_request=agent_request,
            agent_type="analysis",
            include_summary=True
        )

        injection_ratio = len(enhanced_request) / len(agent_request)
        print(f"✅ Context injection successful: {injection_ratio:.1f}x size increase")

        # 6. Validate the enhanced request contains relevant information
        relevant_keywords = ["local", "cloud", "hybrid", "performance", "cost", "privacy"]
        found_keywords = sum(1 for keyword in relevant_keywords if keyword.lower() in enhanced_request.lower())
        print(f"✅ Enhanced request relevance: {found_keywords}/{len(relevant_keywords)} keywords found")

        # 7. Test broker status
        status = broker.get_broker_status()
        search_stats = status.get("search_engine", {})
        print(f"✅ System status: {search_stats.get('total_items', 0)} items indexed")

        return found_keywords >= 4  # At least 4 out of 6 keywords should be present

    except Exception as e:
        print(f"❌ End-to-end workflow failed: {e}")
        import traceback
        traceback.print_exc()
        return False


def main():
    """Run all integration tests."""
    print("🚀 Context Broker Integration Test Suite")
    print("=" * 60)

    start_time = time.time()

    tests = [
        ("Configuration System", test_configuration_system),
        ("Provider System", test_provider_system),
        ("Search Engine", test_search_engine),
        ("Context Broker", test_context_broker),
        ("CLI Functionality", test_cli_functionality),
        ("End-to-End Workflow", test_end_to_end_workflow),
    ]

    results = []

    for test_name, test_func in tests:
        try:
            result = test_func()
            results.append((test_name, result))
            status = "✅ PASS" if result else "❌ FAIL"
            print(f"\n{status}: {test_name}")
        except Exception as e:
            print(f"\n💥 ERROR: {test_name} - {e}")
            results.append((test_name, False))

    # Summary
    elapsed_time = time.time() - start_time
    passed = sum(1 for _, result in results if result)
    total = len(results)

    print("\n" + "=" * 60)
    print("📊 TEST SUMMARY")
    print("=" * 60)

    for test_name, result in results:
        status = "✅ PASS" if result else "❌ FAIL"
        print(f"{status} {test_name}")

    print(f"\n🎯 Results: {passed}/{total} tests passed")
    print(f"⏱️  Elapsed time: {elapsed_time:.2f} seconds")

    if passed == total:
        print("\n🎉 All tests passed! Context Broker system is fully functional.")
        return True
    else:
        print(f"\n💥 {total - passed} tests failed. System needs attention.")
        return False


if __name__ == "__main__":
    # Run in virtual environment if available
    if "test_env" in sys.prefix or "VIRTUAL_ENV" in os.environ:
        success = main()
        sys.exit(0 if success else 1)
    else:
        print("⚠️  Running without virtual environment. Some tests may fail due to missing dependencies.")
        print("Recommended: source test_env/bin/activate && python test_broker_integration.py")
        success = main()
        sys.exit(0 if success else 1)