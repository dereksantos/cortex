"""
Background processor agent for context capture system.
Monitors queue and processes events with local LLM.
"""

import signal
import time
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any, Dict, List, Optional

from context_capture.core.queue import QueueManager
from context_capture.llm.ollama_client import OllamaClient, create_fallback_analysis
from context_capture.utils.config import Config


class ContextProcessor:
    """Background agent that processes queued events."""

    def __init__(self, config: Optional[Config] = None, project_dir: Optional[Path] = None):
        """
        Initialize context processor.

        Args:
            config: Configuration object
            project_dir: Project directory for context storage
        """
        self.project_dir = project_dir or Path.cwd()
        self.config = config or Config(project_dir=self.project_dir)
        self.config.ensure_directories()

        # Initialize components
        self.queue_manager = QueueManager(self.config)
        self.llm_client = OllamaClient(self.config)

        # State
        self.running = True
        self.stats = {
            'processed': 0,
            'captured': 0,
            'errors': 0,
            'start_time': datetime.now(),
            'last_cleanup': datetime.now()
        }

        # Set up signal handlers
        signal.signal(signal.SIGINT, self._signal_handler)
        signal.signal(signal.SIGTERM, self._signal_handler)

    def _signal_handler(self, signum: int, frame) -> None:
        """Handle shutdown signals gracefully."""
        self._log(f"Received signal {signum}, shutting down...")
        self.running = False

    def _log(self, message: str, level: str = "INFO") -> None:
        """Log message to file and optionally to console."""
        timestamp = datetime.now().isoformat()
        log_message = f"{timestamp} [{level}] {message}"

        # Write to log file
        try:
            log_file = self.config.logs_dir / 'processor.log'
            with open(log_file, 'a') as f:
                f.write(log_message + '\n')
        except:
            pass

        # Debug mode also prints to console
        if self.config.get('features.debug_mode', False):
            print(log_message)

    def run_forever(self) -> None:
        """Main processing loop."""
        self._log("Context processor agent started")

        interval = self.config.get('performance.process_interval', 2)
        cleanup_interval = self.config.get('performance.cleanup_interval', 3600)

        while self.running:
            try:
                # Process queued events
                self.process_queue()

                # Periodic cleanup
                if (datetime.now() - self.stats['last_cleanup']).seconds > cleanup_interval:
                    self._periodic_cleanup()
                    self.stats['last_cleanup'] = datetime.now()

                # Sleep until next cycle
                time.sleep(interval)

            except Exception as e:
                self._log(f"Error in main loop: {e}", "ERROR")
                self.stats['errors'] += 1
                time.sleep(interval)

        self._log("Context processor agent stopped")

    def process_queue(self) -> None:
        """Process all pending events in the queue."""
        try:
            batch_size = self.config.get('performance.batch_size', 5)

            for batch in self.queue_manager.batch_process(batch_size):
                if not self.running:
                    break

                self._process_batch(batch)

        except Exception as e:
            self._log(f"Error processing queue: {e}", "ERROR")

    def _process_batch(self, batch: List[Dict[str, Any]]) -> None:
        """Process a batch of events."""
        for event_data in batch:
            try:
                self._process_single_event(event_data)
                self.stats['processed'] += 1

            except Exception as e:
                self._log(f"Error processing event {event_data.get('id', 'unknown')}: {e}", "ERROR")
                self.stats['errors'] += 1

    def _process_single_event(self, event_data: Dict[str, Any]) -> None:
        """Process a single event."""
        # Analyze with LLM or fallback
        if self.llm_client.is_available() and self.llm_client.is_model_available():
            analysis = self.llm_client.analyze_event(event_data)
            if not analysis:
                analysis = create_fallback_analysis(event_data)
                analysis['fallback_reason'] = 'LLM analysis failed'
        else:
            analysis = create_fallback_analysis(event_data)
            analysis['fallback_reason'] = 'LLM unavailable'

        # Check if event meets capture threshold
        importance_threshold = self.config.get('capture.importance_threshold', 0.5)
        should_capture = analysis.get('should_capture', True) and \
                        analysis.get('importance', 0.0) >= importance_threshold

        if should_capture:
            self._store_insight(event_data, analysis)
            self.stats['captured'] += 1

    def _store_insight(self, event_data: Dict[str, Any], analysis: Dict[str, Any]) -> None:
        """Store analyzed insight in knowledge base."""
        category = analysis.get('category', 'insights')
        date_str = datetime.now().strftime('%Y-%m-%d')

        # Prepare insight data
        insight = {
            'timestamp': event_data.get('timestamp'),
            'datetime': event_data.get('datetime'),
            'event_id': event_data.get('id'),
            'tool_name': event_data.get('tool_name'),
            'summary': analysis.get('summary', ''),
            'importance': analysis.get('importance'),
            'tags': analysis.get('tags', []),
            'reasoning': analysis.get('reasoning', ''),
            'fallback': analysis.get('fallback', False),
            'project': event_data.get('project'),
            'user': event_data.get('user')
        }

        # Add tool-specific context
        if event_data.get('tool_input'):
            insight['context'] = self._extract_context(event_data)

        # Store in appropriate category file
        self._append_to_knowledge_file(category, date_str, insight)

        # Also store in daily summary
        self._update_daily_summary(date_str, insight)

    def _extract_context(self, event_data: Dict[str, Any]) -> Dict[str, Any]:
        """Extract relevant context from tool input/output."""
        tool_name = event_data.get('tool_name', '')
        tool_input = event_data.get('tool_input', {})

        context = {}

        if tool_name == 'Edit':
            context['file'] = tool_input.get('file_path', '')
            context['change_type'] = 'edit'

        elif tool_name == 'Write':
            context['file'] = tool_input.get('file_path', '')
            context['change_type'] = 'create'

        elif tool_name == 'Bash':
            context['command'] = tool_input.get('command', '')
            context['description'] = tool_input.get('description', '')

        elif tool_name == 'Task':
            context['agent_type'] = tool_input.get('subagent_type', '')
            context['task_description'] = tool_input.get('description', '')

        elif tool_name in ['Read', 'Grep', 'Glob']:
            context['file_path'] = tool_input.get('file_path', '')
            context['pattern'] = tool_input.get('pattern', '')

        return context

    def _append_to_knowledge_file(self, category: str, date_str: str, insight: Dict[str, Any]) -> None:
        """Append insight to knowledge file."""
        knowledge_file = self.config.knowledge_dir / category / f"{date_str}.md"
        knowledge_file.parent.mkdir(parents=True, exist_ok=True)

        # Create header if file doesn't exist
        if not knowledge_file.exists():
            with open(knowledge_file, 'w') as f:
                f.write(f"# {category.title()} - {date_str}\n\n")

        # Append insight
        with open(knowledge_file, 'a') as f:
            f.write(f"\n---\n\n")
            f.write(f"## {insight['datetime'][:19]} - {insight['summary']}\n\n")
            f.write(f"**Tool**: {insight['tool_name']}\n")
            f.write(f"**Importance**: {insight['importance']:.2f}\n")
            f.write(f"**Tags**: {', '.join(f'#{tag}' for tag in insight['tags'])}\n")
            f.write(f"**Reasoning**: {insight['reasoning']}\n")

            if insight.get('context'):
                f.write(f"**Context**: {insight['context']}\n")

            if insight.get('fallback'):
                f.write(f"**Note**: Analyzed using fallback heuristics (LLM unavailable)\n")

            f.write("\n")

    def _update_daily_summary(self, date_str: str, insight: Dict[str, Any]) -> None:
        """Update daily summary with new insight."""
        daily_file = self.config.knowledge_dir / 'daily' / f"{date_str}.md"
        daily_file.parent.mkdir(parents=True, exist_ok=True)

        # Initialize file if it doesn't exist
        if not daily_file.exists():
            with open(daily_file, 'w') as f:
                f.write(f"# Daily Summary - {date_str}\n\n")
                f.write("## Statistics\n\n")
                f.write("- **Decisions**: 0\n")
                f.write("- **Patterns**: 0\n")
                f.write("- **Insights**: 0\n")
                f.write("- **Strategies**: 0\n\n")
                f.write("## Key Events\n\n")

        # Read current content
        content = daily_file.read_text()

        # Update statistics
        category = insight.get('tags', ['insights'])[0] if insight.get('tags') else 'insights'
        if f"**{category.title()}**:" in content:
            # Find and increment the count
            import re
            pattern = f"(- \\*\\*{category.title()}\\*\\*: )(\\d+)"
            match = re.search(pattern, content)
            if match:
                current_count = int(match.group(2))
                new_count = current_count + 1
                content = re.sub(pattern, f"{match.group(1)}{new_count}", content)

        # Add to key events
        time_str = insight['datetime'][11:16]  # HH:MM format
        event_line = f"- **{time_str}** - {insight['summary']}\n"
        content += event_line

        # Write back
        daily_file.write_text(content)

    def _periodic_cleanup(self) -> None:
        """Perform periodic cleanup tasks."""
        try:
            # Clean up old processed files
            cleaned = self.queue_manager.cleanup_old_processed()
            if cleaned > 0:
                self._log(f"Cleaned up {cleaned} old processed files")

            # Emergency cleanup of stuck files
            emergency_stats = self.queue_manager.emergency_cleanup()
            if emergency_stats['moved_back'] > 0 or emergency_stats['removed_corrupted'] > 0:
                self._log(f"Emergency cleanup: {emergency_stats}")

        except Exception as e:
            self._log(f"Error during cleanup: {e}", "ERROR")

    def get_status(self) -> Dict[str, Any]:
        """Get current processor status."""
        uptime = datetime.now() - self.stats['start_time']
        queue_stats = self.queue_manager.get_queue_stats()

        return {
            'running': self.running,
            'uptime_seconds': int(uptime.total_seconds()),
            'stats': self.stats.copy(),
            'queue': queue_stats,
            'llm_available': self.llm_client.is_available(),
            'model_available': self.llm_client.is_model_available(),
            'config': {
                'model': self.config.get('model.name'),
                'threshold': self.config.get('capture.importance_threshold'),
                'batch_size': self.config.get('performance.batch_size'),
            }
        }

    def stop(self) -> None:
        """Stop the processor gracefully."""
        self.running = False


def main():
    """Entry point for running processor as a module."""
    import argparse

    parser = argparse.ArgumentParser(description="Context Capture Background Processor")
    parser.add_argument('--project-dir', type=str, help='Project directory')
    parser.add_argument('--debug', action='store_true', help='Enable debug mode')

    args = parser.parse_args()

    # Set project directory
    project_dir = Path(args.project_dir) if args.project_dir else Path.cwd()

    # Create config and enable debug if requested
    config = Config(project_dir=project_dir)
    if args.debug:
        config.set('features.debug_mode', True)

    # Create and run processor
    processor = ContextProcessor(config=config, project_dir=project_dir)

    try:
        processor.run_forever()
    except KeyboardInterrupt:
        print("\nShutting down processor...")
        processor.stop()


if __name__ == '__main__':
    main()