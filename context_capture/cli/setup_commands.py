"""
Setup and bootstrap commands for the Context Capture system.

Provides commands for initial setup, system verification, and bootstrapping
the context capture environment.
"""

import json
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path
from typing import Dict, List, Optional, Tuple

import requests


class SetupCLI:
    """CLI for setup and bootstrap operations."""

    def __init__(self):
        """Initialize setup CLI."""
        self.project_root = Path.cwd()
        self.context_dir = self.project_root / ".context"

    def cmd_bootstrap(self, args) -> None:
        """
        Bootstrap the context capture system.

        Performs comprehensive setup including:
        - Directory structure creation
        - Ollama installation and service check
        - Model availability verification
        - Configuration validation
        """
        print("🚀 Bootstrapping Agentic Context Capture\n")
        print("=" * 60)

        # Step 1: Create directory structure
        print("\n📁 Setting up directory structure...")
        self._create_directory_structure()

        # Step 2: Check git ignore
        print("\n🔒 Verifying .gitignore configuration...")
        self._check_gitignore()

        # Step 3: Check Ollama
        print("\n🤖 Checking local model setup...")
        ollama_status = self._check_ollama()

        # Step 4: Validate configuration
        print("\n⚙️  Validating configuration...")
        self._validate_config()

        # Step 5: Show next steps
        print("\n" + "=" * 60)
        print("✅ Bootstrap complete!\n")

        self._show_next_steps(ollama_status)

    def _create_directory_structure(self) -> None:
        """Create the .context directory structure."""
        dirs_to_create = [
            self.context_dir / "knowledge" / "decisions",
            self.context_dir / "knowledge" / "patterns",
            self.context_dir / "knowledge" / "insights",
            self.context_dir / "knowledge" / "strategies",
            self.context_dir / "knowledge" / "daily",
            self.context_dir / "queue" / "pending",
            self.context_dir / "queue" / "processing",
            self.context_dir / "queue" / "processed",
            self.context_dir / "logs",
        ]

        created = []
        existing = []

        for dir_path in dirs_to_create:
            if not dir_path.exists():
                dir_path.mkdir(parents=True, exist_ok=True)
                created.append(str(dir_path.relative_to(self.project_root)))
            else:
                existing.append(str(dir_path.relative_to(self.project_root)))

        if created:
            print(f"  ✅ Created {len(created)} directories")
            if len(created) <= 5:
                for d in created:
                    print(f"     - {d}")

        if existing:
            print(f"  ℹ️  {len(existing)} directories already exist")

    def _check_gitignore(self) -> None:
        """Check if .context is in .gitignore."""
        gitignore_path = self.project_root / ".gitignore"

        if not gitignore_path.exists():
            print("  ⚠️  No .gitignore found")
            response = input("  Create .gitignore with .context/? [Y/n]: ").strip().lower()
            if response in ['', 'y', 'yes']:
                gitignore_path.write_text(".context/\n")
                print("  ✅ Created .gitignore")
            return

        content = gitignore_path.read_text()
        if ".context/" in content or ".context" in content:
            print("  ✅ .context/ is ignored")
        else:
            print("  ⚠️  .context/ not in .gitignore")
            response = input("  Add .context/ to .gitignore? [Y/n]: ").strip().lower()
            if response in ['', 'y', 'yes']:
                with open(gitignore_path, 'a') as f:
                    f.write("\n# Context capture\n.context/\n")
                print("  ✅ Added .context/ to .gitignore")

    def _check_ollama(self) -> Dict[str, any]:
        """
        Check Ollama installation, service, and models.

        Returns:
            Dictionary with status information
        """
        status = {
            "binary_found": False,
            "binary_path": None,
            "service_running": False,
            "service_url": "http://localhost:11434",
            "models_available": [],
            "configured_model": "mistral:7b",
            "model_ready": False,
        }

        # Check if ollama binary exists
        ollama_path = shutil.which("ollama")
        if ollama_path:
            status["binary_found"] = True
            status["binary_path"] = ollama_path
            print(f"  ✅ Ollama binary found: {ollama_path}")
        else:
            print("  ❌ Ollama binary not found")
            self._show_ollama_install_help()
            return status

        # Check if service is running
        try:
            response = requests.get(f"{status['service_url']}/api/version", timeout=2)
            if response.status_code == 200:
                status["service_running"] = True
                version_info = response.json()
                print(f"  ✅ Ollama service running (version: {version_info.get('version', 'unknown')})")
            else:
                print("  ❌ Ollama service not responding properly")
        except requests.exceptions.RequestException:
            print("  ❌ Ollama service not running")
            self._try_start_ollama_service(status)
            return status

        # Check available models
        if status["service_running"]:
            models = self._get_ollama_models()
            status["models_available"] = models

            if models:
                print(f"  📦 Found {len(models)} model(s):")
                for model in models:
                    is_configured = model["name"] == status["configured_model"]
                    marker = "✅" if is_configured else "  "
                    print(f"     {marker} {model['name']} ({model['size']})")
                    if is_configured:
                        status["model_ready"] = True
            else:
                print("  ⚠️  No models found")

            # Check if configured model is available
            if not status["model_ready"]:
                print(f"\n  ⚠️  Configured model '{status['configured_model']}' not found")
                self._offer_model_download(status["configured_model"])

        return status

    def _get_ollama_models(self) -> List[Dict[str, str]]:
        """Get list of available Ollama models."""
        try:
            result = subprocess.run(
                ["ollama", "list"],
                capture_output=True,
                text=True,
                timeout=5
            )

            if result.returncode != 0:
                return []

            # Parse ollama list output
            models = []
            lines = result.stdout.strip().split('\n')[1:]  # Skip header

            for line in lines:
                if line.strip():
                    parts = line.split()
                    if len(parts) >= 2:
                        models.append({
                            "name": parts[0],
                            "size": parts[2] if len(parts) > 2 else "unknown"
                        })

            return models

        except Exception as e:
            print(f"  ⚠️  Error listing models: {e}")
            return []

    def _try_start_ollama_service(self, status: Dict) -> None:
        """Attempt to start the Ollama service."""
        print("\n  Ollama service is not running.")

        # Check platform
        if sys.platform == "darwin":
            print("  💡 On macOS, you can start Ollama by:")
            print("     1. Opening the Ollama app, OR")
            print("     2. Running: ollama serve")
        else:
            print("  💡 Start Ollama service with: ollama serve")

        response = input("\n  Start Ollama now? [Y/n]: ").strip().lower()
        if response not in ['', 'y', 'yes']:
            return

        try:
            print("  🔄 Starting Ollama service...")
            # Start ollama serve in background
            subprocess.Popen(
                ["ollama", "serve"],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                start_new_session=True
            )

            # Wait for service to be ready
            for i in range(10):
                time.sleep(1)
                try:
                    response = requests.get(f"{status['service_url']}/api/version", timeout=1)
                    if response.status_code == 200:
                        print("  ✅ Ollama service started successfully")
                        status["service_running"] = True

                        # Now check models
                        models = self._get_ollama_models()
                        status["models_available"] = models

                        if models:
                            print(f"  📦 Found {len(models)} model(s)")
                            for model in models:
                                print(f"     - {model['name']}")
                        else:
                            print("  ⚠️  No models found")
                            self._offer_model_download(status["configured_model"])

                        return
                except:
                    continue

            print("  ⚠️  Service started but not responding yet")
            print("     Please wait a few seconds and run: context-capture status")

        except Exception as e:
            print(f"  ❌ Failed to start service: {e}")

    def _offer_model_download(self, model_name: str) -> None:
        """Offer to download the configured model."""
        print(f"\n  The configured model '{model_name}' is not available.")

        # Get model info
        model_info = {
            "mistral:7b": "4.1GB, Fast, Excellent quality (recommended)",
            "phi3:mini": "2.2GB, Very fast, Good quality",
            "codellama:7b": "3.8GB, Fast, Excellent for code",
        }

        info = model_info.get(model_name, "unknown size")
        print(f"  Model info: {info}")

        response = input(f"\n  Download {model_name} now? [Y/n]: ").strip().lower()
        if response not in ['', 'y', 'yes']:
            print("  ℹ️  You can download it later with: ollama pull " + model_name)
            return

        try:
            print(f"\n  📥 Downloading {model_name}...")
            print("  This may take several minutes depending on your connection.\n")

            result = subprocess.run(
                ["ollama", "pull", model_name],
                check=True
            )

            if result.returncode == 0:
                print(f"\n  ✅ Model {model_name} downloaded successfully!")
            else:
                print(f"\n  ❌ Failed to download model")

        except subprocess.CalledProcessError as e:
            print(f"\n  ❌ Error downloading model: {e}")
        except KeyboardInterrupt:
            print("\n\n  ⚠️  Download cancelled")

    def _show_ollama_install_help(self) -> None:
        """Show instructions for installing Ollama."""
        print("\n  📦 Ollama is not installed. Install it to use local models:")
        print("\n  macOS/Linux:")
        print("    curl -fsSL https://ollama.com/install.sh | sh")
        print("\n  Or visit: https://ollama.com")
        print("\n  After installation, run: context-capture bootstrap")

    def _validate_config(self) -> None:
        """Validate configuration files."""
        config_files = [
            self.context_dir / "provider_config.json",
            self.context_dir / "broker_config.json",
        ]

        found = 0
        for config_file in config_files:
            if config_file.exists():
                found += 1

        if found > 0:
            print(f"  ✅ Found {found} configuration file(s)")
        else:
            print("  ℹ️  No configuration files found (will use defaults)")

    def _show_next_steps(self, ollama_status: Dict) -> None:
        """Show next steps for the user."""
        print("📋 Next Steps:\n")

        step = 1

        # Model setup
        if not ollama_status.get("model_ready"):
            print(f"{step}. Set up a local model:")
            if not ollama_status.get("binary_found"):
                print("   → Install Ollama: https://ollama.com")
            elif not ollama_status.get("service_running"):
                print("   → Start Ollama: ollama serve")
            else:
                print(f"   → Download model: ollama pull {ollama_status['configured_model']}")
            print()
            step += 1

        # Claude Code hooks
        print(f"{step}. Configure Claude Code hooks:")
        self._setup_claude_hooks()
        print()
        step += 1

        # Test
        print(f"{step}. Test the system:")
        print("   → context-capture status")
        print("   → context-capture search \"test query\"")
        print()

        # Documentation
        print("📚 Documentation:")
        print("   → README.md - Overview and features")
        print("   → CONTEXT_BROKER.md - Detailed broker documentation")
        print("   → CURRENT_STATUS.md - Development status and roadmap")

    def _get_python_command(self) -> str:
        """
        Get the appropriate Python command for hooks.

        Prefers venv Python to avoid missing dependencies.
        """
        # Check for venv in project root
        venv_paths = [
            self.project_root / "venv" / "bin" / "python3",
            self.project_root / ".venv" / "bin" / "python3",
            self.project_root / "env" / "bin" / "python3",
        ]

        for venv_python in venv_paths:
            if venv_python.exists():
                # Test if it has the required packages
                try:
                    result = subprocess.run(
                        [str(venv_python), "-c", "import yaml; import context_capture"],
                        capture_output=True,
                        timeout=5
                    )
                    if result.returncode == 0:
                        return str(venv_python)
                except:
                    continue

        # Fallback to system Python (but warn)
        print("   ⚠️  No venv found with dependencies, using system python3")
        print("      You may need to install dependencies: pip3 install -e .")
        return "python3"

    def _show_hook_config(self) -> None:
        """Display Claude Code hook configuration."""
        python_cmd = self._get_python_command()

        hook_config = {
            "hooks": {
                "PostToolUse": [
                    {
                        "hooks": [
                            {
                                "type": "command",
                                "command": f"{python_cmd} -m context_capture.core.capture"
                            }
                        ]
                    }
                ]
            },
            "statusLine": {
                "type": "command",
                "command": f"{python_cmd} -c \"from context_capture.utils.status import StatusMonitor; print(StatusMonitor().get_status_line(), end='')\""
            }
        }

        print(json.dumps(hook_config, indent=2))

    def cmd_troubleshoot(self, args) -> None:
        """
        Comprehensive system diagnostics for Claude Code integration.

        Checks all necessary components:
        - Claude Code hooks
        - Ollama/Local LLM
        - Provider configuration
        - Directory structure
        - Background processor
        - System health
        """
        print("🔍 Agentic Context Capture - System Diagnostics")
        print("=" * 60)
        print()

        issues = []
        warnings = []
        auto_fixes = []

        # Check 1: Claude Code Integration
        print("📋 Claude Code Integration:")
        hook_status = self._check_claude_hooks(verbose=args.verbose)
        if not hook_status['ok']:
            issues.extend(hook_status['errors'])
            auto_fixes.extend(hook_status['fixes'])
        if hook_status['warnings']:
            warnings.extend(hook_status['warnings'])
        print()

        # Check 2: Local LLM (Ollama)
        print("🤖 Local LLM (Ollama):")
        ollama_status = self._check_ollama()
        if not ollama_status.get("model_ready"):
            if not ollama_status.get("binary_found"):
                issues.append("Ollama not installed")
            elif not ollama_status.get("service_running"):
                issues.append("Ollama service not running")
                auto_fixes.append(("start_ollama", "Start Ollama service"))
            else:
                issues.append(f"Model '{ollama_status['configured_model']}' not available")
                auto_fixes.append(("pull_model", f"Download {ollama_status['configured_model']}"))
        print()

        # Check 3: Provider Configuration
        print("⚙️  Provider Configuration:")
        config_status = self._check_provider_config(verbose=args.verbose)
        if not config_status['ok']:
            issues.extend(config_status['errors'])
        if config_status['warnings']:
            warnings.extend(config_status['warnings'])
        print()

        # Check 4: Directory Structure
        print("📁 Directory Structure:")
        dir_status = self._check_directories(verbose=args.verbose)
        if not dir_status['ok']:
            issues.extend(dir_status['errors'])
            auto_fixes.append(("create_dirs", "Create missing directories"))
        print()

        # Check 5: Background Processor
        print("🔄 Background Processor:")
        proc_status = self._check_processor(verbose=args.verbose)
        if not proc_status['ok']:
            warnings.extend(proc_status['warnings'])
            auto_fixes.append(("start_processor", "Start background processor"))
        print()

        # Check 6: System Health
        print("💊 System Health:")
        health_status = self._check_system_health(verbose=args.verbose)
        if health_status['warnings']:
            warnings.extend(health_status['warnings'])
        print()

        # Summary
        print("=" * 60)
        overall_status = "🟢 HEALTHY" if not issues and not warnings else "🟡 DEGRADED" if not issues else "🔴 CRITICAL"
        print(f"Overall Status: {overall_status}")
        if issues or warnings:
            print(f"  ({len(warnings)} warnings, {len(issues)} errors)")
        print()

        # Show issues
        if issues:
            print("Critical Issues:")
            for i, issue in enumerate(issues, 1):
                print(f"  {i}. {issue}")
            print()

        # Show warnings
        if warnings:
            print("Warnings:")
            for i, warning in enumerate(warnings, 1):
                print(f"  {i}. {warning}")
            print()

        # Show fixes
        if auto_fixes and not args.fix:
            print("💡 Run with --fix to attempt automatic fixes:")
            for fix_type, description in auto_fixes:
                print(f"   • {description}")
            print()

        # Auto-fix if requested
        if args.fix and auto_fixes:
            print("🔧 Attempting automatic fixes...")
            self._apply_fixes(auto_fixes, ollama_status)
            print()

        # Exit with appropriate code
        sys.exit(1 if issues else 0)

    def _check_claude_hooks(self, verbose: bool = False) -> dict:
        """Check Claude Code hooks configuration."""
        errors = []
        warnings = []
        fixes = []

        claude_dir = self.project_root / ".claude"
        settings_file = claude_dir / "settings.local.json"

        if not claude_dir.exists():
            print("   ❌ .claude directory not found")
            errors.append(".claude directory not configured")
            fixes.append(("setup_hooks", "Create .claude/settings.local.json"))
            return {'ok': False, 'errors': errors, 'warnings': warnings, 'fixes': fixes}

        if not settings_file.exists():
            print("   ❌ settings.local.json not found")
            errors.append("Claude Code hooks not configured")
            fixes.append(("setup_hooks", "Create hooks configuration"))
            return {'ok': False, 'errors': errors, 'warnings': warnings, 'fixes': fixes}

        # Check hook content
        try:
            with open(settings_file, 'r') as f:
                config = json.load(f)

            # Check PostToolUse hook
            if 'hooks' in config and 'PostToolUse' in config['hooks']:
                print("   ✅ PostToolUse hook configured")

                # Check if Python command is valid
                hook_cmd = config['hooks']['PostToolUse'][0]['hooks'][0].get('command', '')
                if 'python3' in hook_cmd:
                    python_path = hook_cmd.split()[0]
                    # Test if the Python can import required modules
                    try:
                        result = subprocess.run(
                            [python_path, "-c", "import yaml; import context_capture"],
                            capture_output=True,
                            timeout=5
                        )
                        if result.returncode != 0:
                            print("   ⚠️  Hook Python missing dependencies")
                            warnings.append("Hook Python environment missing dependencies")
                            fixes.append(("fix_hook_python", "Update hook to use venv Python"))
                    except:
                        print("   ⚠️  Hook Python command invalid")
                        warnings.append("Hook Python command may be invalid")
            else:
                print("   ❌ PostToolUse hook missing")
                errors.append("PostToolUse hook not configured")

            # Check statusLine
            if 'statusLine' in config:
                print("   ✅ Status line hook configured")
            else:
                print("   ⚠️  Status line hook missing")
                warnings.append("Status line not configured (optional)")

            if verbose:
                print(f"   📄 Config: {settings_file}")

        except json.JSONDecodeError:
            print("   ❌ settings.local.json is invalid JSON")
            errors.append("Invalid hook configuration file")

        return {
            'ok': len(errors) == 0,
            'errors': errors,
            'warnings': warnings,
            'fixes': fixes
        }

    def _check_provider_config(self, verbose: bool = False) -> dict:
        """Check provider configuration."""
        from context_capture.config.provider_config import ProviderConfig

        errors = []
        warnings = []

        try:
            provider_config = ProviderConfig()
            validation_errors = provider_config.settings.validate()

            if validation_errors:
                print("   ❌ Provider config has errors")
                errors.extend(validation_errors)
                return {'ok': False, 'errors': errors, 'warnings': warnings}

            print("   ✅ Provider config valid")

            # Check enabled providers
            if provider_config.settings.ollama_enabled:
                print("   ✅ Ollama provider enabled")
            else:
                print("   ⚠️  Ollama provider disabled")
                warnings.append("Ollama provider disabled")

            if provider_config.settings.anthropic_enabled:
                if provider_config.settings.anthropic_api_key:
                    print("   ✅ Anthropic provider configured")
                else:
                    print("   ⚠️  Anthropic enabled but no API key")
                    warnings.append("Anthropic API key not set")
            else:
                print("   ⚠️  Anthropic not configured (cloud fallback unavailable)")
                warnings.append("Cloud provider not configured (optional)")

            if verbose:
                print(f"   📄 Config: {provider_config.config_path}")

        except Exception as e:
            print(f"   ❌ Error checking provider config: {e}")
            errors.append(f"Provider config error: {e}")

        return {'ok': len(errors) == 0, 'errors': errors, 'warnings': warnings}

    def _check_directories(self, verbose: bool = False) -> dict:
        """Check directory structure."""
        errors = []

        required_dirs = [
            self.context_dir / "knowledge" / "decisions",
            self.context_dir / "knowledge" / "patterns",
            self.context_dir / "knowledge" / "insights",
            self.context_dir / "knowledge" / "strategies",
            self.context_dir / "knowledge" / "daily",
            self.context_dir / "queue" / "pending",
            self.context_dir / "queue" / "processing",
            self.context_dir / "queue" / "processed",
            self.context_dir / "logs",
        ]

        missing = []
        for dir_path in required_dirs:
            if not dir_path.exists():
                missing.append(str(dir_path.relative_to(self.project_root)))

        if missing:
            print(f"   ❌ {len(missing)} directories missing")
            if verbose:
                for d in missing:
                    print(f"      - {d}")
            errors.append(f"{len(missing)} required directories missing")
        else:
            print("   ✅ All required directories present")

        if verbose and not missing:
            print(f"   📁 Base: {self.context_dir}")

        return {'ok': len(errors) == 0, 'errors': errors}

    def _check_processor(self, verbose: bool = False) -> dict:
        """Check background processor status."""
        from context_capture.utils.status import StatusMonitor

        warnings = []

        try:
            monitor = StatusMonitor()
            if monitor.is_processor_running():
                pid = monitor.get_processor_pid()
                uptime = monitor.get_processor_uptime()
                print(f"   ✅ Processor running (PID: {pid})")
                if uptime and verbose:
                    print(f"      Uptime: {uptime}")
            else:
                print("   ⚠️  Processor not running")
                warnings.append("Background processor not running (events won't be analyzed)")
        except Exception as e:
            print(f"   ⚠️  Error checking processor: {e}")
            warnings.append(f"Processor check failed: {e}")

        return {'ok': True, 'warnings': warnings}

    def _check_system_health(self, verbose: bool = False) -> dict:
        """Check overall system health."""
        from context_capture.utils.status import StatusMonitor

        warnings = []

        try:
            monitor = StatusMonitor()
            health_score = monitor._calculate_system_health()

            if health_score >= 0.8:
                print(f"   ✅ Health Score: {health_score:.1f} (excellent)")
            elif health_score >= 0.6:
                print(f"   ⚠️  Health Score: {health_score:.1f} (degraded)")
                warnings.append("System health degraded")
            else:
                print(f"   ❌ Health Score: {health_score:.1f} (critical)")
                warnings.append("System health critical")

            # Queue stats
            queue_stats = monitor.queue_manager.get_queue_stats()
            pending = queue_stats.get('pending', {}).get('count', 0)
            processing = queue_stats.get('processing', {}).get('count', 0)
            processed = queue_stats.get('processed', {}).get('count', 0)

            print(f"   📥 Queue: {pending} pending, {processing} processing")

            if pending > 20:
                warnings.append(f"Large queue backlog: {pending} pending events")

            # Knowledge stats
            knowledge_stats = monitor.get_knowledge_stats()
            total_insights = knowledge_stats.get('total_insights', 0)
            print(f"   📚 Knowledge: {total_insights} insights captured")

            if verbose:
                for cat, info in knowledge_stats.get('categories', {}).items():
                    print(f"      - {cat}: {info['insights']} insights")

        except Exception as e:
            print(f"   ⚠️  Error checking health: {e}")
            warnings.append(f"Health check failed: {e}")

        return {'warnings': warnings}

    def _apply_fixes(self, fixes, ollama_status) -> None:
        """Apply automatic fixes."""
        for fix_type, description in fixes:
            print(f"\n   🔧 {description}...")

            if fix_type == "create_dirs":
                self._create_directory_structure()
                print("      ✅ Directories created")

            elif fix_type == "start_ollama":
                self._try_start_ollama_service(ollama_status)

            elif fix_type == "pull_model":
                model = ollama_status.get('configured_model', 'mistral:7b')
                self._offer_model_download(model)

            elif fix_type == "start_processor":
                try:
                    subprocess.Popen(
                        [sys.executable, "-m", "context_capture.core.processor", "--project-dir", str(self.project_root)],
                        start_new_session=True,
                        stdout=subprocess.DEVNULL,
                        stderr=subprocess.DEVNULL
                    )
                    time.sleep(2)
                    print("      ✅ Processor started")
                except Exception as e:
                    print(f"      ❌ Failed to start processor: {e}")

            elif fix_type == "setup_hooks" or fix_type == "fix_hook_python":
                self._setup_claude_hooks()
                print("      ✅ Hooks configured")

    def _setup_claude_hooks(self) -> None:
        """Setup Claude Code hooks in .claude/settings.local.json."""
        claude_dir = self.project_root / ".claude"
        settings_file = claude_dir / "settings.local.json"

        # Detect Python executable (prefer venv if available)
        python_cmd = self._get_python_command()

        hook_config = {
            "hooks": {
                "PostToolUse": [
                    {
                        "hooks": [
                            {
                                "type": "command",
                                "command": f"{python_cmd} -m context_capture.core.capture"
                            }
                        ]
                    }
                ]
            },
            "statusLine": {
                "type": "command",
                "command": f"{python_cmd} -c \"from context_capture.utils.status import StatusMonitor; print(StatusMonitor().get_status_line(), end='')\""
            }
        }

        # Check if .claude directory exists
        if not claude_dir.exists():
            claude_dir.mkdir(parents=True, exist_ok=True)
            print("   ✅ Created .claude directory")

        # Check if settings file exists
        if settings_file.exists():
            try:
                with open(settings_file, 'r') as f:
                    existing_config = json.load(f)

                # Merge configurations
                if "hooks" in existing_config:
                    print("   ℹ️  Existing hooks found in settings.local.json")

                    # Check if we're in an interactive terminal
                    if sys.stdin.isatty():
                        response = input("   Overwrite hooks configuration? [y/N]: ").strip().lower()
                        if response not in ['y', 'yes']:
                            print("   ℹ️  Skipped hooks configuration. Manual config needed:")
                            print()
                            self._show_hook_config()
                            return
                    else:
                        # Non-interactive mode - skip overwriting
                        print("   ℹ️  Skipping hooks update (already configured)")
                        return

                # Merge the configs
                existing_config.update(hook_config)

                with open(settings_file, 'w') as f:
                    json.dump(existing_config, f, indent=2)

                print("   ✅ Updated .claude/settings.local.json")

            except json.JSONDecodeError:
                print("   ⚠️  Existing settings.local.json is invalid JSON")

                # Check if we're in an interactive terminal
                if sys.stdin.isatty():
                    response = input("   Overwrite with new configuration? [y/N]: ").strip().lower()
                    if response in ['y', 'yes']:
                        with open(settings_file, 'w') as f:
                            json.dump(hook_config, f, indent=2)
                        print("   ✅ Created .claude/settings.local.json")
                    else:
                        print("   ℹ️  Manual configuration needed:")
                        print()
                        self._show_hook_config()
                else:
                    # Non-interactive - don't overwrite invalid file
                    print("   ℹ️  Manual configuration needed:")
                    print()
                    self._show_hook_config()
        else:
            # Create new settings file
            with open(settings_file, 'w') as f:
                json.dump(hook_config, f, indent=2)
            print("   ✅ Created .claude/settings.local.json")


def main():
    """Entry point for setup CLI."""
    import argparse

    parser = argparse.ArgumentParser(description="Setup commands for Context Capture")
    subparsers = parser.add_subparsers(dest="command", help="Available commands")

    # Bootstrap command
    subparsers.add_parser("bootstrap", help="Bootstrap the context capture system")

    # Troubleshoot command
    troubleshoot_parser = subparsers.add_parser(
        "troubleshoot",
        help="Diagnose system setup and configuration"
    )
    troubleshoot_parser.add_argument(
        "--fix",
        action="store_true",
        help="Attempt to auto-fix issues"
    )
    troubleshoot_parser.add_argument(
        "--verbose", "-v",
        action="store_true",
        help="Show detailed diagnostic information"
    )

    args = parser.parse_args()

    cli = SetupCLI()

    if args.command == "bootstrap":
        cli.cmd_bootstrap(args)
    elif args.command == "troubleshoot":
        cli.cmd_troubleshoot(args)
    else:
        parser.print_help()


if __name__ == "__main__":
    main()
