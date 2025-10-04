"""
Ollama provider for local LLM models.
"""

import time
from typing import Any, Dict, Optional

import requests

from context_capture.providers.base import (
    ModelProvider,
    ModelSpeed,
    PrivacyLevel,
    ProviderConfig,
)


class OllamaProvider(ModelProvider):
    """Provider for Ollama local models."""

    # Model configurations with performance characteristics
    MODEL_CONFIGS = {
        "phi-2": {
            "size": "1.7GB",
            "speed": ModelSpeed.VERY_FAST,
            "context_window": 2048,
        },
        "phi3:mini": {
            "size": "2.2GB",
            "speed": ModelSpeed.VERY_FAST,
            "context_window": 4096,
        },
        "gemma:2b": {
            "size": "1.4GB",
            "speed": ModelSpeed.VERY_FAST,
            "context_window": 8192,
        },
        "mistral:7b": {
            "size": "4.1GB",
            "speed": ModelSpeed.FAST,
            "context_window": 8192,
        },
        "codellama:7b": {
            "size": "3.8GB",
            "speed": ModelSpeed.FAST,
            "context_window": 16384,
        },
        "llama3:8b": {
            "size": "4.7GB",
            "speed": ModelSpeed.MEDIUM,
            "context_window": 8192,
        },
        "mixtral:8x7b": {
            "size": "26GB",
            "speed": ModelSpeed.SLOW,
            "context_window": 32768,
        },
    }

    def __init__(self, model: str = "mistral:7b", base_url: str = "http://localhost:11434"):
        """
        Initialize Ollama provider.

        Args:
            model: Model name
            base_url: Ollama service URL
        """
        # Get model configuration
        model_info = self.MODEL_CONFIGS.get(model, {
            "speed": ModelSpeed.MEDIUM,
            "context_window": 4096,
        })

        config = ProviderConfig(
            name=f"ollama/{model}",
            model=model,
            provider_type="ollama",
            privacy_level=PrivacyLevel.LOCAL,
            speed=model_info["speed"],
            context_window=model_info["context_window"],
            base_url=base_url.rstrip('/'),
            cost_per_1k_input=0.0,  # Local models are free
            cost_per_1k_output=0.0,
        )

        super().__init__(config)
        self._available_models: Optional[list] = None
        self._last_model_check: float = 0

    def generate(self, prompt: str, **kwargs) -> Optional[str]:
        """
        Generate text using Ollama model.

        Args:
            prompt: Input prompt
            **kwargs: Additional generation parameters

        Returns:
            Generated text or None if failed
        """
        try:
            # Prepare request parameters
            params = {
                "model": self.config.model,
                "prompt": prompt,
                "stream": False,
                "options": {
                    "temperature": kwargs.get("temperature", self.config.temperature),
                    "num_predict": kwargs.get("max_tokens", self.config.max_tokens),
                }
            }

            # Add any additional options
            if "top_p" in kwargs:
                params["options"]["top_p"] = kwargs["top_p"]
            if "seed" in kwargs:
                params["options"]["seed"] = kwargs["seed"]

            # Make request to Ollama
            response = requests.post(
                f"{self.config.base_url}/api/generate",
                json=params,
                timeout=kwargs.get("timeout", 30)
            )

            if response.status_code == 200:
                result = response.json()
                return result.get("response", "")

            return None

        except requests.exceptions.Timeout:
            return None
        except Exception as e:
            if kwargs.get("debug", False):
                print(f"Ollama generation error: {e}")
            return None

    def is_available(self) -> bool:
        """
        Check if Ollama service and model are available.

        Returns:
            True if both service and model are available
        """
        # Check if we've recently verified availability
        current_time = time.time()
        if self._is_available is not None and (current_time - self._last_check_time) < 60:
            return self._is_available

        try:
            # Check if Ollama service is running
            response = requests.get(
                f"{self.config.base_url}/api/version",
                timeout=5
            )

            if response.status_code != 200:
                self._is_available = False
                self._last_check_time = current_time
                return False

            # Check if specific model is available
            if self._check_model_available():
                self._is_available = True
            else:
                self._is_available = False

            self._last_check_time = current_time
            return self._is_available

        except Exception:
            self._is_available = False
            self._last_check_time = current_time
            return False

    def validate_config(self) -> bool:
        """
        Validate Ollama configuration.

        Returns:
            True if configuration is valid
        """
        # Check if base URL is valid
        if not self.config.base_url:
            return False

        # Check if model name is provided
        if not self.config.model:
            return False

        # Optionally check if model is in known configurations
        # (but allow unknown models too)
        return True

    def _check_model_available(self) -> bool:
        """
        Check if specific model is available in Ollama.

        Returns:
            True if model is available
        """
        try:
            # Get list of available models
            response = requests.get(
                f"{self.config.base_url}/api/tags",
                timeout=10
            )

            if response.status_code == 200:
                models_info = response.json()
                available_models = [
                    m["name"] for m in models_info.get("models", [])
                ]
                self._available_models = available_models
                return self.config.model in available_models

            return False

        except Exception:
            return False

    def list_models(self) -> Optional[Dict[str, Any]]:
        """
        List all available Ollama models.

        Returns:
            Dictionary with model information or None if failed
        """
        try:
            response = requests.get(
                f"{self.config.base_url}/api/tags",
                timeout=10
            )

            if response.status_code == 200:
                return response.json()

            return None

        except Exception:
            return None

    def pull_model(self, model_name: Optional[str] = None) -> bool:
        """
        Pull (download) a model from Ollama.

        Args:
            model_name: Model to pull (uses configured model if None)

        Returns:
            True if successful
        """
        model = model_name or self.config.model

        try:
            response = requests.post(
                f"{self.config.base_url}/api/pull",
                json={"name": model},
                timeout=None  # Downloads can take a while
            )

            return response.status_code == 200

        except Exception:
            return False

    def get_model_info(self) -> Dict[str, Any]:
        """
        Get detailed information about the model.

        Returns:
            Dictionary with model information
        """
        info = super().get_model_info()

        # Add Ollama-specific information
        if self.config.model in self.MODEL_CONFIGS:
            info["size"] = self.MODEL_CONFIGS[self.config.model].get("size", "unknown")

        if self._available_models:
            info["available_models"] = self._available_models

        info["base_url"] = self.config.base_url

        return info