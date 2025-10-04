"""
Agentic Context Capture System

An intelligent, privacy-first system for automatically capturing and organizing
development insights from Claude Code sessions using local LLMs.

Never lose an architectural decision, development pattern, or strategic insight again.
"""

__version__ = "0.1.0"
__author__ = "Derek Santos"
__email__ = "derek@example.com"

from context_capture.core.capture import ContextCapture
from context_capture.core.processor import ContextProcessor
from context_capture.core.queue import QueueManager
from context_capture.utils.config import Config
from context_capture.utils.status import StatusMonitor

__all__ = [
    "ContextCapture",
    "ContextProcessor",
    "QueueManager",
    "Config",
    "StatusMonitor",
]