"""
Command-line interface for the Context Capture system.

Provides CLI commands for interacting with the Context Broker and other
components of the agentic context capture system.
"""

from context_capture.cli.broker_commands import BrokerCLI, main

__all__ = [
    "BrokerCLI",
    "main",
]