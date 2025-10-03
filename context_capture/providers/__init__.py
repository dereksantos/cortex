"""
Model provider abstraction layer for context capture system.

Supports multiple LLM providers including local (Ollama) and cloud
(Anthropic, OpenAI) with automatic fallback and privacy-aware routing.
"""

from context_capture.providers.base import ModelProvider, ProviderConfig
from context_capture.providers.router import ProviderRouter
from context_capture.providers.ollama_provider import OllamaProvider
from context_capture.providers.anthropic_provider import AnthropicProvider

__all__ = [
    "ModelProvider",
    "ProviderConfig",
    "ProviderRouter",
    "OllamaProvider",
    "AnthropicProvider",
]