#!/usr/bin/env python3
"""
Direct test of provider modules without package imports.
"""

import os
import sys
import logging
import time

# Add current directory to path for imports
sys.path.insert(0, os.path.dirname(__file__))

# Setup logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

def test_base_classes():
    """Test base provider classes."""
    print("=== Testing Base Classes ===")

    # Import base classes directly
    from context_capture.providers.base import ModelProvider, ProviderConfig, ModelSpeed, PrivacyLevel

    print("✅ Base classes imported successfully")

    # Test enums
    print(f"ModelSpeed options: {[s.value for s in ModelSpeed]}")
    print(f"PrivacyLevel options: {[p.value for p in PrivacyLevel]}")

    # Test ProviderConfig
    config = ProviderConfig(
        name="test/model",
        model="test-model",
        provider_type="test",
        privacy_level=PrivacyLevel.LOCAL,
        speed=ModelSpeed.FAST,
        context_window=8192,
        cost_per_1k_input=0.0,
        cost_per_1k_output=0.0
    )

    print(f"✅ ProviderConfig created: {config.name}")
    print(f"   Local: {config.is_local}")
    print(f"   Cost estimate: ${config.estimate_cost(100, 50):.4f}")

def test_ollama_provider():
    """Test Ollama provider directly."""
    print("\n=== Testing Ollama Provider ===")

    from context_capture.providers.ollama_provider import OllamaProvider

    try:
        # Create provider
        provider = OllamaProvider(model="mistral:7b")
        print(f"✅ OllamaProvider created: {provider.config.name}")
        print(f"   Model: {provider.config.model}")
        print(f"   Privacy: {provider.config.privacy_level.value}")
        print(f"   Speed: {provider.config.speed.value}")
        print(f"   Context: {provider.config.context_window}")

        # Test availability
        available = provider.is_available()
        print(f"   Available: {available}")

        # Test model info
        info = provider.get_model_info()
        print(f"   Info: {info}")

        if available:
            # Test simple generation
            print("   Testing generation...")
            result = provider.generate("What is 2+2?", max_tokens=10, timeout=5)
            if result:
                print(f"   ✅ Generated: {result[:50]}...")
            else:
                print("   ❌ Generation failed")

        return provider

    except Exception as e:
        print(f"❌ Ollama provider failed: {e}")
        return None

def test_anthropic_provider():
    """Test Anthropic provider directly."""
    print("\n=== Testing Anthropic Provider ===")

    from context_capture.providers.anthropic_provider import AnthropicProvider

    api_key = os.environ.get("ANTHROPIC_API_KEY")
    if not api_key:
        print("⚠️  No ANTHROPIC_API_KEY found, skipping test")
        return None

    try:
        # Create provider
        provider = AnthropicProvider(model="haiku-3.5", api_key=api_key)
        print(f"✅ AnthropicProvider created: {provider.config.name}")
        print(f"   Model: {provider.config.model}")
        print(f"   Privacy: {provider.config.privacy_level.value}")
        print(f"   Speed: {provider.config.speed.value}")
        print(f"   Cost per 1k input: ${provider.config.cost_per_1k_input}")

        # Test availability (this makes an API call)
        print("   Testing availability...")
        available = provider.is_available()
        print(f"   Available: {available}")

        if available:
            # Test simple generation
            print("   Testing generation...")
            result = provider.generate("What is 2+2?", max_tokens=10, timeout=5)
            if result:
                print(f"   ✅ Generated: {result[:50]}...")
            else:
                print("   ❌ Generation failed")

        return provider

    except Exception as e:
        print(f"❌ Anthropic provider failed: {e}")
        return None

def test_router():
    """Test router directly."""
    print("\n=== Testing Provider Router ===")

    from context_capture.providers.router import ProviderRouter

    try:
        # Create router
        api_key = os.environ.get("ANTHROPIC_API_KEY")
        router = ProviderRouter(prefer_local=True)
        print("✅ Router created")

        # Add Ollama provider
        from context_capture.providers.ollama_provider import OllamaProvider
        ollama = OllamaProvider()
        router.add_provider(ollama)
        print("✅ Ollama provider added")

        # Add Anthropic provider if API key available
        if api_key:
            from context_capture.providers.anthropic_provider import AnthropicProvider
            anthropic = AnthropicProvider(api_key=api_key)
            router.add_provider(anthropic)
            print("✅ Anthropic provider added")

        # Get status
        status = router.get_provider_status()
        print(f"✅ Router status: {status['total_providers']} total, {status['healthy_providers']} healthy")

        for provider_info in status['providers']:
            print(f"   - {provider_info['name']}: {provider_info['healthy']} (privacy: {provider_info['privacy']})")

        # Test provider selection
        print("\n   Testing provider selection:")
        for task in ["broker", "capture", "analysis"]:
            provider = router.get_provider_for_task(task_type=task)
            if provider:
                print(f"   - {task}: {provider.config.name}")
            else:
                print(f"   - {task}: No provider found")

        # Test generation with fallback
        print("\n   Testing generation with fallback:")
        result = router.generate_with_fallback(
            "What is machine learning?",
            task_type="broker",
            max_tokens=20,
            timeout=10
        )

        if result:
            print(f"   ✅ Generated: {result[:100]}...")
        else:
            print("   ❌ Generation failed")

        return router

    except Exception as e:
        print(f"❌ Router test failed: {e}")
        return None

def main():
    """Run all tests."""
    print("Direct Provider System Testing")
    print("=" * 50)

    try:
        test_base_classes()
        test_ollama_provider()
        test_anthropic_provider()
        test_router()

        print("\n" + "=" * 50)
        print("✅ All tests completed!")

    except Exception as e:
        print(f"\n❌ Test suite failed: {e}")
        import traceback
        traceback.print_exc()

if __name__ == "__main__":
    main()