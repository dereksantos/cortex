"""
Queue management for context capture system.
"""

import json
import shutil
import time
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any, Dict, Iterator, List, Optional

from context_capture.utils.config import Config


class QueueManager:
    """Manages the file-based queue system."""

    def __init__(self, config: Optional[Config] = None):
        """
        Initialize queue manager.

        Args:
            config: Configuration object
        """
        self.config = config or Config()
        self.config.ensure_directories()

    @property
    def pending_dir(self) -> Path:
        """Get pending queue directory."""
        return self.config.queue_dir / 'pending'

    @property
    def processing_dir(self) -> Path:
        """Get processing queue directory."""
        return self.config.queue_dir / 'processing'

    @property
    def processed_dir(self) -> Path:
        """Get processed queue directory."""
        return self.config.queue_dir / 'processed'

    def get_pending_events(self, limit: Optional[int] = None) -> List[Path]:
        """
        Get list of pending event files.

        Args:
            limit: Maximum number of files to return

        Returns:
            List of pending event file paths, sorted by creation time
        """
        try:
            files = [f for f in self.pending_dir.glob('*.json') if f.is_file()]
            files.sort(key=lambda f: f.stat().st_mtime)

            if limit:
                files = files[:limit]

            return files
        except Exception:
            return []

    def move_to_processing(self, file_path: Path) -> Optional[Path]:
        """
        Move file from pending to processing.

        Args:
            file_path: Path to pending file

        Returns:
            Path to file in processing directory, or None if failed
        """
        try:
            processing_path = self.processing_dir / file_path.name
            shutil.move(str(file_path), str(processing_path))
            return processing_path
        except Exception:
            return None

    def move_to_processed(self, file_path: Path) -> Optional[Path]:
        """
        Move file from processing to processed.

        Args:
            file_path: Path to processing file

        Returns:
            Path to file in processed directory, or None if failed
        """
        try:
            processed_path = self.processed_dir / file_path.name
            shutil.move(str(file_path), str(processed_path))
            return processed_path
        except Exception:
            return None

    def move_back_to_pending(self, file_path: Path) -> Optional[Path]:
        """
        Move file from processing back to pending (on error).

        Args:
            file_path: Path to processing file

        Returns:
            Path to file in pending directory, or None if failed
        """
        try:
            pending_path = self.pending_dir / file_path.name
            shutil.move(str(file_path), str(pending_path))
            return pending_path
        except Exception:
            return None

    def load_event(self, file_path: Path) -> Optional[Dict[str, Any]]:
        """
        Load event data from file.

        Args:
            file_path: Path to event file

        Returns:
            Event data dictionary, or None if failed
        """
        try:
            with open(file_path, 'r') as f:
                return json.load(f)
        except Exception:
            return None

    def save_event(self, file_path: Path, event_data: Dict[str, Any]) -> bool:
        """
        Save event data to file.

        Args:
            file_path: Path to event file
            event_data: Event data to save

        Returns:
            True if successful, False otherwise
        """
        try:
            temp_path = file_path.with_suffix('.tmp')
            with open(temp_path, 'w') as f:
                json.dump(event_data, f, indent=2)
            temp_path.rename(file_path)
            return True
        except Exception:
            return False

    def get_queue_stats(self) -> Dict[str, Any]:
        """
        Get queue statistics.

        Returns:
            Dictionary with queue statistics
        """
        try:
            def count_and_size(directory: Path) -> tuple[int, int]:
                files = list(directory.glob('*.json'))
                size = sum(f.stat().st_size for f in files if f.is_file())
                return len(files), size

            pending_count, pending_size = count_and_size(self.pending_dir)
            processing_count, processing_size = count_and_size(self.processing_dir)
            processed_count, processed_size = count_and_size(self.processed_dir)

            return {
                'pending': {
                    'count': pending_count,
                    'size_bytes': pending_size,
                    'size_mb': round(pending_size / 1024 / 1024, 2)
                },
                'processing': {
                    'count': processing_count,
                    'size_bytes': processing_size,
                    'size_mb': round(processing_size / 1024 / 1024, 2)
                },
                'processed': {
                    'count': processed_count,
                    'size_bytes': processed_size,
                    'size_mb': round(processed_size / 1024 / 1024, 2)
                },
                'total': {
                    'count': pending_count + processing_count + processed_count,
                    'size_bytes': pending_size + processing_size + processed_size,
                    'size_mb': round((pending_size + processing_size + processed_size) / 1024 / 1024, 2)
                }
            }
        except Exception:
            return {}

    def cleanup_old_processed(self, max_age_hours: Optional[int] = None) -> int:
        """
        Clean up old processed files.

        Args:
            max_age_hours: Maximum age in hours (uses config if None)

        Returns:
            Number of files cleaned up
        """
        if max_age_hours is None:
            retention_days = self.config.get('storage.retention_days', 30)
            max_age_hours = retention_days * 24

        cutoff_time = datetime.now() - timedelta(hours=max_age_hours)
        cleaned_count = 0

        try:
            for file_path in self.processed_dir.glob('*.json'):
                if file_path.is_file():
                    file_time = datetime.fromtimestamp(file_path.stat().st_mtime)
                    if file_time < cutoff_time:
                        file_path.unlink()
                        cleaned_count += 1
        except Exception:
            pass

        return cleaned_count

    def batch_process(self, batch_size: Optional[int] = None) -> Iterator[List[Dict[str, Any]]]:
        """
        Generator that yields batches of events for processing.

        Args:
            batch_size: Size of each batch (uses config if None)

        Yields:
            Lists of event dictionaries
        """
        if batch_size is None:
            batch_size = self.config.get('performance.batch_size', 5)

        while True:
            pending_files = self.get_pending_events(limit=batch_size)
            if not pending_files:
                break

            batch = []
            processing_files = []

            for file_path in pending_files:
                # Move to processing
                processing_path = self.move_to_processing(file_path)
                if processing_path:
                    event_data = self.load_event(processing_path)
                    if event_data:
                        event_data['_file_path'] = processing_path
                        batch.append(event_data)
                        processing_files.append(processing_path)

            if batch:
                yield batch

            # Move processed files
            for file_path in processing_files:
                self.move_to_processed(file_path)

    def emergency_cleanup(self) -> Dict[str, int]:
        """
        Emergency cleanup of stuck processing files.

        Returns:
            Dictionary with cleanup statistics
        """
        stats = {'moved_back': 0, 'removed_corrupted': 0}

        try:
            # Move old processing files back to pending
            cutoff_time = datetime.now() - timedelta(minutes=30)

            for file_path in self.processing_dir.glob('*.json'):
                if file_path.is_file():
                    file_time = datetime.fromtimestamp(file_path.stat().st_mtime)
                    if file_time < cutoff_time:
                        # Try to load and validate
                        event_data = self.load_event(file_path)
                        if event_data:
                            self.move_back_to_pending(file_path)
                            stats['moved_back'] += 1
                        else:
                            # Corrupted file
                            file_path.unlink()
                            stats['removed_corrupted'] += 1

        except Exception:
            pass

        return stats