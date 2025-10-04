"""
Command-line interface for agentic context capture system.
"""

import os
import subprocess
import sys
from pathlib import Path
from typing import Optional

import click
from rich.console import Console
from rich.table import Table

from context_capture import __version__
from context_capture.core.capture import ContextCapture
from context_capture.core.processor import ContextProcessor
from context_capture.utils.config import Config
from context_capture.utils.status import StatusMonitor

console = Console()


@click.group()
@click.version_option(version=__version__)
@click.option('--config', '-c', type=click.Path(exists=True),
              help='Path to configuration file')
@click.option('--project-dir', '-p', type=click.Path(exists=True),
              help='Project directory')
@click.pass_context
def main(ctx, config: Optional[str], project_dir: Optional[str]):
    """Agentic Context Capture - Intelligent development insight preservation."""
    # Ensure context object exists
    ctx.ensure_object(dict)

    # Store config options
    ctx.obj['config_path'] = config
    ctx.obj['project_dir'] = Path(project_dir) if project_dir else Path.cwd()


@main.command()
@click.option('--force', '-f', is_flag=True, help='Force initialization even if already exists')
@click.pass_context
def init(ctx, force: bool):
    """Initialize context capture for a project."""
    project_dir = ctx.obj['project_dir']
    config = Config(config_path=ctx.obj['config_path'], project_dir=project_dir)

    console.print(f"🚀 Initializing context capture in: {project_dir}")

    try:
        # Create directory structure
        config.ensure_directories()
        console.print("✅ Created directory structure")

        # Create default config if it doesn't exist
        config_path = project_dir / '.context-capture' / 'config.yaml'
        if not config_path.exists() or force:
            config.save(config_path)
            console.print(f"✅ Created configuration: {config_path}")

        # Create hook template
        hook_template = project_dir / '.context-capture' / 'claude_hooks.json'
        if not hook_template.exists() or force:
            create_hook_template(hook_template, project_dir)
            console.print(f"✅ Created hook template: {hook_template}")

        console.print("\n🎯 Next steps:")
        console.print("1. Review configuration in .context-capture/config.yaml")
        console.print("2. Set up Claude Code hooks using the template")
        console.print("3. Start the background processor: context-capture start")

    except Exception as e:
        console.print(f"❌ Initialization failed: {e}")
        sys.exit(1)


@main.command()
@click.option('--daemon', '-d', is_flag=True, help='Run as daemon process')
@click.option('--debug', is_flag=True, help='Enable debug output')
@click.pass_context
def start(ctx, daemon: bool, debug: bool):
    """Start the background processor."""
    project_dir = ctx.obj['project_dir']
    config = Config(config_path=ctx.obj['config_path'], project_dir=project_dir)

    if debug:
        config.set('features.debug_mode', True)

    console.print("🤖 Starting context capture processor...")

    try:
        if daemon:
            # Start as daemon process
            script_path = Path(__file__).parent / 'core' / 'processor.py'
            subprocess.Popen([
                sys.executable, str(script_path),
                '--project-dir', str(project_dir)
            ], start_new_session=True)
            console.print("✅ Processor started in background")
        else:
            # Run in foreground
            processor = ContextProcessor(config, project_dir)
            console.print("✅ Processor running (Ctrl+C to stop)")
            processor.run_forever()

    except KeyboardInterrupt:
        console.print("\n🛑 Processor stopped")
    except Exception as e:
        console.print(f"❌ Failed to start processor: {e}")
        sys.exit(1)


@main.command()
@click.pass_context
def stop(ctx):
    """Stop the background processor."""
    console.print("🛑 Stopping context capture processor...")

    try:
        # Find and kill processor
        result = subprocess.run(
            ['pkill', '-f', 'processor.py'],
            capture_output=True, text=True
        )

        if result.returncode == 0:
            console.print("✅ Processor stopped")
        else:
            console.print("⚠️  No processor found running")

    except Exception as e:
        console.print(f"❌ Failed to stop processor: {e}")
        sys.exit(1)


@main.command()
@click.option('--format', '-f', type=click.Choice(['table', 'json', 'line']),
              default='table', help='Output format')
@click.option('--watch', '-w', is_flag=True, help='Watch mode (continuous updates)')
@click.pass_context
def status(ctx, format: str, watch: bool):
    """Show system status."""
    project_dir = ctx.obj['project_dir']
    config = Config(config_path=ctx.obj['config_path'], project_dir=project_dir)
    monitor = StatusMonitor(config)

    try:
        if format == 'line':
            # Status line format (for Claude Code)
            status_line = monitor.get_status_line()
            console.print(status_line, end='')

        elif format == 'json':
            # JSON format
            import json
            status_data = monitor.get_detailed_status()
            console.print(json.dumps(status_data, indent=2))

        else:
            # Table format (default)
            if watch:
                import time
                try:
                    while True:
                        console.clear()
                        monitor.print_detailed_status()
                        time.sleep(2)
                except KeyboardInterrupt:
                    pass
            else:
                monitor.print_detailed_status()

    except Exception as e:
        console.print(f"❌ Failed to get status: {e}")
        sys.exit(1)


@main.command()
@click.argument('source_project', type=click.Path(exists=True))
@click.option('--dry-run', is_flag=True, help='Show what would be migrated without doing it')
@click.pass_context
def migrate(ctx, source_project: str, dry_run: bool):
    """Migrate existing context capture installation."""
    project_dir = ctx.obj['project_dir']
    source_dir = Path(source_project)

    console.print(f"📦 Migrating from: {source_dir}")
    console.print(f"📍 Migrating to: {project_dir}")

    if dry_run:
        console.print("🔍 Dry run mode - no changes will be made")

    try:
        # Check for existing context capture installation
        source_context = source_dir / '.context'
        source_scripts = source_dir / '.context-capture'

        if not (source_context.exists() or source_scripts.exists()):
            console.print("❌ No context capture installation found in source directory")
            sys.exit(1)

        # Plan migration
        migration_plan = []

        if source_context.exists():
            migration_plan.append(f"Copy knowledge base: {source_context / 'knowledge'}")
            migration_plan.append(f"Copy configuration: {source_context}")

        if source_scripts.exists():
            migration_plan.append(f"Copy scripts: {source_scripts}")

        console.print("\n📋 Migration plan:")
        for item in migration_plan:
            console.print(f"  • {item}")

        if not dry_run:
            if not click.confirm("\nProceed with migration?"):
                console.print("Migration cancelled")
                return

            # Perform migration
            config = Config(project_dir=project_dir)
            config.ensure_directories()

            # Copy knowledge base
            if source_context.exists():
                import shutil
                knowledge_source = source_context / 'knowledge'
                knowledge_dest = config.knowledge_dir

                if knowledge_source.exists():
                    shutil.copytree(knowledge_source, knowledge_dest, dirs_exist_ok=True)
                    console.print("✅ Migrated knowledge base")

            console.print("✅ Migration completed")
            console.print("\n🎯 Next steps:")
            console.print("1. Update Claude Code hooks to use new system")
            console.print("2. Start processor: context-capture start")

    except Exception as e:
        console.print(f"❌ Migration failed: {e}")
        sys.exit(1)


@main.command()
@click.option('--category', '-c', type=click.Choice(['decisions', 'patterns', 'insights', 'strategies']),
              help='Search specific category')
@click.option('--days', '-d', type=int, default=30, help='Search last N days')
@click.argument('query', required=False)
@click.pass_context
def search(ctx, category: Optional[str], days: int, query: Optional[str]):
    """Search captured insights."""
    project_dir = ctx.obj['project_dir']
    config = Config(config_path=ctx.obj['config_path'], project_dir=project_dir)

    if not query:
        query = click.prompt("Search query")

    console.print(f"🔍 Searching for: '{query}'")

    try:
        from datetime import datetime, timedelta
        import re

        cutoff_date = datetime.now() - timedelta(days=days)
        results = []

        # Search in knowledge base
        categories = [category] if category else ['decisions', 'patterns', 'insights', 'strategies']

        for cat in categories:
            cat_dir = config.knowledge_dir / cat
            if cat_dir.exists():
                for md_file in cat_dir.glob('*.md'):
                    # Check file date
                    file_date = datetime.fromtimestamp(md_file.stat().st_mtime)
                    if file_date < cutoff_date:
                        continue

                    # Search content
                    content = md_file.read_text()
                    if re.search(query, content, re.IGNORECASE):
                        results.append({
                            'category': cat,
                            'file': md_file.name,
                            'date': file_date,
                            'preview': content[:200] + '...'
                        })

        if results:
            console.print(f"\n📊 Found {len(results)} results:")

            table = Table()
            table.add_column("Category")
            table.add_column("Date")
            table.add_column("File")
            table.add_column("Preview")

            for result in sorted(results, key=lambda x: x['date'], reverse=True):
                table.add_row(
                    result['category'],
                    result['date'].strftime('%Y-%m-%d'),
                    result['file'],
                    result['preview']
                )

            console.print(table)
        else:
            console.print("❌ No results found")

    except Exception as e:
        console.print(f"❌ Search failed: {e}")
        sys.exit(1)


def create_hook_template(template_path: Path, project_dir: Path) -> None:
    """Create Claude Code hooks template."""
    template_content = {
        "hooks": {
            "PostToolUse": [
                {
                    "hooks": [
                        {
                            "type": "command",
                            "command": f"python3 {project_dir}/.context-capture/scripts/capture.py"
                        }
                    ]
                }
            ]
        },
        "statusLine": {
            "type": "command",
            "command": f"python3 -m context_capture.utils.status --format line"
        }
    }

    import json
    with open(template_path, 'w') as f:
        json.dump(template_content, f, indent=2)


if __name__ == '__main__':
    main()