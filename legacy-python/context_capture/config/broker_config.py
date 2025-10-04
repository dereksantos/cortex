"""
Configuration system for the Context Broker.

Provides settings management, validation, and easy configuration
for all broker components and behavior.
"""

import json
import logging
import os
from dataclasses import dataclass, asdict
from pathlib import Path
from typing import Any, Dict, List, Optional, Union

from context_capture.providers.base import PrivacyLevel

logger = logging.getLogger(__name__)


@dataclass
class BrokerSettings:
    """Settings for the Context Broker behavior."""

    # Core settings
    knowledge_base_path: str = ".context"
    privacy_level: str = "local"
    max_context_tokens: int = 2000
    max_search_results: int = 10
    similarity_threshold: float = 0.7

    # Performance settings
    cache_ttl_seconds: int = 300  # 5 minutes
    index_update_interval_seconds: int = 3600  # 1 hour
    search_timeout_seconds: int = 30
    generation_timeout_seconds: int = 30

    # Search engine settings
    enable_semantic_search: bool = True
    enable_text_preprocessing: bool = True
    max_file_size_mb: int = 10
    supported_file_types: List[str] = None

    # Context injection settings
    include_metadata: bool = True
    include_summary: bool = True
    context_template: str = "default"
    minimal_injection: bool = False

    # Provider preferences
    prefer_local_providers: bool = True
    fallback_to_cloud: bool = True
    max_cost_per_request: float = 0.01

    def __post_init__(self):
        """Initialize default values after creation."""
        if self.supported_file_types is None:
            self.supported_file_types = [".md", ".txt", ".json", ".py", ".js", ".go", ".rs"]

    def to_dict(self) -> Dict[str, Any]:
        """Convert settings to dictionary."""
        return asdict(self)

    @classmethod
    def from_dict(cls, data: Dict[str, Any]) -> "BrokerSettings":
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

        # Validate privacy level
        try:
            PrivacyLevel(self.privacy_level.lower())
        except ValueError:
            errors.append(f"Invalid privacy level: {self.privacy_level}")

        # Validate numeric ranges
        if self.max_context_tokens <= 0:
            errors.append("max_context_tokens must be positive")

        if self.max_search_results <= 0:
            errors.append("max_search_results must be positive")

        if not 0.0 <= self.similarity_threshold <= 1.0:
            errors.append("similarity_threshold must be between 0.0 and 1.0")

        if self.cache_ttl_seconds <= 0:
            errors.append("cache_ttl_seconds must be positive")

        if self.max_file_size_mb <= 0:
            errors.append("max_file_size_mb must be positive")

        if self.max_cost_per_request < 0:
            errors.append("max_cost_per_request must be non-negative")

        # Validate paths
        try:
            Path(self.knowledge_base_path)
        except Exception:
            errors.append(f"Invalid knowledge_base_path: {self.knowledge_base_path}")

        return errors


class BrokerConfig:
    """
    Configuration manager for the Context Broker.

    Handles loading, saving, and managing broker configuration from
    files, environment variables, and programmatic settings.
    """

    DEFAULT_CONFIG_FILENAME = "broker_config.json"
    ENV_PREFIX = "CONTEXT_BROKER_"

    def __init__(
        self,
        config_path: Optional[Union[str, Path]] = None,
        auto_save: bool = True
    ):
        """
        Initialize broker configuration.

        Args:
            config_path: Path to configuration file
            auto_save: Whether to auto-save changes
        """
        self.auto_save = auto_save
        self.config_path = self._resolve_config_path(config_path)

        # Load initial settings
        self.settings = self._load_settings()

        logger.debug(f"Broker config initialized from: {self.config_path}")

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

    def _load_settings(self) -> BrokerSettings:
        """Load settings from file and environment."""
        # Start with defaults
        settings_dict = {}

        # Load from file if it exists
        if self.config_path.exists():
            try:
                with open(self.config_path, 'r', encoding='utf-8') as f:
                    file_settings = json.load(f)
                settings_dict.update(file_settings)
                logger.debug(f"Loaded config from file: {self.config_path}")
            except Exception as e:
                logger.warning(f"Error loading config file {self.config_path}: {e}")

        # Override with environment variables
        env_settings = self._load_from_environment()
        settings_dict.update(env_settings)

        # Create settings object
        return BrokerSettings.from_dict(settings_dict)

    def _load_from_environment(self) -> Dict[str, Any]:
        """Load settings from environment variables."""
        env_settings = {}

        # Map of environment variable names to setting names
        env_mapping = {
            f"{self.ENV_PREFIX}KNOWLEDGE_BASE_PATH": "knowledge_base_path",
            f"{self.ENV_PREFIX}PRIVACY_LEVEL": "privacy_level",
            f"{self.ENV_PREFIX}MAX_CONTEXT_TOKENS": ("max_context_tokens", int),
            f"{self.ENV_PREFIX}MAX_SEARCH_RESULTS": ("max_search_results", int),
            f"{self.ENV_PREFIX}SIMILARITY_THRESHOLD": ("similarity_threshold", float),
            f"{self.ENV_PREFIX}CACHE_TTL": ("cache_ttl_seconds", int),
            f"{self.ENV_PREFIX}PREFER_LOCAL": ("prefer_local_providers", bool),
            f"{self.ENV_PREFIX}MAX_COST": ("max_cost_per_request", float),
        }

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
            logger.debug(f"Loaded {len(env_settings)} settings from environment")

        return env_settings

    def save(self, settings: Optional[BrokerSettings] = None) -> bool:
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
                logger.error(f"Cannot save invalid settings: {errors}")
                return False

            # Ensure directory exists
            self.config_path.parent.mkdir(parents=True, exist_ok=True)

            # Save to file
            with open(self.config_path, 'w', encoding='utf-8') as f:
                json.dump(settings_to_save.to_dict(), f, indent=2)

            logger.info(f"Saved broker config to: {self.config_path}")
            return True

        except Exception as e:
            logger.error(f"Error saving config: {e}")
            return False

    def reload(self) -> bool:
        """
        Reload settings from file and environment.

        Returns:
            True if reloaded successfully
        """
        try:
            self.settings = self._load_settings()
            logger.info("Broker config reloaded")
            return True
        except Exception as e:
            logger.error(f"Error reloading config: {e}")
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
                logger.error(f"Unknown setting: {key}")
                return False

            # Update setting
            setattr(self.settings, key, value)

            # Validate
            errors = self.settings.validate()
            if errors:
                logger.error(f"Invalid setting value: {errors}")
                return False

            # Auto-save if enabled
            if self.auto_save:
                return self.save()

            return True

        except Exception as e:
            logger.error(f"Error updating setting {key}: {e}")
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

    def reset_to_defaults(self) -> None:
        """Reset all settings to defaults."""
        self.settings = BrokerSettings()
        if self.auto_save:
            self.save()
        logger.info("Reset broker config to defaults")

    def export_config(self, output_path: Union[str, Path]) -> bool:
        """
        Export configuration to a file.

        Args:
            output_path: Path to export file

        Returns:
            True if exported successfully
        """
        try:
            output_path = Path(output_path)
            output_path.parent.mkdir(parents=True, exist_ok=True)

            with open(output_path, 'w', encoding='utf-8') as f:
                json.dump(self.settings.to_dict(), f, indent=2)

            logger.info(f"Exported config to: {output_path}")
            return True

        except Exception as e:
            logger.error(f"Error exporting config: {e}")
            return False

    def import_config(self, input_path: Union[str, Path]) -> bool:
        """
        Import configuration from a file.

        Args:
            input_path: Path to import file

        Returns:
            True if imported successfully
        """
        try:
            input_path = Path(input_path)
            if not input_path.exists():
                logger.error(f"Config file not found: {input_path}")
                return False

            with open(input_path, 'r', encoding='utf-8') as f:
                settings_dict = json.load(f)

            # Validate imported settings
            new_settings = BrokerSettings.from_dict(settings_dict)
            errors = new_settings.validate()
            if errors:
                logger.error(f"Invalid imported config: {errors}")
                return False

            # Apply new settings
            self.settings = new_settings

            if self.auto_save:
                self.save()

            logger.info(f"Imported config from: {input_path}")
            return True

        except Exception as e:
            logger.error(f"Error importing config: {e}")
            return False

    def get_status(self) -> Dict[str, Any]:
        """Get configuration status and information."""
        errors = self.settings.validate()

        return {
            "config_path": str(self.config_path),
            "config_exists": self.config_path.exists(),
            "auto_save": self.auto_save,
            "valid": len(errors) == 0,
            "validation_errors": errors,
            "settings": self.settings.to_dict(),
            "env_prefix": self.ENV_PREFIX
        }

    @classmethod
    def create_default_config(
        cls,
        config_path: Optional[Union[str, Path]] = None,
        **kwargs
    ) -> "BrokerConfig":
        """
        Create a default configuration file.

        Args:
            config_path: Path for config file
            **kwargs: Override default settings

        Returns:
            BrokerConfig instance
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

        logger.info(f"Created default config at: {config.config_path}")
        return config

    def __repr__(self) -> str:
        """String representation of config."""
        return f"BrokerConfig(path={self.config_path}, auto_save={self.auto_save})"