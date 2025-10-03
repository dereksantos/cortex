"""
Anthropic provider for Claude models (Haiku, Sonnet, Opus).
"""

import os
import time
from typing import Any, Dict, Optional

import requests

from context_capture.providers.base import (
    ModelProvider,
    ModelSpeed,
    PrivacyLevel,
    ProviderConfig,
)


class AnthropicProvider(ModelProvider):
    """Provider for Anthropic's Claude models."""

    # Model configurations
    MODEL_CONFIGS = {
        "haiku-3.5": {
            "id": "claude-3-haiku-20241022",
            "speed": ModelSpeed.VERY_FAST,
            "context_window": 200000,
            "cost_per_1k_input": 0.00025,
            "cost_per_1k_output": 0.00125,
        },
        "haiku-3": {
            "id": "claude-3-haiku-20240307",
            "speed": ModelSpeed.VERY_FAST,
            "context_window": 200000,
            "cost_per_1k_input": 0.00025,
            "cost_per_1k_output": 0.00125,
        },
        "sonnet-3.5": {
            "id": "claude-3-5-sonnet-20241022",
            "speed": ModelSpeed.FAST,
            "context_window": 200000,
            "cost_per_1k_input": 0.003,
            "cost_per_1k_output": 0.015,
        },
        "sonnet-3": {
            "id": "claude-3-sonnet-20240229",
            "speed": ModelSpeed.FAST,
            "context_window": 200000,
            "cost_per_1k_input": 0.003,
            "cost_per_1k_output": 0.015,
        },
        "opus-3": {
            "id": "claude-3-opus-20240229",
            "speed": ModelSpeed.MEDIUM,
            "context_window": 200000,
            "cost_per_1k_input": 0.015,
            "cost_per_1k_output": 0.075,
        },
    }

    def __init__(self, model: str = "haiku-3.5", api_key: Optional[str] = None):
        """
        Initialize Anthropic provider.

        Args:
            model: Model name (haiku-3.5, sonnet-3.5, opus-3, etc.)
            api_key: Anthropic API key (uses env var if not provided)
        """
        # Get model configuration
        model_info = self.MODEL_CONFIGS.get(model, self.MODEL_CONFIGS["haiku-3.5"])

        # Get API key from parameter or environment
        if not api_key:
            api_key = os.environ.get("ANTHROPIC_API_KEY")

        config = ProviderConfig(
            name=f"anthropic/{model}",
            model=model_info["id"],
            provider_type="anthropic",
            privacy_level=PrivacyLevel.CLOUD,
            speed=model_info["speed"],
            context_window=model_info["context_window"],
            api_key=api_key,
            base_url="https://api.anthropic.com/v1",
            cost_per_1k_input=model_info["cost_per_1k_input"],
            cost_per_1k_output=model_info["cost_per_1k_output"],
        )

        super().__init__(config)
        self._headers = None

    def generate(self, prompt: str, **kwargs) -> Optional[str]:
        """
        Generate text using Anthropic's API.

        Args:
            prompt: Input prompt
            **kwargs: Additional generation parameters

        Returns:
            Generated text or None if failed
        """
        if not self.config.api_key:
            return None

        try:
            # Prepare headers
            if not self._headers:
                self._headers = {
                    "x-api-key": self.config.api_key,
                    "anthropic-version": "2023-06-01",
                    "content-type": "application/json",
                }

            # Convert simple prompt to messages format
            messages = kwargs.get("messages", [
                {"role": "user", "content": prompt}
            ])

            # Prepare request body
            body = {
                "model": self.config.model,
                "messages": messages,
                "max_tokens": kwargs.get("max_tokens", self.config.max_tokens),
                "temperature": kwargs.get("temperature", self.config.temperature),
            }

            # Add optional parameters
            if "system" in kwargs:
                body["system"] = kwargs["system"]
            if "stop_sequences" in kwargs:
                body["stop_sequences"] = kwargs["stop_sequences"]
            if "top_p" in kwargs:
                body["top_p"] = kwargs["top_p"]

            # Make request
            response = requests.post(
                f"{self.config.base_url}/messages",
                json=body,
                headers=self._headers,
                timeout=kwargs.get("timeout", 30),
            )

            if response.status_code == 200:
                result = response.json()
                # Extract text from first content block
                if result.get("content") and len(result["content"]) > 0:
                    return result["content"][0].get("text", "")

            elif response.status_code == 401:
                self._is_available = False
                if kwargs.get("debug", False):
                    print("Anthropic API key is invalid")

            elif response.status_code == 429:
                if kwargs.get("debug", False):
                    print("Rate limit exceeded")

            return None

        except requests.exceptions.Timeout:
            return None
        except Exception as e:
            if kwargs.get("debug", False):
                print(f"Anthropic generation error: {e}")
            return None

    def is_available(self) -> bool:
        """
        Check if Anthropic API is accessible with the provided key.

        Returns:
            True if API is accessible
        """
        # Check cache
        current_time = time.time()
        if self._is_available is not None and (current_time - self._last_check_time) < 300:
            return self._is_available

        # No API key means not available
        if not self.config.api_key:
            self._is_available = False
            self._last_check_time = current_time
            return False

        try:
            # Test with a minimal request
            test_response = self.generate(
                "Hi",
                max_tokens=1,
                timeout=5
            )

            self._is_available = test_response is not None
            self._last_check_time = current_time
            return self._is_available

        except Exception:
            self._is_available = False
            self._last_check_time = current_time
            return False

    def validate_config(self) -> bool:
        """
        Validate Anthropic configuration.

        Returns:
            True if configuration is valid
        """
        # Must have API key
        if not self.config.api_key:
            return False

        # Model must be valid
        if not self.config.model:
            return False

        return True

    def estimate_cost(self, input_text: str, expected_output_tokens: int = 200) -> float:
        """
        Estimate cost for a request.

        Args:
            input_text: Input text
            expected_output_tokens: Expected output length in tokens

        Returns:
            Estimated cost in USD
        """
        input_tokens = self.estimate_tokens(input_text)
        return self.config.estimate_cost(input_tokens, expected_output_tokens)

    def get_model_info(self) -> Dict[str, Any]:
        """
        Get detailed information about the model.

        Returns:
            Dictionary with model information
        """
        info = super().get_model_info()

        # Add pricing information
        info["pricing"] = {
            "input_per_1k": self.config.cost_per_1k_input,
            "output_per_1k": self.config.cost_per_1k_output,
            "currency": "USD",
        }

        # Add capabilities
        info["capabilities"] = {
            "streaming": True,
            "function_calling": True,
            "vision": self.config.model in ["haiku-3.5", "sonnet-3.5", "opus-3"],
            "max_context": self.config.context_window,
        }

        return info

    @classmethod
    def create_for_task(cls, task_type: str, api_key: Optional[str] = None):
        """
        Create an Anthropic provider optimized for a specific task.

        Args:
            task_type: Type of task (broker, capture, analysis)
            api_key: API key

        Returns:
            Configured AnthropicProvider
        """
        task_models = {
            "broker": "haiku-3.5",  # Fast and cheap for quick lookups
            "capture": "haiku-3.5",  # Good balance for event analysis
            "analysis": "sonnet-3.5",  # More intelligence for synthesis
            "deep": "opus-3",  # Maximum intelligence (expensive)
        }

        model = task_models.get(task_type, "haiku-3.5")
        return cls(model=model, api_key=api_key)