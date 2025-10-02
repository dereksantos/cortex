"""
Fast event capture for Claude Code hooks.
Captures events to file queue in < 50ms.
"""

import json
import os
import sys
import time
import uuid
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, Optional

from context_capture.utils.config import Config


class ContextCapture:
    """Fast event capture system for Claude Code hooks."""

    def __init__(self, config: Optional[Config] = None, project_dir: Optional[Path] = None):
        """
        Initialize context capture.

        Args:
            config: Configuration object
            project_dir: Project directory for context storage
        """
        self.project_dir = project_dir or Path.cwd()
        self.config = config or Config(project_dir=self.project_dir)
        self.config.ensure_directories()

    def quick_filter(self, event_data: Dict[str, Any]) -> bool:
        """
        Quick pre-filter to skip obvious noise.

        Args:
            event_data: Event data from Claude Code hook

        Returns:
            True if event should be skipped, False otherwise
        """
        tool_name = event_data.get('tool_name', '')
        tool_input = event_data.get('tool_input', {})

        # Get skip patterns from config
        skip_patterns = self.config.get('capture.skip_patterns', [])

        # Skip file operations in ignored directories
        if tool_name in ['Read', 'Grep', 'Glob']:
            file_path = str(tool_input.get('file_path', ''))
            pattern = str(tool_input.get('pattern', ''))
            path = str(tool_input.get('path', ''))

            for skip in skip_patterns:
                if skip in file_path or skip in pattern or skip in path:
                    return True

        # Skip routine bash commands
        if tool_name == 'Bash':
            command = str(tool_input.get('command', '')).lower()
            routine_commands = ['ls', 'pwd', 'echo', 'cd', 'which', 'date', 'cat', 'head', 'tail']
            if command and any(cmd == command.split()[0] for cmd in routine_commands):
                return True

        # Skip based on tool result content
        tool_result = str(event_data.get('tool_result', ''))
        for skip in skip_patterns:
            if skip in tool_result:
                return True

        return False

    def capture_event(self, event_data: Optional[Dict[str, Any]] = None) -> bool:
        """
        Main capture function - must be fast!

        Args:
            event_data: Event data (if None, reads from stdin)

        Returns:
            True if event was captured, False if skipped or failed
        """
        try:
            # Read event data
            if event_data is None:
                raw_data = sys.stdin.read()
                if not raw_data:
                    return False
                event_data = json.loads(raw_data)

            # Quick filter
            if self.quick_filter(event_data):
                return False

            # Create event metadata
            timestamp = time.time()
            event_id = f"{datetime.now().strftime('%Y%m%d-%H%M%S')}-{uuid.uuid4().hex[:8]}"

            # Prepare event data (truncate large results)
            tool_result = event_data.get('tool_result', '')
            truncate_length = self.config.get('capture.truncate_length', 2000)

            if isinstance(tool_result, str) and len(tool_result) > truncate_length:
                tool_result = tool_result[:truncate_length] + '... [truncated]'

            # Build event object
            event = {
                'id': event_id,
                'timestamp': timestamp,
                'datetime': datetime.now().isoformat(),
                'tool_name': event_data.get('tool_name', ''),
                'tool_input': event_data.get('tool_input', {}),
                'tool_result_preview': tool_result,
                'project': str(self.project_dir),
                'session_id': os.environ.get('CLAUDE_SESSION_ID', 'unknown'),
                'user': os.environ.get('USER', 'unknown'),
                'version': '0.1.0'
            }

            # Write to queue (atomic operation)
            self._write_to_queue(event, event_id)
            return True

        except Exception as e:
            self._log_error(f"Capture error: {str(e)}")
            return False

    def _write_to_queue(self, event: Dict[str, Any], event_id: str) -> None:
        """Write event to queue with atomic operation."""
        queue_file = self.config.queue_dir / 'pending' / f"{event_id}.json"
        temp_file = queue_file.with_suffix('.tmp')

        # Write to temporary file first
        with open(temp_file, 'w') as f:
            json.dump(event, f, indent=2)

        # Atomic rename
        temp_file.rename(queue_file)

    def _log_error(self, message: str) -> None:
        """Log error without interrupting Claude Code."""
        try:
            error_log = self.config.logs_dir / 'capture_errors.log'
            with open(error_log, 'a') as f:
                f.write(f"{datetime.now().isoformat()} - {message}\n")
        except:
            # Silent failure - don't interrupt Claude Code
            pass

    @classmethod
    def from_stdin(cls, project_dir: Optional[Path] = None) -> bool:
        """
        Capture event from stdin (for Claude Code hooks).

        Args:
            project_dir: Project directory (detected from environment if None)

        Returns:
            True if event was captured successfully
        """
        # Detect project directory from environment
        if project_dir is None:
            project_dir = Path(os.environ.get('CLAUDE_PROJECT_DIR', os.getcwd()))

        capture = cls(project_dir=project_dir)
        return capture.capture_event()


def main():
    """Entry point for hook script."""
    try:
        success = ContextCapture.from_stdin()
        sys.exit(0 if success else 1)
    except Exception:
        # Silent failure - don't interrupt Claude Code
        sys.exit(0)


if __name__ == '__main__':
    main()