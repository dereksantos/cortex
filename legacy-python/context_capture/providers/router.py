"""
Intelligent provider routing for optimal model selection.

Handles routing between local (Ollama) and cloud (Anthropic) providers
based on privacy requirements, speed needs, and availability.
"""

import logging
import time
from typing import Any, Dict, List, Optional, Type

from context_capture.providers.base import ModelProvider, ModelSpeed, PrivacyLevel
from context_capture.providers.ollama_provider import OllamaProvider
from context_capture.providers.anthropic_provider import AnthropicProvider


logger = logging.getLogger(__name__)


class ProviderRouter:
    """Intelligent router for selecting optimal model providers."""

    def __init__(self, prefer_local: bool = True, max_cost_per_request: float = 0.01):
        """
        Initialize provider router.

        Args:
            prefer_local: Prefer local models when available
            max_cost_per_request: Maximum cost per request in USD
        """
        self.prefer_local = prefer_local
        self.max_cost_per_request = max_cost_per_request
        self._providers: List[ModelProvider] = []
        self._last_health_check = 0
        self._health_cache: Dict[str, bool] = {}

    def add_provider(self, provider: ModelProvider) -> None:
        """
        Add a provider to the routing pool.

        Args:
            provider: Model provider instance
        """
        if provider not in self._providers:
            self._providers.append(provider)
            logger.info(f"Added provider: {provider.config.name}")

    def remove_provider(self, provider_name: str) -> None:
        """
        Remove a provider from the routing pool.

        Args:
            provider_name: Name of provider to remove
        """
        self._providers = [
            p for p in self._providers
            if p.config.name != provider_name
        ]
        logger.info(f"Removed provider: {provider_name}")

    def get_provider_for_task(
        self,
        task_type: str = "general",
        privacy_level: Optional[PrivacyLevel] = None,
        max_speed: Optional[ModelSpeed] = None,
        max_cost: Optional[float] = None
    ) -> Optional[ModelProvider]:
        """
        Select the best provider for a specific task.

        Args:
            task_type: Task type (broker, capture, analysis, general)
            privacy_level: Required privacy level
            max_speed: Maximum acceptable speed
            max_cost: Maximum acceptable cost per request

        Returns:
            Best available provider or None
        """
        # Update provider health cache
        self._update_health_cache()

        # Filter available providers
        candidates = self._filter_providers(
            privacy_level=privacy_level,
            max_speed=max_speed,
            max_cost=max_cost or self.max_cost_per_request
        )

        if not candidates:
            logger.warning("No providers match the requirements")
            return None

        # Task-specific selection logic
        if task_type == "broker":
            # Broker tasks: prioritize speed and low cost
            return self._select_for_broker(candidates)
        elif task_type == "capture":
            # Capture tasks: balanced speed and capability
            return self._select_for_capture(candidates)
        elif task_type == "analysis":
            # Analysis tasks: prioritize capability
            return self._select_for_analysis(candidates)
        else:
            # General tasks: use default selection
            return self._select_default(candidates)

    def generate_with_fallback(
        self,
        prompt: str,
        task_type: str = "general",
        privacy_level: Optional[PrivacyLevel] = None,
        **kwargs
    ) -> Optional[str]:
        """
        Generate text with automatic fallback to other providers.

        Args:
            prompt: Input prompt
            task_type: Task type for provider selection
            privacy_level: Required privacy level
            **kwargs: Additional generation parameters

        Returns:
            Generated text or None if all providers fail
        """
        # Get primary provider
        provider = self.get_provider_for_task(
            task_type=task_type,
            privacy_level=privacy_level,
            max_speed=kwargs.get("max_speed"),
            max_cost=kwargs.get("max_cost")
        )

        if not provider:
            logger.error("No suitable provider found")
            return None

        # Try primary provider
        try:
            result = provider.generate(prompt, **kwargs)
            if result:
                logger.info(f"Generated with {provider.config.name}")
                return result
        except Exception as e:
            logger.warning(f"Primary provider {provider.config.name} failed: {e}")

        # Try fallback providers
        fallback_candidates = [
            p for p in self._providers
            if p != provider and self._is_healthy(p)
        ]

        for fallback in fallback_candidates:
            # Skip if privacy requirements not met
            if privacy_level and fallback.config.privacy_level != privacy_level:
                continue

            try:
                result = fallback.generate(prompt, **kwargs)
                if result:
                    logger.info(f"Generated with fallback {fallback.config.name}")
                    return result
            except Exception as e:
                logger.warning(f"Fallback provider {fallback.config.name} failed: {e}")

        logger.error("All providers failed")
        return None

    def get_provider_status(self) -> Dict[str, Any]:
        """
        Get status of all registered providers.

        Returns:
            Dictionary with provider status information
        """
        self._update_health_cache()

        status = {
            "total_providers": len(self._providers),
            "healthy_providers": sum(1 for p in self._providers if self._is_healthy(p)),
            "providers": []
        }

        for provider in self._providers:
            provider_status = {
                "name": provider.config.name,
                "type": provider.config.provider_type,
                "model": provider.config.model,
                "privacy": provider.config.privacy_level.value,
                "speed": provider.config.speed.value,
                "healthy": self._is_healthy(provider),
                "cost_per_1k_input": provider.config.cost_per_1k_input,
                "cost_per_1k_output": provider.config.cost_per_1k_output,
            }
            status["providers"].append(provider_status)

        return status

    def _filter_providers(
        self,
        privacy_level: Optional[PrivacyLevel] = None,
        max_speed: Optional[ModelSpeed] = None,
        max_cost: Optional[float] = None
    ) -> List[ModelProvider]:
        """Filter providers based on requirements."""
        candidates = []

        for provider in self._providers:
            # Must be healthy
            if not self._is_healthy(provider):
                continue

            # Privacy level check
            if privacy_level and provider.config.privacy_level != privacy_level:
                continue

            # Speed check (enum ordering: VERY_FAST < FAST < MEDIUM < SLOW)
            if max_speed:
                speed_order = {
                    ModelSpeed.VERY_FAST: 0,
                    ModelSpeed.FAST: 1,
                    ModelSpeed.MEDIUM: 2,
                    ModelSpeed.SLOW: 3
                }
                if speed_order[provider.config.speed] > speed_order[max_speed]:
                    continue

            # Cost check (rough estimate for 200 token input, 200 token output)
            if max_cost is not None:
                estimated_cost = provider.config.estimate_cost(200, 200)
                if estimated_cost > max_cost:
                    continue

            candidates.append(provider)

        return candidates

    def _select_for_broker(self, candidates: List[ModelProvider]) -> Optional[ModelProvider]:
        """Select best provider for broker tasks (fast, cheap)."""
        if self.prefer_local:
            # Try local providers first
            local_candidates = [p for p in candidates if p.config.is_local]
            if local_candidates:
                # Sort by speed (fastest first)
                return min(local_candidates, key=lambda p: p.config.speed.value)

        # Sort by cost then speed
        return min(
            candidates,
            key=lambda p: (p.config.estimate_cost(200, 200), p.config.speed.value)
        )

    def _select_for_capture(self, candidates: List[ModelProvider]) -> Optional[ModelProvider]:
        """Select best provider for capture tasks (balanced)."""
        if self.prefer_local:
            # Try local providers first
            local_candidates = [p for p in candidates if p.config.is_local]
            if local_candidates:
                return local_candidates[0]

        # Balance cost and capability
        scored_candidates = []
        for provider in candidates:
            cost_score = provider.config.estimate_cost(500, 300)  # Larger context
            speed_score = {"very_fast": 1, "fast": 2, "medium": 3, "slow": 4}[provider.config.speed.value]
            total_score = cost_score * 10 + speed_score  # Weight cost more heavily
            scored_candidates.append((total_score, provider))

        return min(scored_candidates, key=lambda x: x[0])[1]

    def _select_for_analysis(self, candidates: List[ModelProvider]) -> Optional[ModelProvider]:
        """Select best provider for analysis tasks (capability focus)."""
        # For analysis, prefer more capable models even if they cost more
        cloud_candidates = [p for p in candidates if p.config.is_cloud]
        local_candidates = [p for p in candidates if p.config.is_local]

        # Try cloud models first for analysis (typically more capable)
        if cloud_candidates:
            # Sort by capability (slower usually means more capable)
            return max(cloud_candidates, key=lambda p: p.config.context_window)

        # Fallback to local if available
        if local_candidates:
            return max(local_candidates, key=lambda p: p.config.context_window)

        return None

    def _select_default(self, candidates: List[ModelProvider]) -> Optional[ModelProvider]:
        """Default selection strategy."""
        if self.prefer_local:
            local_candidates = [p for p in candidates if p.config.is_local]
            if local_candidates:
                return local_candidates[0]

        # Return first available
        return candidates[0] if candidates else None

    def _update_health_cache(self) -> None:
        """Update provider health cache if needed."""
        current_time = time.time()
        if current_time - self._last_health_check < 60:  # Cache for 1 minute
            return

        logger.debug("Updating provider health cache")
        for provider in self._providers:
            try:
                self._health_cache[provider.config.name] = provider.is_available()
            except Exception as e:
                logger.warning(f"Health check failed for {provider.config.name}: {e}")
                self._health_cache[provider.config.name] = False

        self._last_health_check = current_time

    def _is_healthy(self, provider: ModelProvider) -> bool:
        """Check if provider is healthy."""
        return self._health_cache.get(provider.config.name, False)

    @classmethod
    def create_default(cls, api_key: Optional[str] = None, **kwargs) -> "ProviderRouter":
        """
        Create a router with default providers.

        Args:
            api_key: Anthropic API key
            **kwargs: Additional router configuration

        Returns:
            Configured router with default providers
        """
        router = cls(**kwargs)

        # Add Ollama provider (local)
        try:
            # Load provider configuration
            from context_capture.config.provider_config import ProviderConfig
            provider_config = ProviderConfig()
            ollama_model = provider_config.settings.ollama_default_model
            ollama_url = provider_config.settings.ollama_base_url
            ollama = OllamaProvider(model=ollama_model, base_url=ollama_url)
            router.add_provider(ollama)
        except Exception as e:
            logger.warning(f"Could not add Ollama provider: {e}")

        # Add Anthropic provider (cloud)
        if api_key:
            try:
                anthropic = AnthropicProvider(api_key=api_key)
                router.add_provider(anthropic)
            except Exception as e:
                logger.warning(f"Could not add Anthropic provider: {e}")

        return router

    @classmethod
    def create_for_privacy(cls, privacy_level: PrivacyLevel, **kwargs) -> "ProviderRouter":
        """
        Create a router optimized for specific privacy requirements.

        Args:
            privacy_level: Required privacy level
            **kwargs: Additional configuration

        Returns:
            Privacy-configured router
        """
        router = cls(**kwargs)

        if privacy_level == PrivacyLevel.LOCAL:
            # Only add local providers
            try:
                # Load provider configuration
                from context_capture.config.provider_config import ProviderConfig
                provider_config = ProviderConfig()
                ollama_model = provider_config.settings.ollama_default_model
                ollama_url = provider_config.settings.ollama_base_url
                ollama = OllamaProvider(model=ollama_model, base_url=ollama_url)
                router.add_provider(ollama)
            except Exception as e:
                logger.warning(f"Could not add Ollama provider: {e}")

        elif privacy_level == PrivacyLevel.CLOUD:
            # Add cloud providers
            api_key = kwargs.get("api_key")
            if api_key:
                try:
                    anthropic = AnthropicProvider(api_key=api_key)
                    router.add_provider(anthropic)
                except Exception as e:
                    logger.warning(f"Could not add Anthropic provider: {e}")

        else:  # HYBRID
            # Add both types
            return cls.create_default(**kwargs)

        return router