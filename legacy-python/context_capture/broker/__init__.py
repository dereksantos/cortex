"""
Context Broker module for intelligent context retrieval and injection.

The Context Broker acts as a "librarian" that can quickly find and organize
relevant context from the captured knowledge base for any request.
"""

from context_capture.broker.core import ContextBroker
from context_capture.broker.search import SemanticSearchEngine
from context_capture.broker.injection import ContextInjector

__all__ = [
    "ContextBroker",
    "SemanticSearchEngine",
    "ContextInjector",
]