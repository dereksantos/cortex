"""
Configuration management for agentic context capture system.
"""

import json
import os
from pathlib import Path
from typing import Any, Dict, Optional, Union

import yaml


class Config:
    """Configuration manager with support for YAML and JSON files."""

    DEFAULT_CONFIG = {
        # Model Configuration
        'model': {
            'provider': 'ollama',
            'name': 'mistral:7b',
            'temperature': 0.3,
            'max_tokens': 500,
            'timeout': 30,
        },

        # Capture Settings
        'capture': {
            'importance_threshold': 0.5,
            'categories': ['decisions', 'patterns', 'insights', 'strategies'],
            'skip_patterns': [
                'node_modules', '.git', '__pycache__', '.cache', 'dist/', 'build/',
                'Reading file', 'Checking directory', 'ls', 'pwd'
            ],
            'truncate_length': 2000,
        },

        # Storage Settings
        'storage': {
            'retention_days': 30,
            'max_queue_size_mb': 100,
            'compress_after_days': 7,
            'base_dir': None,  # Will be set dynamically
        },

        # Performance Settings
        'performance': {
            'batch_size': 5,
            'process_interval': 2,
            'max_processing_time': 10,
            'cleanup_interval': 3600,
        },

        # Features
        'features': {
            'fallback_enabled': True,
            'debug_mode': False,
            'autonomous_reflection': False,
            'cross_repo_context': False,
        }
    }

    def __init__(self, config_path: Optional[Union[str, Path]] = None,
                 project_dir: Optional[Union[str, Path]] = None):
        """
        Initialize configuration.

        Args:
            config_path: Path to configuration file (YAML or JSON)
            project_dir: Project directory for relative path resolution
        """
        self.project_dir = Path(project_dir) if project_dir else Path.cwd()
        self.config_path = self._resolve_config_path(config_path)
        self._config = self._load_config()

    def _resolve_config_path(self, config_path: Optional[Union[str, Path]]) -> Optional[Path]:
        """Resolve configuration file path."""
        if config_path:
            return Path(config_path)

        # Look for config files in standard locations
        search_paths = [
            self.project_dir / '.context-capture' / 'config.yaml',
            self.project_dir / '.context-capture' / 'config.yml',
            self.project_dir / '.context-capture' / 'config.json',
            self.project_dir / 'context-capture.yaml',
            self.project_dir / 'context-capture.yml',
            self.project_dir / 'context-capture.json',
            Path.home() / '.context-capture' / 'config.yaml',
        ]

        for path in search_paths:
            if path.exists():
                return path

        return None

    def _load_config(self) -> Dict[str, Any]:
        """Load configuration from file or use defaults."""
        config = self.DEFAULT_CONFIG.copy()

        if self.config_path and self.config_path.exists():
            try:
                with open(self.config_path, 'r') as f:
                    if self.config_path.suffix.lower() in ['.yaml', '.yml']:
                        user_config = yaml.safe_load(f)
                    else:
                        user_config = json.load(f)

                if user_config:
                    config = self._deep_merge(config, user_config)

            except Exception as e:
                print(f"Warning: Failed to load config from {self.config_path}: {e}")

        # Set dynamic defaults
        if not config['storage']['base_dir']:
            config['storage']['base_dir'] = str(self.project_dir / '.context')

        return config

    def _deep_merge(self, base: Dict[str, Any], override: Dict[str, Any]) -> Dict[str, Any]:
        """Deep merge two dictionaries."""
        result = base.copy()

        for key, value in override.items():
            if key in result and isinstance(result[key], dict) and isinstance(value, dict):
                result[key] = self._deep_merge(result[key], value)
            else:
                result[key] = value

        return result

    def get(self, key: str, default: Any = None) -> Any:
        """Get configuration value using dot notation."""
        keys = key.split('.')
        value = self._config

        for k in keys:
            if isinstance(value, dict) and k in value:
                value = value[k]
            else:
                return default

        return value

    def set(self, key: str, value: Any) -> None:
        """Set configuration value using dot notation."""
        keys = key.split('.')
        config = self._config

        for k in keys[:-1]:
            if k not in config:
                config[k] = {}
            config = config[k]

        config[keys[-1]] = value

    def save(self, path: Optional[Union[str, Path]] = None) -> None:
        """Save current configuration to file."""
        save_path = Path(path) if path else self.config_path

        if not save_path:
            save_path = self.project_dir / '.context-capture' / 'config.yaml'

        save_path.parent.mkdir(parents=True, exist_ok=True)

        with open(save_path, 'w') as f:
            if save_path.suffix.lower() in ['.yaml', '.yml']:
                yaml.safe_dump(self._config, f, default_flow_style=False, indent=2)
            else:
                json.dump(self._config, f, indent=2)

    def to_dict(self) -> Dict[str, Any]:
        """Return configuration as dictionary."""
        return self._config.copy()

    @property
    def base_dir(self) -> Path:
        """Get base directory for context storage."""
        return Path(self.get('storage.base_dir'))

    @property
    def queue_dir(self) -> Path:
        """Get queue directory."""
        return self.base_dir / 'queue'

    @property
    def knowledge_dir(self) -> Path:
        """Get knowledge directory."""
        return self.base_dir / 'knowledge'

    @property
    def logs_dir(self) -> Path:
        """Get logs directory."""
        return self.base_dir / 'logs'

    def ensure_directories(self) -> None:
        """Ensure all required directories exist."""
        directories = [
            self.queue_dir / 'pending',
            self.queue_dir / 'processing',
            self.queue_dir / 'processed',
            self.knowledge_dir / 'decisions',
            self.knowledge_dir / 'patterns',
            self.knowledge_dir / 'insights',
            self.knowledge_dir / 'strategies',
            self.knowledge_dir / 'daily',
            self.logs_dir,
        ]

        for directory in directories:
            directory.mkdir(parents=True, exist_ok=True)