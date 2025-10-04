"""
Context injection for enhancing agent requests with relevant knowledge.

The ContextInjector formats and injects retrieved context into agent
requests in a way that enhances their effectiveness without overwhelming them.
"""

import logging
import time
from typing import Any, Dict, List, Optional
from datetime import datetime

logger = logging.getLogger(__name__)


class ContextInjector:
    """
    Injects relevant context into agent requests and prompts.

    Formats retrieved context in a way that's helpful to agents without
    overwhelming them or exceeding token limits.
    """

    def __init__(
        self,
        max_tokens: int = 2000,
        context_template: Optional[str] = None,
        include_metadata: bool = True
    ):
        """
        Initialize the context injector.

        Args:
            max_tokens: Maximum tokens to use for context injection
            context_template: Custom template for formatting context
            include_metadata: Whether to include metadata in injected context
        """
        self.max_tokens = max_tokens
        self.include_metadata = include_metadata

        # Default context template
        self.context_template = context_template or """
# Relevant Context

{summary}

## Context Details

{context_items}

---

{original_request}
"""

        logger.debug(f"Context injector initialized with max_tokens={max_tokens}")

    def inject_context(
        self,
        original_request: str,
        context_data: Dict[str, Any],
        include_summary: bool = True
    ) -> str:
        """
        Inject context into an agent request.

        Args:
            original_request: The original agent request or prompt
            context_data: Context data from the broker
            include_summary: Whether to include the context summary

        Returns:
            Enhanced request with context injected
        """
        try:
            # Extract context information
            contexts = context_data.get("contexts", [])
            summary = context_data.get("summary", "")
            metadata = context_data.get("metadata", {})

            if not contexts:
                logger.debug("No context to inject")
                return original_request

            # Format context items within token limit
            formatted_context = self._format_context_items(contexts)

            # Build the context section
            context_section = ""

            if include_summary and summary:
                context_section += f"## Summary\n{summary}\n\n"

            if formatted_context:
                context_section += f"## Relevant Information\n{formatted_context}\n\n"

            # Add metadata if requested
            if self.include_metadata and metadata:
                query = metadata.get("query", "")
                total_results = metadata.get("total_results", 0)
                if query and total_results:
                    context_section += f"*Context retrieved for: \"{query}\" ({total_results} items)*\n\n"

            # Inject context before the original request
            if context_section.strip():
                enhanced_request = f"""# Relevant Context

{context_section.strip()}

---

# Your Task

{original_request}"""

                logger.debug(f"Injected context from {len(contexts)} items")
                return enhanced_request
            else:
                return original_request

        except Exception as e:
            logger.error(f"Error injecting context: {e}")
            return original_request

    def inject_context_minimal(
        self,
        original_request: str,
        context_data: Dict[str, Any],
        max_items: int = 2
    ) -> str:
        """
        Inject context in a minimal format for token-constrained situations.

        Args:
            original_request: The original request
            context_data: Context data from the broker
            max_items: Maximum context items to include

        Returns:
            Enhanced request with minimal context
        """
        try:
            contexts = context_data.get("contexts", [])[:max_items]

            if not contexts:
                return original_request

            # Create very concise context
            context_snippets = []
            for ctx in contexts:
                content = ctx.get("content", "")
                source = ctx.get("source", "")

                # Take only the first 100 characters
                snippet = content[:100] + "..." if len(content) > 100 else content
                context_snippets.append(f"- {snippet}")

            if context_snippets:
                context_text = "\n".join(context_snippets)
                enhanced_request = f"""Context: {context_text}

{original_request}"""
                return enhanced_request
            else:
                return original_request

        except Exception as e:
            logger.error(f"Error injecting minimal context: {e}")
            return original_request

    def inject_context_for_claude_code(
        self,
        original_request: str,
        context_data: Dict[str, Any],
        project_context: bool = True
    ) -> str:
        """
        Inject context specifically formatted for Claude Code agents.

        Args:
            original_request: The original request
            context_data: Context data from the broker
            project_context: Whether to include project-specific context

        Returns:
            Enhanced request formatted for Claude Code
        """
        try:
            contexts = context_data.get("contexts", [])
            summary = context_data.get("summary", "")

            if not contexts:
                return original_request

            # Group contexts by type
            context_by_type = {}
            for ctx in contexts:
                ctx_type = ctx.get("type", "general")
                if ctx_type not in context_by_type:
                    context_by_type[ctx_type] = []
                context_by_type[ctx_type].append(ctx)

            # Build context sections
            context_sections = []

            if summary:
                context_sections.append(f"## Context Summary\n{summary}")

            # Add type-specific sections
            type_names = {
                "decision": "Architectural Decisions",
                "insight": "Key Insights",
                "pattern": "Development Patterns",
                "strategy": "Strategies & Approaches",
                "general": "Additional Context"
            }

            for ctx_type, items in context_by_type.items():
                if not items:
                    continue

                section_name = type_names.get(ctx_type, ctx_type.title())
                context_sections.append(f"## {section_name}")

                for item in items[:2]:  # Limit to 2 items per type
                    content = item.get("content", "")
                    source = item.get("source", "")
                    timestamp = item.get("timestamp", "")

                    # Format content for Claude Code
                    formatted_content = self._format_for_claude_code(content, source, timestamp)
                    context_sections.append(formatted_content)

            # Combine everything
            if context_sections:
                context_text = "\n\n".join(context_sections)
                enhanced_request = f"""# Project Context

{context_text}

---

# Current Request

{original_request}

*Note: The above context is automatically retrieved from your project's captured knowledge.*"""

                return enhanced_request
            else:
                return original_request

        except Exception as e:
            logger.error(f"Error injecting Claude Code context: {e}")
            return original_request

    def _format_context_items(self, contexts: List[Dict[str, Any]]) -> str:
        """Format context items for injection."""
        if not contexts:
            return ""

        formatted_items = []
        current_tokens = 0
        estimated_token_limit = self.max_tokens * 0.8  # Leave room for other content

        for i, ctx in enumerate(contexts):
            content = ctx.get("content", "")
            source = ctx.get("source", "")
            similarity = ctx.get("similarity", 0)
            timestamp = ctx.get("timestamp", "")

            # Estimate tokens (rough approximation: 1 token per 4 characters)
            content_tokens = len(content) / 4

            if current_tokens + content_tokens > estimated_token_limit:
                # Truncate content to fit
                remaining_tokens = estimated_token_limit - current_tokens
                max_chars = int(remaining_tokens * 4)
                if max_chars > 100:  # Only include if we have reasonable space
                    content = content[:max_chars] + "..."
                else:
                    break  # No more room

            # Format the item
            formatted_item = self._format_single_context_item(
                content, source, similarity, timestamp, i + 1
            )

            formatted_items.append(formatted_item)
            current_tokens += len(formatted_item) / 4

            # Stop if we're approaching the limit
            if current_tokens > estimated_token_limit:
                break

        return "\n\n".join(formatted_items)

    def _format_single_context_item(
        self,
        content: str,
        source: str,
        similarity: float,
        timestamp: Any,
        index: int
    ) -> str:
        """Format a single context item."""
        # Format timestamp
        time_str = ""
        if timestamp:
            try:
                if isinstance(timestamp, (int, float)):
                    time_str = datetime.fromtimestamp(timestamp).strftime("%Y-%m-%d")
                else:
                    time_str = str(timestamp)[:10]  # First 10 chars
            except:
                pass

        # Format source
        source_str = source.split("/")[-1] if "/" in source else source

        # Create header
        header_parts = [f"### Context {index}"]
        if source_str:
            header_parts.append(f"*Source: {source_str}*")
        if time_str:
            header_parts.append(f"*Date: {time_str}*")
        if similarity > 0:
            header_parts.append(f"*Relevance: {similarity:.1%}*")

        header = " - ".join(header_parts)

        return f"{header}\n\n{content.strip()}"

    def _format_for_claude_code(self, content: str, source: str, timestamp: Any) -> str:
        """Format context specifically for Claude Code."""
        # Extract filename from source
        filename = source.split("/")[-1] if "/" in source else source

        # Format timestamp
        time_str = ""
        if timestamp:
            try:
                if isinstance(timestamp, (int, float)):
                    time_str = datetime.fromtimestamp(timestamp).strftime("%Y-%m-%d %H:%M")
                else:
                    time_str = str(timestamp)
            except:
                pass

        # Create a code-friendly format
        header = f"**{filename}**"
        if time_str:
            header += f" _{time_str}_"

        # Format content with proper indentation if it looks like code
        if any(indicator in content for indicator in ["```", "def ", "class ", "function", "const ", "let "]):
            # Preserve code formatting
            formatted_content = content
        else:
            # Regular text, ensure proper paragraph breaks
            formatted_content = content.replace("\n\n", "\n\n").strip()

        return f"{header}\n\n{formatted_content}"

    def estimate_injection_tokens(self, context_data: Dict[str, Any]) -> int:
        """
        Estimate how many tokens the context injection will use.

        Args:
            context_data: Context data from the broker

        Returns:
            Estimated token count
        """
        try:
            contexts = context_data.get("contexts", [])
            summary = context_data.get("summary", "")

            total_chars = 0

            # Count summary
            if summary:
                total_chars += len(summary)

            # Count context items
            for ctx in contexts:
                content = ctx.get("content", "")
                total_chars += len(content)

            # Add overhead for formatting
            overhead_chars = len(contexts) * 100  # Estimated formatting overhead
            total_chars += overhead_chars

            # Convert to estimated tokens (1 token ≈ 4 characters)
            estimated_tokens = total_chars / 4

            return int(estimated_tokens)

        except Exception as e:
            logger.error(f"Error estimating tokens: {e}")
            return 0

    def __repr__(self) -> str:
        """String representation of the injector."""
        return f"ContextInjector(max_tokens={self.max_tokens})"