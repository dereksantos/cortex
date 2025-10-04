"""
Base provider interface for all model providers.
"""

from abc import ABC, abstractmethod
from dataclasses import dataclass
from enum import Enum
from typing import Any, Dict, Optional


class ModelSpeed(Enum):
    """Model response speed categories."""
    VERY_FAST = "very_fast"  # <500ms
    FAST = "fast"  # 500ms-2s
    MEDIUM = "medium"  # 2-5s
    SLOW = "slow"  # >5s


class PrivacyLevel(Enum):
    """Privacy level for model providers."""
    LOCAL = "local"  # Completely local, no data leaves machine
    CLOUD = "cloud"  # Data sent to cloud provider
    HYBRID = "hybrid"  # Can be configured for either


@dataclass
class ProviderConfig:
    """Configuration for a model provider."""

    name: str
    model: str
    provider_type: str
    privacy_level: PrivacyLevel
    speed: ModelSpeed
    context_window: int = 4096
    max_tokens: int = 500
    temperature: float = 0.3
    api_key: Optional[str] = None
    base_url: Optional[str] = None
    cost_per_1k_input: float = 0.0
    cost_per_1k_output: float = 0.0

    @property
    def is_local(self) -> bool:
        """Check if this is a local provider."""
        return self.privacy_level == PrivacyLevel.LOCAL

    @property
    def is_cloud(self) -> bool:
        """Check if this is a cloud provider."""
        return self.privacy_level == PrivacyLevel.CLOUD

    def estimate_cost(self, input_tokens: int, output_tokens: int) -> float:
        """Estimate cost for a request."""
        input_cost = (input_tokens / 1000) * self.cost_per_1k_input
        output_cost = (output_tokens / 1000) * self.cost_per_1k_output
        return input_cost + output_cost


class ModelProvider(ABC):
    """Abstract base class for all model providers."""

    def __init__(self, config: ProviderConfig):
        """
        Initialize provider with configuration.

        Args:
            config: Provider configuration
        """
        self.config = config
        self._is_available: Optional[bool] = None
        self._last_check_time: float = 0

    @abstractmethod
    def generate(self, prompt: str, **kwargs) -> Optional[str]:
        """
        Generate text from the model.

        Args:
            prompt: Input prompt
            **kwargs: Additional provider-specific parameters

        Returns:
            Generated text or None if failed
        """
        pass

    @abstractmethod
    def is_available(self) -> bool:
        """
        Check if the provider/model is available.

        Returns:
            True if available, False otherwise
        """
        pass

    @abstractmethod
    def validate_config(self) -> bool:
        """
        Validate provider configuration.

        Returns:
            True if configuration is valid
        """
        pass

    def estimate_tokens(self, text: str) -> int:
        """
        Estimate token count for text (rough approximation).

        Args:
            text: Input text

        Returns:
            Estimated token count
        """
        # Rough estimate: 1 token per 4 characters
        return len(text) // 4

    def get_model_info(self) -> Dict[str, Any]:
        """
        Get information about the model.

        Returns:
            Dictionary with model information
        """
        return {
            "provider": self.config.provider_type,
            "model": self.config.model,
            "privacy": self.config.privacy_level.value,
            "speed": self.config.speed.value,
            "context_window": self.config.context_window,
            "is_available": self.is_available(),
        }

    def __repr__(self) -> str:
        """String representation of provider."""
        return f"{self.__class__.__name__}(model={self.config.model})"