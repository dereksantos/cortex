"""
Configuration system for model providers.

Manages provider settings, API keys, model preferences,
and routing configuration for the provider system.
"""

import json
import logging
import os
from dataclasses import dataclass, asdict, field
from pathlib import Path
from typing import Any, Dict, List, Optional, Union

logger = logging.getLogger(__name__)


@dataclass
class ProviderSettings:
    """Settings for model providers and routing."""

    # Provider preferences
    prefer_local: bool = True
    fallback_to_cloud: bool = True
    max_cost_per_request: float = 0.01

    # Ollama settings
    ollama_enabled: bool = True
    ollama_base_url: str = "http://localhost:11434"
    ollama_default_model: str = "mistral:7b"
    ollama_timeout: int = 30

    # Anthropic settings
    anthropic_enabled: bool = False
    anthropic_api_key: Optional[str] = None
    anthropic_default_model: str = "haiku-3.5"
    anthropic_timeout: int = 30

    # Task-specific model preferences
    broker_model_preference: str = "local"  # local, cloud, or specific model
    capture_model_preference: str = "local"
    analysis_model_preference: str = "cloud"

    # Provider routing settings
    health_check_interval: int = 300  # 5 minutes
    retry_attempts: int = 3
    retry_delay: float = 1.0

    # Model-specific overrides
    model_overrides: Dict[str, Dict[str, Any]] = field(default_factory=dict)

    def to_dict(self) -> Dict[str, Any]:
        """Convert settings to dictionary."""
        return asdict(self)

    @classmethod
    def from_dict(cls, data: Dict[str, Any]) -> "ProviderSettings":
        """Create settings from dictionary."""
        # Filter out unknown keys
        try:
            # Use __dataclass_fields__ (note the different attribute name)
            if hasattr(cls, '__dataclass_fields__'):
                valid_keys = set(cls.__dataclass_fields__.keys())
            else:
                # Fallback: get all non-method attributes
                valid_keys = {attr for attr in dir(cls) if not attr.startswith('_') and not callable(getattr(cls, attr))}
            filtered_data = {k: v for k, v in data.items() if k in valid_keys}
        except AttributeError:
            # If dataclass introspection fails, just use the data as-is
            filtered_data = data
        return cls(**filtered_data)

    def validate(self) -> List[str]:
        """Validate settings and return list of errors."""
        errors = []

        # Validate numeric ranges
        if self.max_cost_per_request < 0:
            errors.append("max_cost_per_request must be non-negative")

        if self.ollama_timeout <= 0:
            errors.append("ollama_timeout must be positive")

        if self.anthropic_timeout <= 0:
            errors.append("anthropic_timeout must be positive")

        if self.health_check_interval <= 0:
            errors.append("health_check_interval must be positive")

        if self.retry_attempts < 0:
            errors.append("retry_attempts must be non-negative")

        if self.retry_delay < 0:
            errors.append("retry_delay must be non-negative")

        # Validate URLs
        if not self.ollama_base_url.startswith(('http://', 'https://')):
            errors.append("ollama_base_url must be a valid HTTP/HTTPS URL")

        # Validate model preferences
        valid_preferences = ["local", "cloud", "auto"]
        for pref_name in ["broker_model_preference", "capture_model_preference", "analysis_model_preference"]:
            pref_value = getattr(self, pref_name)
            if pref_value not in valid_preferences and not pref_value.startswith(("ollama/", "anthropic/")):
                errors.append(f"{pref_name} must be one of {valid_preferences} or a specific model")

        # Validate that at least one provider is enabled
        if not self.ollama_enabled and not self.anthropic_enabled:
            errors.append("At least one provider must be enabled")

        return errors

    def get_task_model(self, task_type: str) -> str:
        """Get preferred model for a task type."""
        task_mapping = {
            "broker": self.broker_model_preference,
            "capture": self.capture_model_preference,
            "analysis": self.analysis_model_preference
        }
        return task_mapping.get(task_type, "auto")


class ProviderConfig:
    """
    Configuration manager for model providers.

    Handles provider settings, API keys, and routing configuration
    with secure storage and environment variable support.
    """

    DEFAULT_CONFIG_FILENAME = "provider_config.json"
    ENV_PREFIX = "CONTEXT_PROVIDER_"

    def __init__(
        self,
        config_path: Optional[Union[str, Path]] = None,
        auto_save: bool = True
    ):
        """
        Initialize provider configuration.

        Args:
            config_path: Path to configuration file
            auto_save: Whether to auto-save changes
        """
        self.auto_save = auto_save
        self.config_path = self._resolve_config_path(config_path)

        # Load initial settings
        self.settings = self._load_settings()

        logger.debug(f"Provider config initialized from: {self.config_path}")

    def _resolve_config_path(self, config_path: Optional[Union[str, Path]]) -> Path:
        """Resolve the configuration file path."""
        if config_path:
            return Path(config_path)

        # Try environment variable
        env_path = os.environ.get(f"{self.ENV_PREFIX}CONFIG_PATH")
        if env_path:
            return Path(env_path)

        # Try current directory
        current_dir_config = Path(self.DEFAULT_CONFIG_FILENAME)
        if current_dir_config.exists():
            return current_dir_config

        # Try .context directory
        context_dir_config = Path(".context") / self.DEFAULT_CONFIG_FILENAME
        if context_dir_config.exists():
            return context_dir_config

        # Default to .context directory (create if needed)
        context_dir = Path(".context")
        context_dir.mkdir(exist_ok=True)
        return context_dir / self.DEFAULT_CONFIG_FILENAME

    def _load_settings(self) -> ProviderSettings:
        """Load settings from file and environment."""
        # Start with defaults
        settings_dict = {}

        # Load from file if it exists
        if self.config_path.exists():
            try:
                with open(self.config_path, 'r', encoding='utf-8') as f:
                    file_settings = json.load(f)
                settings_dict.update(file_settings)
                logger.debug(f"Loaded provider config from file: {self.config_path}")
            except Exception as e:
                logger.warning(f"Error loading provider config file {self.config_path}: {e}")

        # Override with environment variables
        env_settings = self._load_from_environment()
        settings_dict.update(env_settings)

        # Create settings object
        return ProviderSettings.from_dict(settings_dict)

    def _load_from_environment(self) -> Dict[str, Any]:
        """Load settings from environment variables."""
        env_settings = {}

        # Map of environment variable names to setting names
        env_mapping = {
            # General settings
            f"{self.ENV_PREFIX}PREFER_LOCAL": ("prefer_local", bool),
            f"{self.ENV_PREFIX}FALLBACK_TO_CLOUD": ("fallback_to_cloud", bool),
            f"{self.ENV_PREFIX}MAX_COST": ("max_cost_per_request", float),

            # Ollama settings
            f"{self.ENV_PREFIX}OLLAMA_ENABLED": ("ollama_enabled", bool),
            f"{self.ENV_PREFIX}OLLAMA_URL": "ollama_base_url",
            f"{self.ENV_PREFIX}OLLAMA_MODEL": "ollama_default_model",
            f"{self.ENV_PREFIX}OLLAMA_TIMEOUT": ("ollama_timeout", int),

            # Anthropic settings
            f"{self.ENV_PREFIX}ANTHROPIC_ENABLED": ("anthropic_enabled", bool),
            f"{self.ENV_PREFIX}ANTHROPIC_API_KEY": "anthropic_api_key",
            f"{self.ENV_PREFIX}ANTHROPIC_MODEL": "anthropic_default_model",
            f"{self.ENV_PREFIX}ANTHROPIC_TIMEOUT": ("anthropic_timeout", int),

            # Task preferences
            f"{self.ENV_PREFIX}BROKER_MODEL": "broker_model_preference",
            f"{self.ENV_PREFIX}CAPTURE_MODEL": "capture_model_preference",
            f"{self.ENV_PREFIX}ANALYSIS_MODEL": "analysis_model_preference",
        }

        # Also check for ANTHROPIC_API_KEY (standard environment variable)
        if not env_settings.get("anthropic_api_key"):
            anthropic_key = os.environ.get("ANTHROPIC_API_KEY")
            if anthropic_key:
                env_settings["anthropic_api_key"] = anthropic_key

        for env_var, setting_info in env_mapping.items():
            env_value = os.environ.get(env_var)
            if env_value is None:
                continue

            if isinstance(setting_info, tuple):
                setting_name, setting_type = setting_info
                try:
                    if setting_type == bool:
                        env_settings[setting_name] = env_value.lower() in ('true', '1', 'yes', 'on')
                    else:
                        env_settings[setting_name] = setting_type(env_value)
                except ValueError as e:
                    logger.warning(f"Invalid value for {env_var}: {env_value} ({e})")
            else:
                env_settings[setting_info] = env_value

        if env_settings:
            logger.debug(f"Loaded {len(env_settings)} provider settings from environment")

        return env_settings

    def save(self, settings: Optional[ProviderSettings] = None) -> bool:
        """
        Save settings to configuration file.

        Args:
            settings: Settings to save (uses current if None)

        Returns:
            True if saved successfully
        """
        try:
            settings_to_save = settings or self.settings

            # Validate before saving
            errors = settings_to_save.validate()
            if errors:
                logger.error(f"Cannot save invalid provider settings: {errors}")
                return False

            # Ensure directory exists
            self.config_path.parent.mkdir(parents=True, exist_ok=True)

            # Prepare settings for saving (sanitize sensitive data if needed)
            settings_dict = settings_to_save.to_dict()

            # Save to file
            with open(self.config_path, 'w', encoding='utf-8') as f:
                json.dump(settings_dict, f, indent=2)

            logger.info(f"Saved provider config to: {self.config_path}")
            return True

        except Exception as e:
            logger.error(f"Error saving provider config: {e}")
            return False

    def reload(self) -> bool:
        """
        Reload settings from file and environment.

        Returns:
            True if reloaded successfully
        """
        try:
            self.settings = self._load_settings()
            logger.info("Provider config reloaded")
            return True
        except Exception as e:
            logger.error(f"Error reloading provider config: {e}")
            return False

    def update_setting(self, key: str, value: Any) -> bool:
        """
        Update a single setting.

        Args:
            key: Setting name
            value: New value

        Returns:
            True if updated successfully
        """
        try:
            # Check if setting exists
            if not hasattr(self.settings, key):
                logger.error(f"Unknown provider setting: {key}")
                return False

            # Update setting
            setattr(self.settings, key, value)

            # Validate
            errors = self.settings.validate()
            if errors:
                logger.error(f"Invalid provider setting value: {errors}")
                return False

            # Auto-save if enabled
            if self.auto_save:
                return self.save()

            return True

        except Exception as e:
            logger.error(f"Error updating provider setting {key}: {e}")
            return False

    def get_setting(self, key: str, default: Any = None) -> Any:
        """
        Get a setting value.

        Args:
            key: Setting name
            default: Default value if setting doesn't exist

        Returns:
            Setting value
        """
        return getattr(self.settings, key, default)

    def set_api_key(self, provider: str, api_key: str) -> bool:
        """
        Set API key for a provider.

        Args:
            provider: Provider name (anthropic, etc.)
            api_key: API key

        Returns:
            True if set successfully
        """
        if provider.lower() == "anthropic":
            return self.update_setting("anthropic_api_key", api_key)
        else:
            logger.error(f"Unknown provider for API key: {provider}")
            return False

    def get_api_key(self, provider: str) -> Optional[str]:
        """
        Get API key for a provider.

        Args:
            provider: Provider name

        Returns:
            API key or None
        """
        if provider.lower() == "anthropic":
            return self.settings.anthropic_api_key
        else:
            return None

    def enable_provider(self, provider: str, enabled: bool = True) -> bool:
        """
        Enable or disable a provider.

        Args:
            provider: Provider name
            enabled: Whether to enable

        Returns:
            True if updated successfully
        """
        if provider.lower() == "ollama":
            return self.update_setting("ollama_enabled", enabled)
        elif provider.lower() == "anthropic":
            return self.update_setting("anthropic_enabled", enabled)
        else:
            logger.error(f"Unknown provider: {provider}")
            return False

    def is_provider_enabled(self, provider: str) -> bool:
        """
        Check if a provider is enabled.

        Args:
            provider: Provider name

        Returns:
            True if enabled
        """
        if provider.lower() == "ollama":
            return self.settings.ollama_enabled
        elif provider.lower() == "anthropic":
            return self.settings.anthropic_enabled
        else:
            return False

    def get_provider_settings(self, provider: str) -> Dict[str, Any]:
        """
        Get all settings for a specific provider.

        Args:
            provider: Provider name

        Returns:
            Dictionary of provider settings
        """
        if provider.lower() == "ollama":
            return {
                "enabled": self.settings.ollama_enabled,
                "base_url": self.settings.ollama_base_url,
                "default_model": self.settings.ollama_default_model,
                "timeout": self.settings.ollama_timeout
            }
        elif provider.lower() == "anthropic":
            return {
                "enabled": self.settings.anthropic_enabled,
                "api_key": self.settings.anthropic_api_key,
                "default_model": self.settings.anthropic_default_model,
                "timeout": self.settings.anthropic_timeout
            }
        else:
            return {}

    def get_task_model_preference(self, task_type: str) -> str:
        """
        Get model preference for a task type.

        Args:
            task_type: Task type (broker, capture, analysis)

        Returns:
            Model preference
        """
        return self.settings.get_task_model(task_type)

    def set_task_model_preference(self, task_type: str, preference: str) -> bool:
        """
        Set model preference for a task type.

        Args:
            task_type: Task type
            preference: Model preference

        Returns:
            True if set successfully
        """
        pref_mapping = {
            "broker": "broker_model_preference",
            "capture": "capture_model_preference",
            "analysis": "analysis_model_preference"
        }

        setting_name = pref_mapping.get(task_type)
        if setting_name:
            return self.update_setting(setting_name, preference)
        else:
            logger.error(f"Unknown task type: {task_type}")
            return False

    def get_status(self) -> Dict[str, Any]:
        """Get provider configuration status and information."""
        errors = self.settings.validate()

        status = {
            "config_path": str(self.config_path),
            "config_exists": self.config_path.exists(),
            "auto_save": self.auto_save,
            "valid": len(errors) == 0,
            "validation_errors": errors,
            "env_prefix": self.ENV_PREFIX,
            "providers": {}
        }

        # Provider status
        for provider in ["ollama", "anthropic"]:
            provider_settings = self.get_provider_settings(provider)
            status["providers"][provider] = {
                "enabled": provider_settings.get("enabled", False),
                "configured": self._is_provider_configured(provider),
                "settings": provider_settings
            }

        return status

    def _is_provider_configured(self, provider: str) -> bool:
        """Check if a provider is properly configured."""
        if provider.lower() == "ollama":
            return self.settings.ollama_enabled and bool(self.settings.ollama_base_url)
        elif provider.lower() == "anthropic":
            return self.settings.anthropic_enabled and bool(self.settings.anthropic_api_key)
        else:
            return False

    @classmethod
    def create_default_config(
        cls,
        config_path: Optional[Union[str, Path]] = None,
        **kwargs
    ) -> "ProviderConfig":
        """
        Create a default provider configuration file.

        Args:
            config_path: Path for config file
            **kwargs: Override default settings

        Returns:
            ProviderConfig instance
        """
        # Create config instance
        config = cls(config_path=config_path, auto_save=False)

        # Apply any overrides
        for key, value in kwargs.items():
            if hasattr(config.settings, key):
                setattr(config.settings, key, value)

        # Save the default config
        config.save()

        # Re-enable auto-save
        config.auto_save = True

        logger.info(f"Created default provider config at: {config.config_path}")
        return config

    def __repr__(self) -> str:
        """String representation of config."""
        return f"ProviderConfig(path={self.config_path}, auto_save={self.auto_save})"