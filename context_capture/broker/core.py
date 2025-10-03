"""
Core Context Broker implementation.

The Context Broker acts as an intelligent librarian that can quickly find
and organize relevant context from captured knowledge for any request.
"""

import logging
import time
from typing import Any, Dict, List, Optional, Set
from pathlib import Path

from context_capture.providers.router import ProviderRouter
from context_capture.providers.base import PrivacyLevel
from context_capture.broker.search import SemanticSearchEngine
from context_capture.broker.injection import ContextInjector

logger = logging.getLogger(__name__)


class ContextBroker:
    """
    Intelligent context broker for retrieving and organizing relevant knowledge.

    Acts as a "librarian" that can instantly find relevant context from the
    captured knowledge base for any request or query.
    """

    def __init__(
        self,
        provider_router: Optional[ProviderRouter] = None,
        knowledge_base_path: Optional[Path] = None,
        privacy_level: PrivacyLevel = PrivacyLevel.LOCAL,
        max_context_tokens: int = 2000
    ):
        """
        Initialize the Context Broker.

        Args:
            provider_router: Router for model providers
            knowledge_base_path: Path to captured knowledge
            privacy_level: Required privacy level for operations
            max_context_tokens: Maximum tokens to inject as context
        """
        self.provider_router = provider_router or ProviderRouter.create_default()
        self.knowledge_base_path = knowledge_base_path or Path(".context")
        self.privacy_level = privacy_level
        self.max_context_tokens = max_context_tokens

        # Initialize components
        self.search_engine = SemanticSearchEngine(
            knowledge_base_path=self.knowledge_base_path,
            provider_router=self.provider_router
        )
        self.injector = ContextInjector(max_tokens=max_context_tokens)

        # Cache for recent queries
        self._query_cache: Dict[str, Any] = {}
        self._cache_ttl = 300  # 5 minutes
        self._last_index_update = 0
        self._index_update_interval = 3600  # 1 hour

        logger.info(f"Context broker initialized with knowledge base: {self.knowledge_base_path}")

    def get_relevant_context(
        self,
        query: str,
        context_type: str = "general",
        max_results: int = 5,
        similarity_threshold: float = 0.7
    ) -> Dict[str, Any]:
        """
        Get relevant context for a query or request.

        Args:
            query: The query or request to find context for
            context_type: Type of context needed (general, technical, decision, etc.)
            max_results: Maximum number of context items to return
            similarity_threshold: Minimum similarity score for results

        Returns:
            Dictionary with relevant context and metadata
        """
        try:
            start_time = time.time()

            # Check cache first
            cache_key = f"{query}:{context_type}:{max_results}"
            if cache_key in self._query_cache:
                cached_result = self._query_cache[cache_key]
                if time.time() - cached_result["timestamp"] < self._cache_ttl:
                    logger.debug(f"Returning cached result for: {query[:50]}...")
                    return cached_result["result"]

            # Update search index if needed
            self._maybe_update_search_index()

            # Search for relevant context
            search_results = self.search_engine.search(
                query=query,
                max_results=max_results * 2,  # Get more to filter
                context_type=context_type
            )

            # Filter by similarity threshold
            filtered_results = [
                result for result in search_results
                if result.get("similarity", 0) >= similarity_threshold
            ][:max_results]

            # Organize and structure the context
            structured_context = self._structure_context(filtered_results, query, context_type)

            # Cache the result
            self._query_cache[cache_key] = {
                "result": structured_context,
                "timestamp": time.time()
            }

            # Clean old cache entries
            self._clean_cache()

            elapsed_time = time.time() - start_time
            logger.info(f"Retrieved {len(filtered_results)} context items for '{query[:50]}...' in {elapsed_time:.3f}s")

            return structured_context

        except Exception as e:
            logger.error(f"Error getting relevant context: {e}")
            return {
                "contexts": [],
                "summary": "Unable to retrieve context due to an error.",
                "metadata": {
                    "error": str(e),
                    "query": query,
                    "context_type": context_type
                }
            }

    def inject_context_for_agent(
        self,
        agent_request: str,
        agent_type: str = "general",
        include_summary: bool = True
    ) -> str:
        """
        Inject relevant context directly into an agent request.

        Args:
            agent_request: The original agent request or prompt
            agent_type: Type of agent making the request
            include_summary: Whether to include a context summary

        Returns:
            Enhanced request with relevant context injected
        """
        try:
            # Get relevant context
            context_data = self.get_relevant_context(
                query=agent_request,
                context_type=agent_type,
                max_results=3  # Fewer results for injection
            )

            # Inject context into the request
            enhanced_request = self.injector.inject_context(
                original_request=agent_request,
                context_data=context_data,
                include_summary=include_summary
            )

            logger.debug(f"Injected context for agent type '{agent_type}'")
            return enhanced_request

        except Exception as e:
            logger.error(f"Error injecting context: {e}")
            return agent_request  # Return original if injection fails

    def update_knowledge_base(self, force: bool = False) -> bool:
        """
        Update the knowledge base index from captured content.

        Args:
            force: Force update even if recently updated

        Returns:
            True if update was performed
        """
        try:
            current_time = time.time()
            if not force and (current_time - self._last_index_update) < self._index_update_interval:
                return False

            logger.info("Updating knowledge base index...")
            updated = self.search_engine.update_index()

            if updated:
                self._last_index_update = current_time
                self._clear_cache()  # Clear cache after index update
                logger.info("Knowledge base index updated successfully")

            return updated

        except Exception as e:
            logger.error(f"Error updating knowledge base: {e}")
            return False

    def get_broker_status(self) -> Dict[str, Any]:
        """
        Get status information about the context broker.

        Returns:
            Status dictionary with broker health and metrics
        """
        try:
            search_stats = self.search_engine.get_statistics()
            provider_status = self.provider_router.get_provider_status()

            return {
                "status": "healthy",
                "knowledge_base_path": str(self.knowledge_base_path),
                "privacy_level": self.privacy_level.value,
                "max_context_tokens": self.max_context_tokens,
                "cache_size": len(self._query_cache),
                "last_index_update": self._last_index_update,
                "search_engine": search_stats,
                "providers": provider_status,
                "timestamp": time.time()
            }

        except Exception as e:
            logger.error(f"Error getting broker status: {e}")
            return {
                "status": "error",
                "error": str(e),
                "timestamp": time.time()
            }

    def clear_cache(self) -> None:
        """Clear the query cache."""
        self._clear_cache()
        logger.info("Context broker cache cleared")

    def _structure_context(
        self,
        search_results: List[Dict[str, Any]],
        query: str,
        context_type: str
    ) -> Dict[str, Any]:
        """Structure search results into organized context."""
        if not search_results:
            return {
                "contexts": [],
                "summary": "No relevant context found.",
                "metadata": {
                    "query": query,
                    "context_type": context_type,
                    "total_results": 0
                }
            }

        # Organize results by type and recency
        organized_contexts = []
        for result in search_results:
            organized_contexts.append({
                "content": result.get("content", ""),
                "source": result.get("source", "unknown"),
                "timestamp": result.get("timestamp", ""),
                "similarity": result.get("similarity", 0.0),
                "type": result.get("type", "general"),
                "metadata": result.get("metadata", {})
            })

        # Generate a summary using the broker model
        summary = self._generate_context_summary(organized_contexts, query, context_type)

        return {
            "contexts": organized_contexts,
            "summary": summary,
            "metadata": {
                "query": query,
                "context_type": context_type,
                "total_results": len(organized_contexts),
                "generated_at": time.time()
            }
        }

    def _generate_context_summary(
        self,
        contexts: List[Dict[str, Any]],
        query: str,
        context_type: str
    ) -> str:
        """Generate a summary of the retrieved context."""
        if not contexts:
            return "No relevant context available."

        try:
            # Create a prompt for summarizing the context
            context_texts = [ctx["content"][:200] + "..." if len(ctx["content"]) > 200
                           else ctx["content"] for ctx in contexts[:3]]

            prompt = f"""Based on the following context items related to "{query}", provide a brief summary of the most relevant information:

Context items:
{chr(10).join(f"- {text}" for text in context_texts)}

Provide a concise 2-3 sentence summary of the key relevant information."""

            # Use a fast model for summarization
            summary = self.provider_router.generate_with_fallback(
                prompt=prompt,
                task_type="broker",  # Use fast model for summaries
                max_tokens=100,
                timeout=10
            )

            return summary or "Context available but unable to generate summary."

        except Exception as e:
            logger.warning(f"Error generating context summary: {e}")
            return f"Found {len(contexts)} relevant context items."

    def _maybe_update_search_index(self) -> None:
        """Update search index if enough time has passed."""
        current_time = time.time()
        if (current_time - self._last_index_update) > self._index_update_interval:
            self.update_knowledge_base()

    def _clear_cache(self) -> None:
        """Clear the query cache."""
        self._query_cache.clear()

    def _clean_cache(self) -> None:
        """Remove expired entries from cache."""
        current_time = time.time()
        expired_keys = [
            key for key, value in self._query_cache.items()
            if (current_time - value["timestamp"]) > self._cache_ttl
        ]
        for key in expired_keys:
            del self._query_cache[key]

        if expired_keys:
            logger.debug(f"Cleaned {len(expired_keys)} expired cache entries")

    def __repr__(self) -> str:
        """String representation of the broker."""
        return f"ContextBroker(knowledge_base={self.knowledge_base_path}, privacy={self.privacy_level.value})"