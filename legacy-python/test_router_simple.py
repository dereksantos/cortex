#!/usr/bin/env python3
"""
Simple test script for the provider router system.
"""

import os
import sys
import logging

# Add current directory to path for imports
sys.path.insert(0, os.path.dirname(__file__))

# Setup logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

# Test provider import and creation directly
def test_provider_imports():
    """Test that provider modules can be imported."""
    print("=== Testing Provider Imports ===")

    try:
        from context_capture.providers.base import ModelProvider, ProviderConfig, ModelSpeed, PrivacyLevel
        print("✅ Base provider classes imported successfully")
    except Exception as e:
        print(f"❌ Base provider import failed: {e}")
        return False

    try:
        from context_capture.providers.ollama_provider import OllamaProvider
        print("✅ OllamaProvider imported successfully")
    except Exception as e:
        print(f"❌ OllamaProvider import failed: {e}")
        return False

    try:
        from context_capture.providers.anthropic_provider import AnthropicProvider
        print("✅ AnthropicProvider imported successfully")
    except Exception as e:
        print(f"❌ AnthropicProvider import failed: {e}")
        return False

    try:
        from context_capture.providers.router import ProviderRouter
        print("✅ ProviderRouter imported successfully")
    except Exception as e:
        print(f"❌ ProviderRouter import failed: {e}")
        return False

    return True

def test_provider_creation():
    """Test creating individual providers."""
    print("\n=== Testing Provider Creation ===")

    from context_capture.providers.ollama_provider import OllamaProvider
    from context_capture.providers.anthropic_provider import AnthropicProvider

    # Test Ollama provider
    try:
        ollama = OllamaProvider()
        print(f"✅ Ollama provider created: {ollama.config.name}")
        print(f"   Model: {ollama.config.model}")
        print(f"   Privacy: {ollama.config.privacy_level.value}")
        print(f"   Available: {ollama.is_available()}")
    except Exception as e:
        print(f"❌ Ollama provider failed: {e}")

    # Test Anthropic provider
    api_key = os.environ.get("ANTHROPIC_API_KEY")
    if api_key:
        try:
            anthropic = AnthropicProvider(api_key=api_key)
            print(f"✅ Anthropic provider created: {anthropic.config.name}")
            print(f"   Model: {anthropic.config.model}")
            print(f"   Privacy: {anthropic.config.privacy_level.value}")
            print(f"   Available: {anthropic.is_available()}")
        except Exception as e:
            print(f"❌ Anthropic provider failed: {e}")
    else:
        print("⚠️  No ANTHROPIC_API_KEY found, skipping Anthropic provider")

def test_router_creation():
    """Test creating and configuring the router."""
    print("\n=== Testing Router Creation ===")

    from context_capture.providers.router import ProviderRouter

    # Test default router
    try:
        api_key = os.environ.get("ANTHROPIC_API_KEY")
        router = ProviderRouter.create_default(api_key=api_key)
        print(f"✅ Default router created with {len(router._providers)} providers")

        status = router.get_provider_status()
        print(f"   Total providers: {status['total_providers']}")
        print(f"   Healthy providers: {status['healthy_providers']}")

        for provider_info in status['providers']:
            print(f"   - {provider_info['name']}: {provider_info['healthy']}")

        return router

    except Exception as e:
        print(f"❌ Router creation failed: {e}")
        return None

def test_provider_selection(router):
    """Test provider selection for different tasks."""
    print("\n=== Testing Provider Selection ===")

    task_types = ["broker", "capture", "analysis", "general"]

    for task_type in task_types:
        try:
            provider = router.get_provider_for_task(task_type=task_type)
            if provider:
                print(f"✅ {task_type}: {provider.config.name} ({provider.config.provider_type})")
            else:
                print(f"❌ {task_type}: No provider found")
        except Exception as e:
            print(f"❌ {task_type}: Selection failed - {e}")

def test_generation(router):
    """Test text generation with a simple prompt."""
    print("\n=== Testing Text Generation ===")

    test_prompt = "What is 2 + 2?"

    try:
        print(f"Testing with prompt: {test_prompt}")

        result = router.generate_with_fallback(
            prompt=test_prompt,
            task_type="broker",
            max_tokens=20,
            timeout=10
        )

        if result:
            print(f"✅ Generated: {result.strip()}")
        else:
            print("❌ Generation failed")

    except Exception as e:
        print(f"❌ Generation error: {e}")

def main():
    """Run all tests."""
    print("Testing Agentic Context Capture Provider System")
    print("=" * 50)

    if not test_provider_imports():
        print("Failed to import providers, stopping tests")
        return

    test_provider_creation()
    router = test_router_creation()

    if router:
        test_provider_selection(router)
        test_generation(router)

    print("\n" + "=" * 50)
    print("Provider system testing complete!")

if __name__ == "__main__":
    main()