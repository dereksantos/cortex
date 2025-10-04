"""
Synthesis agent for generating best practices and documentation from insights.
"""

import json
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any, Dict, List, Optional

from context_capture.llm.ollama_client import OllamaClient
from context_capture.utils.config import Config


class SynthesisAgent:
    """Agent for synthesizing insights into actionable knowledge products."""

    def __init__(self, config: Optional[Config] = None):
        """
        Initialize synthesis agent.

        Args:
            config: Configuration object
        """
        self.config = config or Config()
        self.llm_client = OllamaClient(self.config)

    def synthesize_best_practices(self, category: Optional[str] = None,
                                 min_insights: int = 3) -> Dict[str, Any]:
        """
        Synthesize best practices from captured patterns.

        Args:
            category: Specific category to synthesize from
            min_insights: Minimum number of insights required

        Returns:
            Dictionary with synthesized best practices
        """
        try:
            # Gather insights for synthesis
            insights = self._gather_insights_for_synthesis(category, min_insights)

            if len(insights) < min_insights:
                return {
                    'category': category,
                    'message': f'Need at least {min_insights} insights for synthesis (found {len(insights)})'
                }

            # Group insights by theme/topic
            themes = self._group_insights_by_theme(insights)

            # Generate best practices
            best_practices = self._generate_best_practices(themes, category)

            return {
                'category': category,
                'insights_analyzed': len(insights),
                'themes_identified': len(themes),
                'best_practices': best_practices,
                'generated_at': datetime.now().isoformat()
            }

        except Exception as e:
            return {'error': f"Best practices synthesis failed: {str(e)}"}

    def create_learning_guide(self, topic: str, timeframe: str = "month") -> Dict[str, Any]:
        """
        Create a learning guide from insights on a specific topic.

        Args:
            topic: Topic to create guide for
            timeframe: Time period to analyze

        Returns:
            Dictionary with learning guide
        """
        try:
            # Calculate date range
            end_date = datetime.now()
            if timeframe == "week":
                start_date = end_date - timedelta(weeks=1)
            elif timeframe == "month":
                start_date = end_date - timedelta(days=30)
            elif timeframe == "quarter":
                start_date = end_date - timedelta(days=90)
            else:
                start_date = end_date - timedelta(days=30)

            # Gather topic-related insights
            insights = self._gather_topic_insights(topic, start_date, end_date)

            if not insights:
                return {
                    'topic': topic,
                    'message': f'No insights found for topic "{topic}" in the last {timeframe}'
                }

            # Create learning progression
            learning_guide = self._create_learning_guide(insights, topic)

            return {
                'topic': topic,
                'timeframe': timeframe,
                'insights_analyzed': len(insights),
                'learning_guide': learning_guide,
                'generated_at': datetime.now().isoformat()
            }

        except Exception as e:
            return {'error': f"Learning guide creation failed: {str(e)}"}

    def generate_documentation(self, doc_type: str, category: Optional[str] = None) -> Dict[str, Any]:
        """
        Generate documentation from captured insights.

        Args:
            doc_type: Type of documentation ('architecture', 'decisions', 'patterns', 'guide')
            category: Specific category to focus on

        Returns:
            Dictionary with generated documentation
        """
        try:
            # Gather relevant insights
            if category:
                insights = self._gather_category_insights(category)
            else:
                insights = self._gather_all_recent_insights(days=30)

            if not insights:
                return {
                    'doc_type': doc_type,
                    'category': category,
                    'message': 'No insights available for documentation generation'
                }

            # Generate documentation based on type
            if doc_type == 'architecture':
                documentation = self._generate_architecture_doc(insights)
            elif doc_type == 'decisions':
                documentation = self._generate_decisions_doc(insights)
            elif doc_type == 'patterns':
                documentation = self._generate_patterns_doc(insights)
            elif doc_type == 'guide':
                documentation = self._generate_guide_doc(insights)
            else:
                return {'error': f'Unknown documentation type: {doc_type}'}

            return {
                'doc_type': doc_type,
                'category': category,
                'insights_used': len(insights),
                'documentation': documentation,
                'generated_at': datetime.now().isoformat()
            }

        except Exception as e:
            return {'error': f"Documentation generation failed: {str(e)}"}

    def create_insight_summary(self, format_type: str = "markdown",
                             period: str = "week") -> Dict[str, Any]:
        """
        Create a summary of insights in specified format.

        Args:
            format_type: Output format ('markdown', 'json', 'html')
            period: Time period to summarize

        Returns:
            Dictionary with formatted summary
        """
        try:
            # Calculate date range
            end_date = datetime.now()
            if period == "day":
                start_date = end_date - timedelta(days=1)
            elif period == "week":
                start_date = end_date - timedelta(weeks=1)
            elif period == "month":
                start_date = end_date - timedelta(days=30)
            else:
                start_date = end_date - timedelta(weeks=1)

            # Gather insights
            insights = self._gather_insights_from_period(start_date, end_date)

            if not insights:
                return {
                    'format': format_type,
                    'period': period,
                    'message': 'No insights found for the specified period'
                }

            # Generate summary in requested format
            if format_type == "markdown":
                summary = self._create_markdown_summary(insights, period)
            elif format_type == "json":
                summary = self._create_json_summary(insights, period)
            elif format_type == "html":
                summary = self._create_html_summary(insights, period)
            else:
                return {'error': f'Unknown format type: {format_type}'}

            return {
                'format': format_type,
                'period': period,
                'insights_count': len(insights),
                'summary': summary,
                'generated_at': datetime.now().isoformat()
            }

        except Exception as e:
            return {'error': f"Summary creation failed: {str(e)}"}

    def _gather_insights_for_synthesis(self, category: Optional[str], min_insights: int) -> List[Dict[str, Any]]:
        """Gather insights suitable for synthesis."""
        insights = []
        categories = [category] if category else ['decisions', 'patterns', 'insights', 'strategies']

        for cat in categories:
            cat_dir = self.config.knowledge_dir / cat
            if cat_dir.exists():
                for md_file in cat_dir.glob('*.md'):
                    file_insights = self._parse_insights_from_file(md_file, cat)
                    insights.extend(file_insights)

        # Sort by importance/relevance
        insights.sort(key=lambda x: float(x.get('importance', '0')), reverse=True)
        return insights

    def _gather_topic_insights(self, topic: str, start_date: datetime, end_date: datetime) -> List[Dict[str, Any]]:
        """Gather insights related to a specific topic."""
        all_insights = []
        categories = ['decisions', 'patterns', 'insights', 'strategies']

        for cat in categories:
            cat_dir = self.config.knowledge_dir / cat
            if cat_dir.exists():
                for md_file in cat_dir.glob('*.md'):
                    file_date = datetime.fromtimestamp(md_file.stat().st_mtime)
                    if start_date <= file_date <= end_date:
                        file_insights = self._parse_insights_from_file(md_file, cat)
                        all_insights.extend(file_insights)

        # Filter by topic relevance
        topic_insights = []
        topic_lower = topic.lower()

        for insight in all_insights:
            title = insight.get('title', '').lower()
            reasoning = insight.get('reasoning', '').lower()
            tags = insight.get('tags', '').lower()

            if topic_lower in title or topic_lower in reasoning or topic_lower in tags:
                topic_insights.append(insight)

        return topic_insights

    def _gather_category_insights(self, category: str) -> List[Dict[str, Any]]:
        """Gather all insights from a specific category."""
        insights = []
        cat_dir = self.config.knowledge_dir / category

        if cat_dir.exists():
            for md_file in cat_dir.glob('*.md'):
                file_insights = self._parse_insights_from_file(md_file, category)
                insights.extend(file_insights)

        return insights

    def _gather_all_recent_insights(self, days: int) -> List[Dict[str, Any]]:
        """Gather all recent insights."""
        end_date = datetime.now()
        start_date = end_date - timedelta(days=days)
        return self._gather_insights_from_period(start_date, end_date)

    def _gather_insights_from_period(self, start_date: datetime, end_date: datetime) -> List[Dict[str, Any]]:
        """Gather insights from a time period."""
        insights = []
        categories = ['decisions', 'patterns', 'insights', 'strategies']

        for cat in categories:
            cat_dir = self.config.knowledge_dir / cat
            if cat_dir.exists():
                for md_file in cat_dir.glob('*.md'):
                    file_date = datetime.fromtimestamp(md_file.stat().st_mtime)
                    if start_date <= file_date <= end_date:
                        file_insights = self._parse_insights_from_file(md_file, cat)
                        insights.extend(file_insights)

        return insights

    def _parse_insights_from_file(self, file_path: Path, category: str) -> List[Dict[str, Any]]:
        """Parse insights from markdown file."""
        insights = []

        try:
            content = file_path.read_text()
            sections = content.split('---')

            for section in sections[1:]:
                if not section.strip():
                    continue

                insight = self._parse_insight_section(section, category, file_path)
                if insight:
                    insights.append(insight)

        except Exception:
            pass

        return insights

    def _parse_insight_section(self, section: str, category: str, file_path: Path) -> Optional[Dict[str, Any]]:
        """Parse individual insight section."""
        try:
            lines = section.strip().split('\n')
            if len(lines) < 2:
                return None

            title = ""
            for line in lines:
                if line.startswith('## '):
                    title = line[3:].strip()
                    break

            metadata = {'category': category, 'file': file_path.name, 'title': title}

            for line in lines:
                if line.startswith('**') and '**:' in line:
                    key = line.split('**:')[0].replace('**', '').lower()
                    value = line.split('**:')[1].strip()
                    metadata[key] = value

            return metadata

        except Exception:
            return None

    def _group_insights_by_theme(self, insights: List[Dict[str, Any]]) -> Dict[str, List[Dict[str, Any]]]:
        """Group insights by common themes."""
        themes = {}

        for insight in insights:
            # Extract themes from tags
            tags = insight.get('tags', '').split('#')
            for tag in tags:
                tag = tag.strip()
                if tag:
                    if tag not in themes:
                        themes[tag] = []
                    themes[tag].append(insight)

            # Also group by tool type
            tool = insight.get('tool', 'unknown')
            tool_theme = f"tool_{tool}"
            if tool_theme not in themes:
                themes[tool_theme] = []
            themes[tool_theme].append(insight)

        return themes

    def _generate_best_practices(self, themes: Dict[str, List[Dict[str, Any]]],
                               category: Optional[str]) -> Dict[str, Any]:
        """Generate best practices from themes."""
        if not self.llm_client.is_available():
            return self._generate_fallback_best_practices(themes, category)

        prompt = self._create_best_practices_prompt(themes, category)
        response = self.llm_client.generate(prompt)

        if response:
            try:
                return json.loads(response)
            except:
                return {'practices': response}

        return self._generate_fallback_best_practices(themes, category)

    def _create_learning_guide(self, insights: List[Dict[str, Any]], topic: str) -> Dict[str, Any]:
        """Create learning guide from insights."""
        if not self.llm_client.is_available():
            return self._generate_fallback_learning_guide(insights, topic)

        prompt = self._create_learning_guide_prompt(insights, topic)
        response = self.llm_client.generate(prompt)

        if response:
            try:
                return json.loads(response)
            except:
                return {'guide': response}

        return self._generate_fallback_learning_guide(insights, topic)

    def _generate_architecture_doc(self, insights: List[Dict[str, Any]]) -> str:
        """Generate architecture documentation."""
        if not self.llm_client.is_available():
            return self._generate_fallback_architecture_doc(insights)

        prompt = f"""Generate architecture documentation based on these insights:

{self._format_insights_for_prompt(insights[:10])}

Create a markdown document with sections:
1. Architecture Overview
2. Key Decisions
3. Design Patterns
4. Trade-offs Made
5. Future Considerations"""

        response = self.llm_client.generate(prompt)
        return response or self._generate_fallback_architecture_doc(insights)

    def _generate_decisions_doc(self, insights: List[Dict[str, Any]]) -> str:
        """Generate decisions documentation."""
        decision_insights = [i for i in insights if i.get('category') == 'decisions']

        if not self.llm_client.is_available():
            return self._generate_fallback_decisions_doc(decision_insights)

        prompt = f"""Generate a decision log from these insights:

{self._format_insights_for_prompt(decision_insights[:10])}

Create a markdown document listing each decision with:
- Decision made
- Context and reasoning
- Alternatives considered
- Impact and consequences"""

        response = self.llm_client.generate(prompt)
        return response or self._generate_fallback_decisions_doc(decision_insights)

    def _create_markdown_summary(self, insights: List[Dict[str, Any]], period: str) -> str:
        """Create markdown summary of insights."""
        summary = f"# Development Insights Summary - {period.title()}\n\n"
        summary += f"Generated on: {datetime.now().strftime('%Y-%m-%d %H:%M')}\n\n"

        # Group by category
        categories = {}
        for insight in insights:
            cat = insight.get('category', 'unknown')
            if cat not in categories:
                categories[cat] = []
            categories[cat].append(insight)

        for category, cat_insights in categories.items():
            summary += f"## {category.title()} ({len(cat_insights)} insights)\n\n"
            for insight in cat_insights[:5]:  # Limit to 5 per category
                summary += f"- **{insight.get('title', 'Unknown')}**: {insight.get('reasoning', '')}\n"
            summary += "\n"

        return summary

    def _create_json_summary(self, insights: List[Dict[str, Any]], period: str) -> Dict[str, Any]:
        """Create JSON summary of insights."""
        return {
            'period': period,
            'generated_at': datetime.now().isoformat(),
            'total_insights': len(insights),
            'categories': {
                cat: len([i for i in insights if i.get('category') == cat])
                for cat in ['decisions', 'patterns', 'insights', 'strategies']
            },
            'insights': insights[:20]  # Limit to 20 most recent
        }

    def _format_insights_for_prompt(self, insights: List[Dict[str, Any]]) -> str:
        """Format insights for LLM prompt."""
        formatted = []
        for insight in insights:
            formatted.append(f"- {insight.get('title', 'Unknown')}: {insight.get('reasoning', '')}")
        return '\n'.join(formatted)

    def _generate_fallback_best_practices(self, themes: Dict[str, List[Dict[str, Any]]],
                                        category: Optional[str]) -> Dict[str, Any]:
        """Generate fallback best practices."""
        practices = []
        for theme, theme_insights in themes.items():
            if len(theme_insights) >= 2:
                practices.append(f"Consider standardizing approach for {theme} (seen {len(theme_insights)} times)")

        return {
            'practices': practices,
            'fallback': True,
            'message': 'LLM unavailable - basic practices generated'
        }

    def _generate_fallback_learning_guide(self, insights: List[Dict[str, Any]], topic: str) -> Dict[str, Any]:
        """Generate fallback learning guide."""
        return {
            'topic': topic,
            'insights_count': len(insights),
            'guide': f"Review the {len(insights)} insights related to {topic}",
            'fallback': True
        }

    def _generate_fallback_architecture_doc(self, insights: List[Dict[str, Any]]) -> str:
        """Generate fallback architecture documentation."""
        return f"""# Architecture Documentation

Generated from {len(insights)} captured insights.

## Key Insights
{chr(10).join([f"- {i.get('title', 'Unknown')}" for i in insights[:10]])}

*Note: LLM unavailable - basic documentation generated*"""

    def _generate_fallback_decisions_doc(self, insights: List[Dict[str, Any]]) -> str:
        """Generate fallback decisions documentation."""
        return f"""# Decision Log

{chr(10).join([f"- {i.get('title', 'Unknown')}: {i.get('reasoning', '')}" for i in insights[:10]])}

*Note: LLM unavailable - basic log generated*"""

    def _create_best_practices_prompt(self, themes: Dict[str, List[Dict[str, Any]]],
                                    category: Optional[str]) -> str:
        """Create prompt for best practices generation."""
        theme_summary = []
        for theme, insights in themes.items():
            if len(insights) >= 2:
                theme_summary.append(f"- {theme}: {len(insights)} occurrences")

        return f"""Based on these recurring themes in development insights, generate best practices:

Themes identified:
{chr(10).join(theme_summary)}

Generate a JSON response with:
{{
  "best_practices": [
    {{
      "title": "Practice title",
      "description": "Detailed description",
      "rationale": "Why this is important",
      "implementation": "How to implement"
    }}
  ],
  "anti_patterns": ["What to avoid"],
  "guidelines": ["General guidelines"]
}}"""

    def _create_learning_guide_prompt(self, insights: List[Dict[str, Any]], topic: str) -> str:
        """Create prompt for learning guide generation."""
        insight_summary = []
        for insight in insights[:10]:
            insight_summary.append(f"- {insight.get('title', 'Unknown')}: {insight.get('reasoning', '')}")

        return f"""Create a learning guide for "{topic}" based on these development insights:

Insights:
{chr(10).join(insight_summary)}

Generate a JSON response with:
{{
  "learning_path": [
    {{
      "step": 1,
      "title": "Step title",
      "description": "What to learn",
      "resources": ["Resource 1", "Resource 2"]
    }}
  ],
  "key_concepts": ["Concept 1", "Concept 2"],
  "practical_exercises": ["Exercise 1", "Exercise 2"],
  "further_reading": ["Resource 1", "Resource 2"]
}}"""