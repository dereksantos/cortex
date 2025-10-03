"""
CLI commands for configuration management.

Provides command-line interface for managing broker and provider
configuration settings, API keys, and preferences.
"""

import argparse
import json
import logging
import sys
from pathlib import Path
from typing import Any, Dict, List, Optional

from context_capture.config.broker_config import BrokerConfig, BrokerSettings
from context_capture.config.provider_config import ProviderConfig, ProviderSettings

logger = logging.getLogger(__name__)


class ConfigCLI:
    """Command-line interface for configuration management."""

    def __init__(self):
        """Initialize the config CLI."""
        self.broker_config: Optional[BrokerConfig] = None
        self.provider_config: Optional[ProviderConfig] = None
        self.setup_logging()

    def setup_logging(self, level: str = "INFO") -> None:
        """Setup logging for CLI operations."""
        log_level = getattr(logging, level.upper(), logging.INFO)
        logging.basicConfig(
            level=log_level,
            format='%(asctime)s - %(name)s - %(levelname)s - %(message)s',
            datefmt='%H:%M:%S'
        )

    def load_configs(self, config_dir: Optional[str] = None) -> None:
        """Load configuration instances."""
        base_path = Path(config_dir) if config_dir else None

        try:
            self.broker_config = BrokerConfig(
                config_path=base_path / "broker_config.json" if base_path else None
            )
            self.provider_config = ProviderConfig(
                config_path=base_path / "provider_config.json" if base_path else None
            )
        except Exception as e:
            print(f"❌ Error loading configurations: {e}")
            sys.exit(1)

    def cmd_show(self, args) -> None:
        """Show current configuration."""
        self.load_configs(args.config_dir)

        if args.format == "json":
            config_data = {
                "broker": self.broker_config.settings.to_dict(),
                "providers": self.provider_config.settings.to_dict()
            }
            print(json.dumps(config_data, indent=2))
            return

        # Show broker configuration
        print("🔧 Broker Configuration")
        print("=" * 50)
        broker_settings = self.broker_config.settings
        print(f"Knowledge Base: {broker_settings.knowledge_base_path}")
        print(f"Privacy Level: {broker_settings.privacy_level}")
        print(f"Max Context Tokens: {broker_settings.max_context_tokens}")
        print(f"Similarity Threshold: {broker_settings.similarity_threshold}")
        print(f"Cache TTL: {broker_settings.cache_ttl_seconds}s")

        # Show provider configuration
        print(f"\n🤖 Provider Configuration")
        print("=" * 50)
        provider_settings = self.provider_config.settings
        print(f"Prefer Local: {provider_settings.prefer_local}")
        print(f"Fallback to Cloud: {provider_settings.fallback_to_cloud}")
        print(f"Max Cost per Request: ${provider_settings.max_cost_per_request}")

        # Ollama settings
        print(f"\n📦 Ollama:")
        print(f"  Enabled: {provider_settings.ollama_enabled}")
        print(f"  URL: {provider_settings.ollama_base_url}")
        print(f"  Default Model: {provider_settings.ollama_default_model}")

        # Anthropic settings
        print(f"\n🧠 Anthropic:")
        print(f"  Enabled: {provider_settings.anthropic_enabled}")
        print(f"  API Key: {'***configured***' if provider_settings.anthropic_api_key else 'not set'}")
        print(f"  Default Model: {provider_settings.anthropic_default_model}")

        # Task preferences
        print(f"\n⚙️  Task Preferences:")
        print(f"  Broker: {provider_settings.broker_model_preference}")
        print(f"  Capture: {provider_settings.capture_model_preference}")
        print(f"  Analysis: {provider_settings.analysis_model_preference}")

    def cmd_set(self, args) -> None:
        """Set a configuration value."""
        self.load_configs(args.config_dir)

        config_type, setting_name = args.setting.split('.', 1)

        # Convert value to appropriate type
        converted_value = self._convert_value(args.value)

        if config_type == "broker":
            success = self.broker_config.update_setting(setting_name, converted_value)
            config_name = "broker"
        elif config_type == "provider":
            success = self.provider_config.update_setting(setting_name, converted_value)
            config_name = "provider"
        else:
            print(f"❌ Invalid configuration type: {config_type}")
            print("Valid types: broker, provider")
            return

        if success:
            print(f"✅ Updated {config_name}.{setting_name} = {converted_value}")
        else:
            print(f"❌ Failed to update {config_name}.{setting_name}")

    def _convert_value(self, value: str) -> Any:
        """Convert string value to appropriate type."""
        # Try boolean conversion
        if value.lower() in ('true', 'false'):
            return value.lower() == 'true'

        # Try integer conversion
        try:
            return int(value)
        except ValueError:
            pass

        # Try float conversion
        try:
            return float(value)
        except ValueError:
            pass

        # Return as string
        return value

    def cmd_get(self, args) -> None:
        """Get a configuration value."""
        self.load_configs(args.config_dir)

        config_type, setting_name = args.setting.split('.', 1)

        if config_type == "broker":
            value = self.broker_config.get_setting(setting_name)
            config_name = "broker"
        elif config_type == "provider":
            value = self.provider_config.get_setting(setting_name)
            config_name = "provider"
        else:
            print(f"❌ Invalid configuration type: {config_type}")
            return

        if value is not None:
            print(f"{config_name}.{setting_name} = {value}")
        else:
            print(f"❌ Setting not found: {config_name}.{setting_name}")

    def cmd_validate(self, args) -> None:
        """Validate configuration."""
        self.load_configs(args.config_dir)

        print("🔍 Validating Configuration")
        print("=" * 50)

        # Validate broker config
        broker_errors = self.broker_config.settings.validate()
        if broker_errors:
            print("❌ Broker Configuration Errors:")
            for error in broker_errors:
                print(f"  - {error}")
        else:
            print("✅ Broker configuration is valid")

        # Validate provider config
        provider_errors = self.provider_config.settings.validate()
        if provider_errors:
            print("❌ Provider Configuration Errors:")
            for error in provider_errors:
                print(f"  - {error}")
        else:
            print("✅ Provider configuration is valid")

        # Overall status
        if not broker_errors and not provider_errors:
            print("\n🎉 All configurations are valid!")
        else:
            print(f"\n💥 Found {len(broker_errors) + len(provider_errors)} configuration errors")
            sys.exit(1)

    def cmd_reset(self, args) -> None:
        """Reset configuration to defaults."""
        self.load_configs(args.config_dir)

        if not args.force:
            response = input("⚠️  This will reset all settings to defaults. Continue? (y/N): ")
            if response.lower() != 'y':
                print("❌ Reset cancelled")
                return

        if args.type in ['broker', 'all']:
            self.broker_config.reset_to_defaults()
            print("✅ Broker configuration reset to defaults")

        if args.type in ['provider', 'all']:
            self.provider_config.settings = ProviderSettings()
            self.provider_config.save()
            print("✅ Provider configuration reset to defaults")

    def cmd_export(self, args) -> None:
        """Export configuration to file."""
        self.load_configs(args.config_dir)

        try:
            export_data = {
                "broker": self.broker_config.settings.to_dict(),
                "providers": self.provider_config.settings.to_dict()
            }

            output_path = Path(args.output)
            output_path.parent.mkdir(parents=True, exist_ok=True)

            with open(output_path, 'w', encoding='utf-8') as f:
                json.dump(export_data, f, indent=2)

            print(f"✅ Configuration exported to: {output_path}")

        except Exception as e:
            print(f"❌ Export failed: {e}")
            sys.exit(1)

    def cmd_import(self, args) -> None:
        """Import configuration from file."""
        self.load_configs(args.config_dir)

        try:
            input_path = Path(args.input)
            if not input_path.exists():
                print(f"❌ Input file not found: {input_path}")
                return

            with open(input_path, 'r', encoding='utf-8') as f:
                import_data = json.load(f)

            # Import broker settings
            if "broker" in import_data:
                broker_settings = BrokerSettings.from_dict(import_data["broker"])
                errors = broker_settings.validate()
                if errors:
                    print(f"❌ Invalid broker settings: {errors}")
                    return
                self.broker_config.settings = broker_settings
                self.broker_config.save()
                print("✅ Broker configuration imported")

            # Import provider settings
            if "providers" in import_data:
                provider_settings = ProviderSettings.from_dict(import_data["providers"])
                errors = provider_settings.validate()
                if errors:
                    print(f"❌ Invalid provider settings: {errors}")
                    return
                self.provider_config.settings = provider_settings
                self.provider_config.save()
                print("✅ Provider configuration imported")

            print(f"✅ Configuration imported from: {input_path}")

        except Exception as e:
            print(f"❌ Import failed: {e}")
            sys.exit(1)

    def cmd_api_key(self, args) -> None:
        """Manage API keys."""
        self.load_configs(args.config_dir)

        if args.action == "set":
            if not args.key:
                # Read from stdin if not provided
                print("Enter API key: ", end="")
                api_key = input().strip()
            else:
                api_key = args.key

            if not api_key:
                print("❌ No API key provided")
                return

            success = self.provider_config.set_api_key(args.provider, api_key)
            if success:
                print(f"✅ API key set for {args.provider}")
            else:
                print(f"❌ Failed to set API key for {args.provider}")

        elif args.action == "check":
            api_key = self.provider_config.get_api_key(args.provider)
            if api_key:
                print(f"✅ {args.provider} API key is configured")
            else:
                print(f"❌ {args.provider} API key is not set")

        elif args.action == "clear":
            success = self.provider_config.set_api_key(args.provider, None)
            if success:
                print(f"✅ API key cleared for {args.provider}")
            else:
                print(f"❌ Failed to clear API key for {args.provider}")

    def cmd_provider(self, args) -> None:
        """Manage provider settings."""
        self.load_configs(args.config_dir)

        if args.action == "enable":
            success = self.provider_config.enable_provider(args.provider, True)
            if success:
                print(f"✅ Enabled {args.provider} provider")
            else:
                print(f"❌ Failed to enable {args.provider} provider")

        elif args.action == "disable":
            success = self.provider_config.enable_provider(args.provider, False)
            if success:
                print(f"✅ Disabled {args.provider} provider")
            else:
                print(f"❌ Failed to disable {args.provider} provider")

        elif args.action == "status":
            status = self.provider_config.get_status()
            providers = status.get("providers", {})

            for provider_name, provider_status in providers.items():
                enabled = "✅" if provider_status.get("enabled") else "❌"
                configured = "✅" if provider_status.get("configured") else "❌"
                print(f"{provider_name}: enabled={enabled} configured={configured}")

    def cmd_task_model(self, args) -> None:
        """Manage task-specific model preferences."""
        self.load_configs(args.config_dir)

        if args.action == "set":
            success = self.provider_config.set_task_model_preference(args.task, args.model)
            if success:
                print(f"✅ Set {args.task} task model preference to {args.model}")
            else:
                print(f"❌ Failed to set {args.task} task model preference")

        elif args.action == "get":
            preference = self.provider_config.get_task_model_preference(args.task)
            print(f"{args.task} task model preference: {preference}")

        elif args.action == "list":
            for task in ["broker", "capture", "analysis"]:
                preference = self.provider_config.get_task_model_preference(task)
                print(f"{task}: {preference}")

    def run(self) -> None:
        """Run the config CLI with command-line arguments."""
        parser = argparse.ArgumentParser(
            description="Context Capture Configuration CLI",
            formatter_class=argparse.RawDescriptionHelpFormatter
        )

        # Global options
        parser.add_argument(
            "--config-dir", "-c",
            type=str,
            help="Configuration directory path"
        )
        parser.add_argument(
            "--log-level",
            choices=["DEBUG", "INFO", "WARNING", "ERROR"],
            default="INFO",
            help="Set logging level"
        )

        subparsers = parser.add_subparsers(dest="command", help="Available commands")

        # Show command
        show_parser = subparsers.add_parser("show", help="Show current configuration")
        show_parser.add_argument("--format", choices=["text", "json"], default="text", help="Output format")

        # Set command
        set_parser = subparsers.add_parser("set", help="Set a configuration value")
        set_parser.add_argument("setting", help="Setting name (e.g., broker.privacy_level)")
        set_parser.add_argument("value", help="Setting value")

        # Get command
        get_parser = subparsers.add_parser("get", help="Get a configuration value")
        get_parser.add_argument("setting", help="Setting name (e.g., broker.privacy_level)")

        # Validate command
        subparsers.add_parser("validate", help="Validate configuration")

        # Reset command
        reset_parser = subparsers.add_parser("reset", help="Reset configuration to defaults")
        reset_parser.add_argument("type", choices=["broker", "provider", "all"], help="Config type to reset")
        reset_parser.add_argument("--force", action="store_true", help="Skip confirmation")

        # Export command
        export_parser = subparsers.add_parser("export", help="Export configuration to file")
        export_parser.add_argument("output", help="Output file path")

        # Import command
        import_parser = subparsers.add_parser("import", help="Import configuration from file")
        import_parser.add_argument("input", help="Input file path")

        # API key command
        api_key_parser = subparsers.add_parser("api-key", help="Manage API keys")
        api_key_parser.add_argument("action", choices=["set", "check", "clear"], help="Action to perform")
        api_key_parser.add_argument("provider", choices=["anthropic"], help="Provider name")
        api_key_parser.add_argument("--key", help="API key (will prompt if not provided)")

        # Provider command
        provider_parser = subparsers.add_parser("provider", help="Manage provider settings")
        provider_parser.add_argument("action", choices=["enable", "disable", "status"], help="Action to perform")
        provider_parser.add_argument("provider", nargs="?", choices=["ollama", "anthropic"], help="Provider name")

        # Task model command
        task_parser = subparsers.add_parser("task-model", help="Manage task-specific model preferences")
        task_parser.add_argument("action", choices=["set", "get", "list"], help="Action to perform")
        task_parser.add_argument("task", nargs="?", choices=["broker", "capture", "analysis"], help="Task type")
        task_parser.add_argument("model", nargs="?", help="Model preference")

        # Parse arguments
        args = parser.parse_args()

        # Setup logging
        self.setup_logging(args.log_level)

        # Route to appropriate command
        if args.command == "show":
            self.cmd_show(args)
        elif args.command == "set":
            self.cmd_set(args)
        elif args.command == "get":
            self.cmd_get(args)
        elif args.command == "validate":
            self.cmd_validate(args)
        elif args.command == "reset":
            self.cmd_reset(args)
        elif args.command == "export":
            self.cmd_export(args)
        elif args.command == "import":
            self.cmd_import(args)
        elif args.command == "api-key":
            self.cmd_api_key(args)
        elif args.command == "provider":
            self.cmd_provider(args)
        elif args.command == "task-model":
            self.cmd_task_model(args)
        else:
            parser.print_help()


def main():
    """Entry point for the config CLI."""
    cli = ConfigCLI()
    cli.run()


if __name__ == "__main__":
    main()