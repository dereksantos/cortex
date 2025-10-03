#!/usr/bin/env python3
"""
Test script for the provider system.
"""

import os
import sys
import logging

# Add current directory to path for imports
sys.path.insert(0, os.path.dirname(__file__))

from context_capture.providers import ProviderRouter, OllamaProvider, AnthropicProvider
from context_capture.providers.base import PrivacyLevel

# Setup logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)


def test_provider_creation():
    """Test creating individual providers."""
    print("=== Testing Provider Creation ===")

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

    except Exception as e:
        print(f"❌ Router creation failed: {e}")

    return router


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
    """Test text generation with fallback."""
    print("\n=== Testing Text Generation ===")

    test_prompts = [
        ("broker", "What is machine learning?"),
        ("capture", "Summarize: The stock market went up today due to positive earnings reports."),
    ]

    for task_type, prompt in test_prompts:
        try:
            print(f"\nTesting {task_type} task...")
            print(f"Prompt: {prompt}")

            result = router.generate_with_fallback(
                prompt=prompt,
                task_type=task_type,
                max_tokens=50,
                timeout=10
            )

            if result:
                print(f"✅ Generated: {result[:100]}...")
            else:
                print("❌ Generation failed")

        except Exception as e:
            print(f"❌ Generation error: {e}")


def test_privacy_routing():
    """Test privacy-aware routing."""
    print("\n=== Testing Privacy Routing ===")

    api_key = os.environ.get("ANTHROPIC_API_KEY")

    # Test local-only router
    try:
        local_router = ProviderRouter.create_for_privacy(PrivacyLevel.LOCAL)
        status = local_router.get_provider_status()
        print(f"✅ Local-only router: {status['total_providers']} providers")

        for provider_info in status['providers']:
            print(f"   - {provider_info['name']}: privacy={provider_info['privacy']}")

    except Exception as e:
        print(f"❌ Local router failed: {e}")

    # Test cloud router
    if api_key:
        try:
            cloud_router = ProviderRouter.create_for_privacy(
                PrivacyLevel.CLOUD,
                api_key=api_key
            )
            status = cloud_router.get_provider_status()
            print(f"✅ Cloud-only router: {status['total_providers']} providers")

            for provider_info in status['providers']:
                print(f"   - {provider_info['name']}: privacy={provider_info['privacy']}")

        except Exception as e:
            print(f"❌ Cloud router failed: {e}")


def main():
    """Run all tests."""
    print("Testing Agentic Context Capture Provider System")
    print("=" * 50)

    test_provider_creation()
    router = test_router_creation()

    if router:
        test_provider_selection(router)
        test_generation(router)

    test_privacy_routing()

    print("\n" + "=" * 50)
    print("Provider system testing complete!")


if __name__ == "__main__":
    main()