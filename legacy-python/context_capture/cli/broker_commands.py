"""
CLI commands for the Context Broker.

Provides command-line interface for interacting with the Context Broker,
including search, status, and configuration operations.
"""

import argparse
import json
import logging
import sys
import time
from pathlib import Path
from typing import Any, Dict, List, Optional

from context_capture.broker.core import ContextBroker
from context_capture.providers.router import ProviderRouter
from context_capture.providers.base import PrivacyLevel
from context_capture.cli.setup_commands import SetupCLI


class BrokerCLI:
    """Command-line interface for the Context Broker."""

    def __init__(self):
        """Initialize the broker CLI."""
        self.broker: Optional[ContextBroker] = None
        self.setup_logging()

    def setup_logging(self, level: str = "INFO") -> None:
        """Setup logging for CLI operations."""
        log_level = getattr(logging, level.upper(), logging.INFO)
        logging.basicConfig(
            level=log_level,
            format='%(asctime)s - %(name)s - %(levelname)s - %(message)s',
            datefmt='%H:%M:%S'
        )

    def create_broker(
        self,
        knowledge_base: Optional[str] = None,
        privacy_level: str = "local",
        api_key: Optional[str] = None
    ) -> ContextBroker:
        """Create and configure a Context Broker instance."""
        try:
            # Setup provider router
            privacy = PrivacyLevel(privacy_level.lower())

            if privacy == PrivacyLevel.LOCAL:
                router = ProviderRouter.create_for_privacy(PrivacyLevel.LOCAL)
            elif api_key:
                router = ProviderRouter.create_default(api_key=api_key)
            else:
                router = ProviderRouter.create_for_privacy(PrivacyLevel.LOCAL)
                print("⚠️  No API key provided, using local models only")

            # Setup knowledge base path
            kb_path = Path(knowledge_base) if knowledge_base else Path(".context")

            # Create broker
            broker = ContextBroker(
                provider_router=router,
                knowledge_base_path=kb_path,
                privacy_level=privacy
            )

            return broker

        except Exception as e:
            print(f"❌ Error creating broker: {e}")
            sys.exit(1)

    def cmd_search(self, args) -> None:
        """Search for relevant context."""
        self.broker = self.create_broker(
            knowledge_base=args.knowledge_base,
            privacy_level=args.privacy,
            api_key=args.api_key
        )

        try:
            print(f"🔍 Searching for: '{args.query}'")

            result = self.broker.get_relevant_context(
                query=args.query,
                context_type=args.type,
                max_results=args.max_results,
                similarity_threshold=args.threshold
            )

            contexts = result.get("contexts", [])
            summary = result.get("summary", "")
            metadata = result.get("metadata", {})

            if not contexts:
                print("❌ No relevant context found")
                return

            # Display results
            print(f"\n✅ Found {len(contexts)} relevant context items")

            if summary and not args.no_summary:
                print(f"\n📋 Summary: {summary}")

            if args.format == "json":
                print(json.dumps(result, indent=2, default=str))
            else:
                self._display_contexts_formatted(contexts, args.verbose)

        except Exception as e:
            print(f"❌ Search error: {e}")
            sys.exit(1)

    def cmd_inject(self, args) -> None:
        """Inject context into a request."""
        self.broker = self.create_broker(
            knowledge_base=args.knowledge_base,
            privacy_level=args.privacy,
            api_key=args.api_key
        )

        try:
            if args.file:
                # Read request from file
                request_text = Path(args.file).read_text(encoding='utf-8')
            else:
                # Read request from stdin or argument
                request_text = args.request or sys.stdin.read()

            if not request_text.strip():
                print("❌ No request provided")
                return

            print(f"💉 Injecting context into request...")

            enhanced_request = self.broker.inject_context_for_agent(
                agent_request=request_text,
                agent_type=args.agent_type,
                include_summary=not args.no_summary
            )

            if args.output:
                # Write to file
                Path(args.output).write_text(enhanced_request, encoding='utf-8')
                print(f"✅ Enhanced request written to {args.output}")
            else:
                # Print to stdout
                print("=" * 50)
                print(enhanced_request)

        except Exception as e:
            print(f"❌ Injection error: {e}")
            sys.exit(1)

    def cmd_status(self, args) -> None:
        """Show broker status and statistics."""
        self.broker = self.create_broker(
            knowledge_base=args.knowledge_base,
            privacy_level=args.privacy,
            api_key=args.api_key
        )

        try:
            status = self.broker.get_broker_status()

            if args.format == "json":
                print(json.dumps(status, indent=2, default=str))
                return

            # Formatted status display
            print("📊 Context Broker Status")
            print("=" * 50)

            print(f"Status: {status.get('status', 'unknown')}")
            print(f"Knowledge Base: {status.get('knowledge_base_path', 'unknown')}")
            print(f"Privacy Level: {status.get('privacy_level', 'unknown')}")
            print(f"Max Context Tokens: {status.get('max_context_tokens', 'unknown')}")
            print(f"Cache Size: {status.get('cache_size', 0)} queries")

            # Last update
            last_update = status.get('last_index_update', 0)
            if last_update:
                update_time = time.strftime('%Y-%m-%d %H:%M:%S', time.localtime(last_update))
                print(f"Last Index Update: {update_time}")

            # Search engine stats
            search_stats = status.get('search_engine', {})
            if search_stats:
                print(f"\n📚 Search Engine:")
                print(f"  Total Items: {search_stats.get('total_items', 0)}")
                print(f"  Cached Embeddings: {search_stats.get('cached_embeddings', 0)}")
                print(f"  Index Size: {search_stats.get('index_size_kb', 0):.1f} KB")

            # Provider stats
            provider_stats = status.get('providers', {})
            if provider_stats:
                print(f"\n🤖 Providers:")
                print(f"  Total: {provider_stats.get('total_providers', 0)}")
                print(f"  Healthy: {provider_stats.get('healthy_providers', 0)}")

                providers = provider_stats.get('providers', [])
                for provider in providers:
                    health = "✅" if provider.get('healthy') else "❌"
                    print(f"  {health} {provider.get('name', 'unknown')} ({provider.get('privacy', 'unknown')})")

        except Exception as e:
            print(f"❌ Status error: {e}")
            sys.exit(1)

    def cmd_update(self, args) -> None:
        """Update the knowledge base index."""
        self.broker = self.create_broker(
            knowledge_base=args.knowledge_base,
            privacy_level=args.privacy,
            api_key=args.api_key
        )

        try:
            print("🔄 Updating knowledge base index...")

            start_time = time.time()
            updated = self.broker.update_knowledge_base(force=args.force)
            elapsed_time = time.time() - start_time

            if updated:
                print(f"✅ Knowledge base updated in {elapsed_time:.2f}s")
            else:
                print("ℹ️  Knowledge base is already up to date")

            # Show post-update status
            if args.verbose:
                print("\n📊 Post-update Status:")
                status = self.broker.get_broker_status()
                search_stats = status.get('search_engine', {})
                print(f"  Total Items: {search_stats.get('total_items', 0)}")
                print(f"  Index Size: {search_stats.get('index_size_kb', 0):.1f} KB")

        except Exception as e:
            print(f"❌ Update error: {e}")
            sys.exit(1)

    def cmd_clear_cache(self, args) -> None:
        """Clear the broker cache."""
        self.broker = self.create_broker(
            knowledge_base=args.knowledge_base,
            privacy_level=args.privacy,
            api_key=args.api_key
        )

        try:
            self.broker.clear_cache()
            print("✅ Broker cache cleared")

        except Exception as e:
            print(f"❌ Clear cache error: {e}")
            sys.exit(1)

    def cmd_interactive(self, args) -> None:
        """Start interactive broker session."""
        self.broker = self.create_broker(
            knowledge_base=args.knowledge_base,
            privacy_level=args.privacy,
            api_key=args.api_key
        )

        print("🤖 Context Broker Interactive Mode")
        print("Enter queries to search for context. Type 'quit' to exit.")
        print("Commands: search <query>, status, update, clear, help")
        print("=" * 50)

        while True:
            try:
                user_input = input("\nbroker> ").strip()

                if not user_input:
                    continue

                if user_input.lower() in ['quit', 'exit', 'q']:
                    print("👋 Goodbye!")
                    break

                if user_input.lower() == 'help':
                    self._show_interactive_help()
                    continue

                if user_input.lower() == 'status':
                    status = self.broker.get_broker_status()
                    search_stats = status.get('search_engine', {})
                    print(f"📊 Items: {search_stats.get('total_items', 0)}, Cache: {status.get('cache_size', 0)}")
                    continue

                if user_input.lower() == 'update':
                    print("🔄 Updating...")
                    updated = self.broker.update_knowledge_base()
                    print("✅ Updated" if updated else "ℹ️  Already up to date")
                    continue

                if user_input.lower() == 'clear':
                    self.broker.clear_cache()
                    print("✅ Cache cleared")
                    continue

                # Default to search
                if user_input.startswith('search '):
                    query = user_input[7:]  # Remove 'search ' prefix
                else:
                    query = user_input

                result = self.broker.get_relevant_context(
                    query=query,
                    max_results=3,
                    similarity_threshold=0.6
                )

                contexts = result.get("contexts", [])
                summary = result.get("summary", "")

                if not contexts:
                    print("❌ No relevant context found")
                    continue

                print(f"\n✅ Found {len(contexts)} items")
                if summary:
                    print(f"📋 {summary}")

                for i, ctx in enumerate(contexts[:2]):  # Show top 2
                    content = ctx.get("content", "")[:150]
                    source = ctx.get("source", "").split("/")[-1]
                    similarity = ctx.get("similarity", 0)
                    print(f"\n{i+1}. {source} ({similarity:.1%})")
                    print(f"   {content}...")

            except KeyboardInterrupt:
                print("\n👋 Goodbye!")
                break
            except Exception as e:
                print(f"❌ Error: {e}")

    def _display_contexts_formatted(self, contexts: List[Dict[str, Any]], verbose: bool = False) -> None:
        """Display context results in a formatted way."""
        for i, ctx in enumerate(contexts, 1):
            content = ctx.get("content", "")
            source = ctx.get("source", "").split("/")[-1]
            similarity = ctx.get("similarity", 0)
            timestamp = ctx.get("timestamp", "")

            print(f"\n{i}. {source} (similarity: {similarity:.1%})")

            if timestamp:
                try:
                    if isinstance(timestamp, (int, float)):
                        time_str = time.strftime('%Y-%m-%d %H:%M', time.localtime(timestamp))
                        print(f"   Date: {time_str}")
                except:
                    pass

            if verbose:
                print(f"   {content}")
            else:
                # Show first 200 characters
                preview = content[:200] + "..." if len(content) > 200 else content
                print(f"   {preview}")

    def _show_interactive_help(self) -> None:
        """Show interactive mode help."""
        print("""
📖 Interactive Commands:
  <query>           Search for context
  search <query>    Search for context
  status           Show broker status
  update           Update knowledge base
  clear            Clear cache
  help             Show this help
  quit             Exit interactive mode
""")

    def run(self) -> None:
        """Run the CLI with command-line arguments."""
        parser = argparse.ArgumentParser(
            description="Context Broker CLI - Intelligent context retrieval system",
            formatter_class=argparse.RawDescriptionHelpFormatter
        )

        # Global options
        parser.add_argument(
            "--knowledge-base", "-k",
            type=str,
            default=".context",
            help="Path to knowledge base directory (default: .context)"
        )
        parser.add_argument(
            "--privacy", "-p",
            choices=["local", "cloud", "hybrid"],
            default="local",
            help="Privacy level for operations (default: local)"
        )
        parser.add_argument(
            "--api-key",
            type=str,
            help="API key for cloud providers"
        )
        parser.add_argument(
            "--verbose", "-v",
            action="store_true",
            help="Enable verbose output"
        )
        parser.add_argument(
            "--log-level",
            choices=["DEBUG", "INFO", "WARNING", "ERROR"],
            default="INFO",
            help="Set logging level"
        )

        subparsers = parser.add_subparsers(dest="command", help="Available commands")

        # Search command
        search_parser = subparsers.add_parser("search", help="Search for relevant context")
        search_parser.add_argument("query", help="Search query")
        search_parser.add_argument("--type", "-t", default="general", help="Context type to search for")
        search_parser.add_argument("--max-results", "-n", type=int, default=5, help="Maximum results")
        search_parser.add_argument("--threshold", type=float, default=0.7, help="Similarity threshold")
        search_parser.add_argument("--format", choices=["text", "json"], default="text", help="Output format")
        search_parser.add_argument("--no-summary", action="store_true", help="Skip summary generation")

        # Inject command
        inject_parser = subparsers.add_parser("inject", help="Inject context into a request")
        inject_parser.add_argument("request", nargs="?", help="Request text (or use --file/stdin)")
        inject_parser.add_argument("--file", "-f", help="Read request from file")
        inject_parser.add_argument("--output", "-o", help="Write enhanced request to file")
        inject_parser.add_argument("--agent-type", default="general", help="Type of agent making the request")
        inject_parser.add_argument("--no-summary", action="store_true", help="Skip context summary")

        # Status command
        status_parser = subparsers.add_parser("status", help="Show broker status")
        status_parser.add_argument("--format", choices=["text", "json"], default="text", help="Output format")

        # Update command
        update_parser = subparsers.add_parser("update", help="Update knowledge base index")
        update_parser.add_argument("--force", action="store_true", help="Force update even if recent")

        # Clear cache command
        subparsers.add_parser("clear-cache", help="Clear broker cache")

        # Interactive command
        subparsers.add_parser("interactive", help="Start interactive broker session")

        # Bootstrap command
        subparsers.add_parser("bootstrap", help="Bootstrap the context capture system")

        # Parse arguments
        args = parser.parse_args()

        # Setup logging
        self.setup_logging(args.log_level)

        # Route to appropriate command
        if args.command == "search":
            self.cmd_search(args)
        elif args.command == "inject":
            self.cmd_inject(args)
        elif args.command == "status":
            self.cmd_status(args)
        elif args.command == "update":
            self.cmd_update(args)
        elif args.command == "clear-cache":
            self.cmd_clear_cache(args)
        elif args.command == "interactive":
            self.cmd_interactive(args)
        elif args.command == "bootstrap":
            setup_cli = SetupCLI()
            setup_cli.cmd_bootstrap(args)
        else:
            parser.print_help()


def main():
    """Entry point for the broker CLI."""
    cli = BrokerCLI()
    cli.run()


if __name__ == "__main__":
    main()