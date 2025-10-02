"""
Status monitoring and display for context capture system.
"""

import os
import subprocess
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any, Dict, List, Optional

from context_capture.core.processor import ContextProcessor
from context_capture.core.queue import QueueManager
from context_capture.llm.ollama_client import OllamaClient
from context_capture.utils.config import Config


class StatusMonitor:
    """Monitor and display system status."""

    def __init__(self, config: Optional[Config] = None):
        """
        Initialize status monitor.

        Args:
            config: Configuration object
        """
        self.config = config or Config()
        self.queue_manager = QueueManager(self.config)
        self.llm_client = OllamaClient(self.config)

    def get_status_line(self) -> str:
        """
        Get concise status line for Claude Code status bar.

        Returns:
            Status line string with emoji indicators
        """
        try:
            # Check if background agent is running
            agent_running = self.is_processor_running()
            agent_icon = "🤖" if agent_running else "💤"

            # Check LLM availability
            llm_available = self.llm_client.is_available() and self.llm_client.is_model_available()
            llm_icon = "🧠" if llm_available else "🔌"

            # Get queue stats
            queue_stats = self.queue_manager.get_queue_stats()
            pending = queue_stats.get('pending', {}).get('count', 0)
            processing = queue_stats.get('processing', {}).get('count', 0)
            processed = queue_stats.get('processed', {}).get('count', 0)

            # Get latest insight info
            latest_info = self.get_latest_insight_info()

            # Build status line
            status_parts = [agent_icon, llm_icon]

            if pending > 0:
                status_parts.append(f"⏳{pending}")

            if processing > 0:
                status_parts.append(f"⚡{processing}")

            if processed > 0:
                status_parts.append(f"✅{processed}")

            if latest_info:
                status_parts.append(f"💡{latest_info}")

            return " ".join(status_parts)

        except Exception:
            return "🤖 ❓"  # Fallback status

    def get_detailed_status(self) -> Dict[str, Any]:
        """
        Get detailed system status.

        Returns:
            Dictionary with comprehensive status information
        """
        status = {
            'timestamp': datetime.now().isoformat(),
            'agent': self.get_agent_status(),
            'llm': self.get_llm_status(),
            'queue': self.queue_manager.get_queue_stats(),
            'knowledge': self.get_knowledge_stats(),
            'system': self.get_system_info(),
        }

        return status

    def get_agent_status(self) -> Dict[str, Any]:
        """Get background agent status."""
        running = self.is_processor_running()
        pid = self.get_processor_pid() if running else None

        return {
            'running': running,
            'pid': pid,
            'uptime': self.get_processor_uptime() if running else None,
        }

    def get_llm_status(self) -> Dict[str, Any]:
        """Get LLM status."""
        ollama_running = self.llm_client.is_available()
        models_info = self.llm_client.list_models() if ollama_running else None
        model_available = self.llm_client.is_model_available() if ollama_running else False

        return {
            'ollama_running': ollama_running,
            'model_available': model_available,
            'configured_model': self.config.get('model.name'),
            'models_count': len(models_info.get('models', [])) if models_info else 0,
        }

    def get_knowledge_stats(self) -> Dict[str, Any]:
        """Get knowledge base statistics."""
        try:
            stats = {'categories': {}, 'total_insights': 0, 'recent_activity': []}

            knowledge_dir = self.config.knowledge_dir
            categories = ['decisions', 'patterns', 'insights', 'strategies']

            for category in categories:
                category_dir = knowledge_dir / category
                if category_dir.exists():
                    files = list(category_dir.glob('*.md'))
                    insights_count = 0

                    for file_path in files:
                        try:
                            content = file_path.read_text()
                            # Count insights by counting "---" separators
                            insights_count += content.count('---')
                        except:
                            pass

                    stats['categories'][category] = {
                        'files': len(files),
                        'insights': insights_count
                    }
                    stats['total_insights'] += insights_count

            # Get recent activity
            stats['recent_activity'] = self.get_recent_activity()

            return stats

        except Exception:
            return {'error': 'Failed to get knowledge stats'}

    def get_recent_activity(self) -> List[Dict[str, str]]:
        """Get recent activity from knowledge files."""
        try:
            activity = []
            knowledge_dir = self.config.knowledge_dir

            # Check all category directories for recent files
            for category_dir in knowledge_dir.iterdir():
                if category_dir.is_dir() and category_dir.name != 'daily':
                    for md_file in category_dir.glob('*.md'):
                        # Get last modified time
                        mtime = datetime.fromtimestamp(md_file.stat().st_mtime)
                        age = datetime.now() - mtime

                        if age < timedelta(hours=24):  # Last 24 hours
                            activity.append({
                                'category': category_dir.name,
                                'age': self.format_age(age),
                                'file': md_file.name
                            })

            # Sort by most recent
            activity.sort(key=lambda x: x['age'])

            return activity[:5]  # Return top 5

        except Exception:
            return []

    def get_system_info(self) -> Dict[str, Any]:
        """Get system information."""
        return {
            'project_dir': str(self.config.project_dir),
            'base_dir': str(self.config.base_dir),
            'config_path': str(self.config.config_path) if self.config.config_path else None,
            'python_version': self.get_python_version(),
        }

    def is_processor_running(self) -> bool:
        """Check if background processor is running."""
        try:
            # Look for processor process
            result = subprocess.run(
                ['pgrep', '-f', 'processor_agent.py'],
                capture_output=True, text=True
            )
            return result.returncode == 0 and result.stdout.strip() != ''
        except:
            return False

    def get_processor_pid(self) -> Optional[int]:
        """Get processor PID if running."""
        try:
            result = subprocess.run(
                ['pgrep', '-f', 'processor_agent.py'],
                capture_output=True, text=True
            )
            if result.returncode == 0 and result.stdout.strip():
                return int(result.stdout.strip().split('\n')[0])
        except:
            pass
        return None

    def get_processor_uptime(self) -> Optional[str]:
        """Get processor uptime."""
        try:
            log_file = self.config.logs_dir / 'processor.log'
            if log_file.exists():
                # Find the most recent startup log
                with open(log_file, 'r') as f:
                    lines = f.readlines()

                for line in reversed(lines):
                    if 'processor agent started' in line.lower():
                        # Extract timestamp
                        timestamp_str = line.split()[0]
                        start_time = datetime.fromisoformat(timestamp_str)
                        uptime = datetime.now() - start_time
                        return self.format_age(uptime)

        except Exception:
            pass
        return None

    def get_latest_insight_info(self) -> Optional[str]:
        """Get info about the latest captured insight."""
        try:
            latest_time = None
            latest_category = None

            knowledge_dir = self.config.knowledge_dir
            categories = ['decisions', 'patterns', 'insights', 'strategies']

            for category in categories:
                category_dir = knowledge_dir / category
                if category_dir.exists():
                    for md_file in category_dir.glob('*.md'):
                        mtime = datetime.fromtimestamp(md_file.stat().st_mtime)
                        if latest_time is None or mtime > latest_time:
                            latest_time = mtime
                            latest_category = category

            if latest_time and latest_category:
                age = datetime.now() - latest_time
                return f"{latest_category}:{self.format_age(age)}"

        except Exception:
            pass
        return None

    def format_age(self, age: timedelta) -> str:
        """Format time age as human-readable string."""
        total_seconds = int(age.total_seconds())

        if total_seconds < 60:
            return f"{total_seconds}s"
        elif total_seconds < 3600:
            return f"{total_seconds // 60}m"
        elif total_seconds < 86400:
            return f"{total_seconds // 3600}h"
        else:
            return f"{total_seconds // 86400}d"

    def get_python_version(self) -> str:
        """Get Python version."""
        try:
            import sys
            return f"{sys.version_info.major}.{sys.version_info.minor}.{sys.version_info.micro}"
        except:
            return "unknown"

    def print_detailed_status(self) -> None:
        """Print detailed status to console."""
        status = self.get_detailed_status()

        print("🤖 Agentic Context Capture - System Status")
        print("=" * 50)
        print()

        # Agent status
        agent = status['agent']
        print("📡 Background Agent:")
        if agent['running']:
            print(f"   ✅ Running (PID: {agent['pid']})")
            if agent['uptime']:
                print(f"   ⏱️  Uptime: {agent['uptime']}")
        else:
            print("   ❌ Not running")
        print()

        # LLM status
        llm = status['llm']
        print("🧠 Local LLM (Ollama):")
        if llm['ollama_running']:
            print("   ✅ Ollama running")
            if llm['model_available']:
                print(f"   ✅ {llm['configured_model']} available")
            else:
                print(f"   ❌ {llm['configured_model']} not available")
            print(f"   📋 Models: {llm['models_count']}")
        else:
            print("   ❌ Ollama not running")
        print()

        # Queue status
        queue = status['queue']
        print("📥 Processing Queue:")
        for queue_type in ['pending', 'processing', 'processed']:
            if queue_type in queue:
                q = queue[queue_type]
                print(f"   {queue_type.title()}: {q['count']} files ({q['size_mb']} MB)")
        print()

        # Knowledge base
        knowledge = status['knowledge']
        print("📚 Knowledge Base:")
        if 'total_insights' in knowledge:
            print(f"   📊 Total insights captured: {knowledge['total_insights']}")
            for category, info in knowledge['categories'].items():
                print(f"   • {category.title()}: {info['insights']} insights in {info['files']} files")
        print()

        # Recent activity
        if knowledge.get('recent_activity'):
            print("🕒 Recent Activity (Last 24h):")
            for activity in knowledge['recent_activity']:
                print(f"   • {activity['category']}: {activity['age']} ago")
        print()


def main():
    """CLI entry point for status display."""
    monitor = StatusMonitor()
    monitor.print_detailed_status()


if __name__ == '__main__':
    main()