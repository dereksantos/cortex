"""
Semantic search engine for the Context Broker.

Provides intelligent search capabilities over captured knowledge using
vector embeddings and similarity matching.
"""

import json
import logging
import time
import hashlib
from typing import Any, Dict, List, Optional, Set, Tuple
from pathlib import Path
from datetime import datetime

from context_capture.providers.router import ProviderRouter

logger = logging.getLogger(__name__)


class SemanticSearchEngine:
    """
    Semantic search engine for captured knowledge.

    Uses text embeddings and similarity scoring to find relevant context
    from the captured knowledge base.
    """

    def __init__(
        self,
        knowledge_base_path: Path,
        provider_router: ProviderRouter,
        index_file: str = "search_index.json",
        embedding_cache_file: str = "embeddings_cache.json"
    ):
        """
        Initialize the semantic search engine.

        Args:
            knowledge_base_path: Path to captured knowledge
            provider_router: Router for model providers
            index_file: Name of the search index file
            embedding_cache_file: Name of embedding cache file
        """
        self.knowledge_base_path = knowledge_base_path
        self.provider_router = provider_router
        self.index_file = knowledge_base_path / index_file
        self.embedding_cache_file = knowledge_base_path / embedding_cache_file

        # Search index and embeddings
        self._search_index: List[Dict[str, Any]] = []
        self._embedding_cache: Dict[str, List[float]] = {}
        self._index_loaded = False
        self._last_update = 0

        # Ensure knowledge base directory exists
        self.knowledge_base_path.mkdir(parents=True, exist_ok=True)

        logger.info(f"Search engine initialized for: {knowledge_base_path}")

    def search(
        self,
        query: str,
        max_results: int = 10,
        context_type: str = "general",
        min_similarity: float = 0.5
    ) -> List[Dict[str, Any]]:
        """
        Search for relevant context using semantic similarity.

        Args:
            query: Search query
            max_results: Maximum number of results to return
            context_type: Type of context to search for
            min_similarity: Minimum similarity score

        Returns:
            List of search results with similarity scores
        """
        try:
            start_time = time.time()

            # Ensure index is loaded
            if not self._index_loaded:
                self._load_index()

            if not self._search_index:
                logger.warning("Search index is empty")
                return []

            # Get query embedding
            query_embedding = self._get_text_embedding(query)
            if not query_embedding:
                logger.error("Failed to get query embedding")
                return []

            # Calculate similarities
            results = []
            for item in self._search_index:
                # Filter by context type if specified
                if context_type != "general" and item.get("type") != context_type:
                    continue

                item_embedding = item.get("embedding")
                if not item_embedding:
                    continue

                similarity = self._calculate_similarity(query_embedding, item_embedding)
                if similarity >= min_similarity:
                    result = item.copy()
                    result["similarity"] = similarity
                    results.append(result)

            # Sort by similarity and recency
            results.sort(key=lambda x: (x["similarity"], x.get("timestamp", 0)), reverse=True)

            # Return top results
            final_results = results[:max_results]

            elapsed_time = time.time() - start_time
            logger.debug(f"Search for '{query[:50]}...' returned {len(final_results)} results in {elapsed_time:.3f}s")

            return final_results

        except Exception as e:
            logger.error(f"Error during search: {e}")
            return []

    def update_index(self) -> bool:
        """
        Update the search index from captured knowledge files.

        Returns:
            True if index was updated
        """
        try:
            logger.info("Updating search index...")
            start_time = time.time()

            # Load existing index and cache
            self._load_index()
            self._load_embedding_cache()

            # Find all knowledge files
            knowledge_files = self._find_knowledge_files()
            logger.debug(f"Found {len(knowledge_files)} knowledge files")

            new_items = 0
            updated_items = 0

            for file_path in knowledge_files:
                try:
                    # Process each file
                    file_items = self._process_knowledge_file(file_path)

                    for item in file_items:
                        # Check if item already exists
                        item_id = item.get("id")
                        existing_item = self._find_item_by_id(item_id)

                        if existing_item:
                            # Update existing item if content changed
                            if existing_item.get("content_hash") != item.get("content_hash"):
                                self._update_item_in_index(item)
                                updated_items += 1
                        else:
                            # Add new item
                            self._add_item_to_index(item)
                            new_items += 1

                except Exception as e:
                    logger.warning(f"Error processing {file_path}: {e}")

            # Save updated index and cache
            self._save_index()
            self._save_embedding_cache()

            self._last_update = time.time()
            elapsed_time = time.time() - start_time

            logger.info(f"Index updated: {new_items} new, {updated_items} updated in {elapsed_time:.3f}s")
            return new_items > 0 or updated_items > 0

        except Exception as e:
            logger.error(f"Error updating index: {e}")
            return False

    def get_statistics(self) -> Dict[str, Any]:
        """
        Get search engine statistics.

        Returns:
            Dictionary with statistics
        """
        if not self._index_loaded:
            self._load_index()

        return {
            "total_items": len(self._search_index),
            "cached_embeddings": len(self._embedding_cache),
            "last_update": self._last_update,
            "knowledge_base_path": str(self.knowledge_base_path),
            "index_size_kb": self._get_file_size(self.index_file) / 1024 if self.index_file.exists() else 0
        }

    def _find_knowledge_files(self) -> List[Path]:
        """Find all knowledge files in the knowledge base."""
        knowledge_files = []

        # Common patterns for knowledge files
        patterns = [
            "**/*.md",
            "**/*.txt",
            "**/*.json",
            "**/decisions/*.md",
            "**/insights/*.md",
            "**/patterns/*.md",
            "**/daily/*.md"
        ]

        for pattern in patterns:
            knowledge_files.extend(self.knowledge_base_path.glob(pattern))

        # Remove duplicates and sort by modification time
        unique_files = list(set(knowledge_files))
        unique_files.sort(key=lambda f: f.stat().st_mtime, reverse=True)

        return unique_files

    def _process_knowledge_file(self, file_path: Path) -> List[Dict[str, Any]]:
        """Process a knowledge file and extract searchable items."""
        try:
            content = file_path.read_text(encoding='utf-8')
            file_stat = file_path.stat()

            items = []

            if file_path.suffix == '.json':
                # Handle JSON files (like captured events)
                try:
                    data = json.loads(content)
                    if isinstance(data, list):
                        for i, item in enumerate(data):
                            if isinstance(item, dict) and item.get("content"):
                                items.append(self._create_search_item(
                                    content=item["content"],
                                    source=str(file_path),
                                    item_type=item.get("type", "event"),
                                    timestamp=item.get("timestamp", file_stat.st_mtime),
                                    metadata=item
                                ))
                    elif isinstance(data, dict) and data.get("content"):
                        items.append(self._create_search_item(
                            content=data["content"],
                            source=str(file_path),
                            item_type=data.get("type", "data"),
                            timestamp=data.get("timestamp", file_stat.st_mtime),
                            metadata=data
                        ))
                except json.JSONDecodeError:
                    pass

            elif file_path.suffix in ['.md', '.txt']:
                # Handle markdown and text files
                # Split content into sections if it's large
                if len(content) > 2000:
                    sections = self._split_content_into_sections(content)
                    for i, section in enumerate(sections):
                        items.append(self._create_search_item(
                            content=section,
                            source=f"{file_path}#section-{i+1}",
                            item_type=self._infer_content_type(file_path, section),
                            timestamp=file_stat.st_mtime,
                            metadata={"file_path": str(file_path), "section": i+1}
                        ))
                else:
                    items.append(self._create_search_item(
                        content=content,
                        source=str(file_path),
                        item_type=self._infer_content_type(file_path, content),
                        timestamp=file_stat.st_mtime,
                        metadata={"file_path": str(file_path)}
                    ))

            return items

        except Exception as e:
            logger.warning(f"Error processing file {file_path}: {e}")
            return []

    def _create_search_item(
        self,
        content: str,
        source: str,
        item_type: str,
        timestamp: float,
        metadata: Optional[Dict[str, Any]] = None
    ) -> Dict[str, Any]:
        """Create a search item with all required fields."""
        content_hash = hashlib.md5(content.encode()).hexdigest()
        item_id = f"{source}:{content_hash[:8]}"

        return {
            "id": item_id,
            "content": content.strip(),
            "content_hash": content_hash,
            "source": source,
            "type": item_type,
            "timestamp": timestamp,
            "metadata": metadata or {},
            "embedding": None  # Will be populated when needed
        }

    def _split_content_into_sections(self, content: str) -> List[str]:
        """Split large content into smaller searchable sections."""
        # Split by headings first
        sections = []
        current_section = ""

        for line in content.split('\n'):
            if line.startswith('#') and current_section.strip():
                # New heading, save current section
                sections.append(current_section.strip())
                current_section = line + '\n'
            else:
                current_section += line + '\n'

            # If section is getting too large, split it
            if len(current_section) > 1500:
                sections.append(current_section.strip())
                current_section = ""

        # Add final section
        if current_section.strip():
            sections.append(current_section.strip())

        # If no headings found, split by paragraphs
        if len(sections) == 1 and len(sections[0]) > 2000:
            paragraphs = sections[0].split('\n\n')
            sections = []
            current_section = ""

            for para in paragraphs:
                if len(current_section + para) > 1500 and current_section:
                    sections.append(current_section.strip())
                    current_section = para + '\n\n'
                else:
                    current_section += para + '\n\n'

            if current_section.strip():
                sections.append(current_section.strip())

        return [s for s in sections if len(s.strip()) > 50]  # Filter out tiny sections

    def _infer_content_type(self, file_path: Path, content: str) -> str:
        """Infer the type of content based on file path and content."""
        path_str = str(file_path).lower()
        content_lower = content.lower()

        # Check path-based types
        if 'decision' in path_str:
            return 'decision'
        elif 'insight' in path_str:
            return 'insight'
        elif 'pattern' in path_str:
            return 'pattern'
        elif 'daily' in path_str:
            return 'daily'
        elif 'strategy' in path_str:
            return 'strategy'

        # Check content-based types
        if any(word in content_lower for word in ['decided', 'decision', 'chose', 'selected']):
            return 'decision'
        elif any(word in content_lower for word in ['insight', 'learned', 'discovered', 'realized']):
            return 'insight'
        elif any(word in content_lower for word in ['pattern', 'recurring', 'repeated', 'consistently']):
            return 'pattern'
        elif any(word in content_lower for word in ['strategy', 'approach', 'methodology', 'framework']):
            return 'strategy'

        return 'general'

    def _get_text_embedding(self, text: str) -> Optional[List[float]]:
        """Get embedding for text using cached or generated embedding."""
        text_hash = hashlib.md5(text.encode()).hexdigest()

        # Check cache first
        if text_hash in self._embedding_cache:
            return self._embedding_cache[text_hash]

        # Generate embedding using a simple method (word frequency)
        # In a real implementation, you'd use a proper embedding model
        embedding = self._generate_simple_embedding(text)

        if embedding:
            self._embedding_cache[text_hash] = embedding

        return embedding

    def _generate_simple_embedding(self, text: str) -> List[float]:
        """
        Generate a simple embedding using word frequency and basic NLP.

        Note: This is a simplified implementation. In production, you'd
        use proper embedding models like sentence-transformers or OpenAI embeddings.
        """
        import re
        from collections import Counter

        # Clean and tokenize text
        words = re.findall(r'\b\w+\b', text.lower())

        # Create a basic vocabulary (top 1000 common programming/context words)
        common_words = [
            'function', 'class', 'method', 'variable', 'code', 'file', 'data', 'system',
            'user', 'application', 'service', 'api', 'database', 'server', 'client',
            'request', 'response', 'error', 'bug', 'fix', 'feature', 'implement',
            'design', 'architecture', 'pattern', 'strategy', 'decision', 'insight',
            'analysis', 'performance', 'security', 'test', 'deploy', 'config',
            'project', 'team', 'development', 'production', 'staging', 'local',
            'environment', 'build', 'compile', 'runtime', 'library', 'framework',
            'component', 'module', 'package', 'dependency', 'version', 'update',
            'create', 'delete', 'modify', 'change', 'add', 'remove', 'replace',
            'optimize', 'refactor', 'migrate', 'upgrade', 'rollback', 'deploy'
        ]

        # Add programming language keywords
        programming_words = [
            'python', 'javascript', 'java', 'go', 'rust', 'typescript', 'sql',
            'html', 'css', 'react', 'node', 'express', 'django', 'flask',
            'docker', 'kubernetes', 'aws', 'azure', 'gcp', 'git', 'github',
            'async', 'await', 'promise', 'callback', 'event', 'loop', 'thread',
            'process', 'memory', 'cache', 'redis', 'postgres', 'mysql', 'mongodb'
        ]

        vocabulary = common_words + programming_words

        # Count word frequencies
        word_counts = Counter(words)

        # Create embedding vector (length = vocabulary size)
        embedding = []
        for word in vocabulary:
            count = word_counts.get(word, 0)
            # Normalize by text length
            frequency = count / len(words) if words else 0
            embedding.append(frequency)

        # Add some text statistics as features
        embedding.extend([
            len(text) / 1000,  # Text length (normalized)
            len(words) / 100,  # Word count (normalized)
            len(set(words)) / len(words) if words else 0,  # Vocabulary diversity
            text.count('\n') / 10,  # Line count (normalized)
            sum(1 for char in text if char.isupper()) / len(text) if text else 0  # Capital ratio
        ])

        return embedding

    def _calculate_similarity(self, embedding1: List[float], embedding2: List[float]) -> float:
        """Calculate cosine similarity between two embeddings."""
        if len(embedding1) != len(embedding2):
            return 0.0

        # Calculate dot product
        dot_product = sum(a * b for a, b in zip(embedding1, embedding2))

        # Calculate magnitudes
        magnitude1 = sum(a * a for a in embedding1) ** 0.5
        magnitude2 = sum(b * b for b in embedding2) ** 0.5

        # Avoid division by zero
        if magnitude1 == 0 or magnitude2 == 0:
            return 0.0

        # Calculate cosine similarity
        similarity = dot_product / (magnitude1 * magnitude2)

        # Ensure result is between 0 and 1
        return max(0.0, min(1.0, similarity))

    def _find_item_by_id(self, item_id: str) -> Optional[Dict[str, Any]]:
        """Find an item in the index by ID."""
        for item in self._search_index:
            if item.get("id") == item_id:
                return item
        return None

    def _add_item_to_index(self, item: Dict[str, Any]) -> None:
        """Add an item to the search index."""
        # Generate embedding if not present
        if not item.get("embedding"):
            item["embedding"] = self._get_text_embedding(item["content"])

        self._search_index.append(item)

    def _update_item_in_index(self, item: Dict[str, Any]) -> None:
        """Update an existing item in the index."""
        for i, existing_item in enumerate(self._search_index):
            if existing_item.get("id") == item.get("id"):
                # Generate new embedding
                item["embedding"] = self._get_text_embedding(item["content"])
                self._search_index[i] = item
                break

    def _load_index(self) -> None:
        """Load the search index from disk."""
        try:
            if self.index_file.exists():
                with open(self.index_file, 'r', encoding='utf-8') as f:
                    data = json.load(f)
                    self._search_index = data.get("items", [])
                    self._last_update = data.get("last_update", 0)
                logger.debug(f"Loaded {len(self._search_index)} items from index")
            else:
                self._search_index = []
                self._last_update = 0
        except Exception as e:
            logger.warning(f"Error loading index: {e}")
            self._search_index = []
            self._last_update = 0

        self._index_loaded = True

    def _save_index(self) -> None:
        """Save the search index to disk."""
        try:
            data = {
                "items": self._search_index,
                "last_update": self._last_update,
                "version": "1.0"
            }
            with open(self.index_file, 'w', encoding='utf-8') as f:
                json.dump(data, f, indent=2)
            logger.debug(f"Saved {len(self._search_index)} items to index")
        except Exception as e:
            logger.error(f"Error saving index: {e}")

    def _load_embedding_cache(self) -> None:
        """Load the embedding cache from disk."""
        try:
            if self.embedding_cache_file.exists():
                with open(self.embedding_cache_file, 'r', encoding='utf-8') as f:
                    self._embedding_cache = json.load(f)
                logger.debug(f"Loaded {len(self._embedding_cache)} cached embeddings")
            else:
                self._embedding_cache = {}
        except Exception as e:
            logger.warning(f"Error loading embedding cache: {e}")
            self._embedding_cache = {}

    def _save_embedding_cache(self) -> None:
        """Save the embedding cache to disk."""
        try:
            with open(self.embedding_cache_file, 'w', encoding='utf-8') as f:
                json.dump(self._embedding_cache, f)
            logger.debug(f"Saved {len(self._embedding_cache)} embeddings to cache")
        except Exception as e:
            logger.error(f"Error saving embedding cache: {e}")

    def _get_file_size(self, file_path: Path) -> int:
        """Get file size in bytes."""
        try:
            return file_path.stat().st_size
        except:
            return 0