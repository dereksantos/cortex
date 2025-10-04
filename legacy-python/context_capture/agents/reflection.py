"""
Reflection agent for analyzing patterns and generating meta-insights.
"""

import json
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any, Dict, List, Optional

from context_capture.llm.ollama_client import OllamaClient
from context_capture.utils.config import Config


class ReflectionAgent:
    """Agent for reflecting on captured insights and generating meta-analysis."""

    def __init__(self, config: Optional[Config] = None):
        """
        Initialize reflection agent.

        Args:
            config: Configuration object
        """
        self.config = config or Config()
        self.llm_client = OllamaClient(self.config)

    def reflect_on_period(self, timeframe: str = "week", focus_area: Optional[str] = None) -> Dict[str, Any]:
        """
        Reflect on insights from a specific time period.

        Args:
            timeframe: Time period to analyze ('day', 'week', 'month')
            focus_area: Specific area to focus on ('architecture', 'patterns', etc.)

        Returns:
            Dictionary with reflection results
        """
        try:
            # Calculate date range
            end_date = datetime.now()
            if timeframe == "day":
                start_date = end_date - timedelta(days=1)
            elif timeframe == "week":
                start_date = end_date - timedelta(weeks=1)
            elif timeframe == "month":
                start_date = end_date - timedelta(days=30)
            else:
                start_date = end_date - timedelta(weeks=1)  # Default to week

            # Gather insights from the period
            insights = self._gather_insights_from_period(start_date, end_date)

            if not insights:
                return {
                    'timeframe': timeframe,
                    'focus_area': focus_area,
                    'insights_count': 0,
                    'message': 'No insights found for the specified period'
                }

            # Filter by focus area if specified
            if focus_area:
                insights = self._filter_by_focus_area(insights, focus_area)

            # Generate reflection
            reflection = self._generate_reflection(insights, timeframe, focus_area)

            return {
                'timeframe': timeframe,
                'focus_area': focus_area,
                'period': f"{start_date.strftime('%Y-%m-%d')} to {end_date.strftime('%Y-%m-%d')}",
                'insights_count': len(insights),
                'reflection': reflection,
                'insights_analyzed': insights
            }

        except Exception as e:
            return {'error': f"Reflection failed: {str(e)}"}

    def analyze_patterns(self, category: Optional[str] = None, min_frequency: int = 2) -> Dict[str, Any]:
        """
        Analyze recurring patterns in captured insights.

        Args:
            category: Specific category to analyze
            min_frequency: Minimum frequency for pattern detection

        Returns:
            Dictionary with pattern analysis
        """
        try:
            # Gather all insights for pattern analysis
            insights = self._gather_all_insights(category)

            if len(insights) < min_frequency:
                return {
                    'category': category,
                    'message': f'Not enough insights for pattern analysis (need at least {min_frequency})'
                }

            # Detect patterns
            patterns = self._detect_patterns(insights, min_frequency)

            # Generate pattern analysis
            analysis = self._analyze_patterns_with_llm(patterns, insights)

            return {
                'category': category,
                'insights_analyzed': len(insights),
                'patterns_found': len(patterns),
                'patterns': patterns,
                'analysis': analysis
            }

        except Exception as e:
            return {'error': f"Pattern analysis failed: {str(e)}"}

    def audit_decisions(self, look_back_days: int = 30) -> Dict[str, Any]:
        """
        Audit decisions for consistency and conflicts.

        Args:
            look_back_days: Number of days to look back

        Returns:
            Dictionary with audit results
        """
        try:
            # Gather decision insights
            start_date = datetime.now() - timedelta(days=look_back_days)
            decision_insights = self._gather_insights_from_period(
                start_date, datetime.now(), category='decisions'
            )

            if not decision_insights:
                return {
                    'message': 'No decisions found in the specified period',
                    'period': f"Last {look_back_days} days"
                }

            # Analyze for conflicts and consistency
            audit_results = self._audit_decisions_with_llm(decision_insights)

            return {
                'period': f"Last {look_back_days} days",
                'decisions_analyzed': len(decision_insights),
                'audit_results': audit_results,
                'decisions': decision_insights
            }

        except Exception as e:
            return {'error': f"Decision audit failed: {str(e)}"}

    def _gather_insights_from_period(self, start_date: datetime, end_date: datetime,
                                    category: Optional[str] = None) -> List[Dict[str, Any]]:
        """Gather insights from a specific time period."""
        insights = []
        categories = [category] if category else ['decisions', 'patterns', 'insights', 'strategies']

        for cat in categories:
            cat_dir = self.config.knowledge_dir / cat
            if not cat_dir.exists():
                continue

            for md_file in cat_dir.glob('*.md'):
                # Check if file is in date range
                file_date = datetime.fromtimestamp(md_file.stat().st_mtime)
                if start_date <= file_date <= end_date:
                    # Parse insights from file
                    file_insights = self._parse_insights_from_file(md_file, cat)
                    insights.extend(file_insights)

        return insights

    def _gather_all_insights(self, category: Optional[str] = None) -> List[Dict[str, Any]]:
        """Gather all insights from knowledge base."""
        insights = []
        categories = [category] if category else ['decisions', 'patterns', 'insights', 'strategies']

        for cat in categories:
            cat_dir = self.config.knowledge_dir / cat
            if not cat_dir.exists():
                continue

            for md_file in cat_dir.glob('*.md'):
                file_insights = self._parse_insights_from_file(md_file, cat)
                insights.extend(file_insights)

        return insights

    def _parse_insights_from_file(self, file_path: Path, category: str) -> List[Dict[str, Any]]:
        """Parse insights from a markdown file."""
        insights = []

        try:
            content = file_path.read_text()

            # Split by insight separators
            sections = content.split('---')

            for section in sections[1:]:  # Skip header
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

            # Extract title (first ## line)
            title = ""
            for line in lines:
                if line.startswith('## '):
                    title = line[3:].strip()
                    break

            # Extract metadata
            metadata = {'category': category, 'file': file_path.name, 'title': title}

            for line in lines:
                if line.startswith('**') and '**:' in line:
                    key = line.split('**:')[0].replace('**', '').lower()
                    value = line.split('**:')[1].strip()
                    metadata[key] = value

            return metadata

        except Exception:
            return None

    def _filter_by_focus_area(self, insights: List[Dict[str, Any]], focus_area: str) -> List[Dict[str, Any]]:
        """Filter insights by focus area."""
        filtered = []

        for insight in insights:
            tags = insight.get('tags', '').lower()
            reasoning = insight.get('reasoning', '').lower()
            title = insight.get('title', '').lower()

            if focus_area.lower() in tags or focus_area.lower() in reasoning or focus_area.lower() in title:
                filtered.append(insight)

        return filtered

    def _detect_patterns(self, insights: List[Dict[str, Any]], min_frequency: int) -> List[Dict[str, Any]]:
        """Detect recurring patterns in insights."""
        patterns = []

        # Group by similar tools
        tool_patterns = {}
        for insight in insights:
            tool = insight.get('tool', 'unknown')
            if tool not in tool_patterns:
                tool_patterns[tool] = []
            tool_patterns[tool].append(insight)

        for tool, tool_insights in tool_patterns.items():
            if len(tool_insights) >= min_frequency:
                patterns.append({
                    'type': 'tool_usage',
                    'pattern': tool,
                    'frequency': len(tool_insights),
                    'insights': tool_insights
                })

        # Group by similar tags
        tag_patterns = {}
        for insight in insights:
            tags = insight.get('tags', '').split('#')
            for tag in tags:
                tag = tag.strip()
                if tag:
                    if tag not in tag_patterns:
                        tag_patterns[tag] = []
                    tag_patterns[tag].append(insight)

        for tag, tag_insights in tag_patterns.items():
            if len(tag_insights) >= min_frequency:
                patterns.append({
                    'type': 'tag_frequency',
                    'pattern': tag,
                    'frequency': len(tag_insights),
                    'insights': tag_insights
                })

        return patterns

    def _generate_reflection(self, insights: List[Dict[str, Any]], timeframe: str,
                           focus_area: Optional[str]) -> Dict[str, Any]:
        """Generate reflection using LLM."""
        if not self.llm_client.is_available():
            return self._generate_fallback_reflection(insights, timeframe, focus_area)

        prompt = self._create_reflection_prompt(insights, timeframe, focus_area)
        response = self.llm_client.generate(prompt)

        if response:
            try:
                # Try to parse JSON response
                return json.loads(response)
            except:
                # Return as text if not JSON
                return {'summary': response}

        return self._generate_fallback_reflection(insights, timeframe, focus_area)

    def _analyze_patterns_with_llm(self, patterns: List[Dict[str, Any]],
                                 insights: List[Dict[str, Any]]) -> Dict[str, Any]:
        """Analyze patterns using LLM."""
        if not self.llm_client.is_available():
            return {'message': 'LLM unavailable for pattern analysis'}

        prompt = self._create_pattern_analysis_prompt(patterns, insights)
        response = self.llm_client.generate(prompt)

        if response:
            try:
                return json.loads(response)
            except:
                return {'analysis': response}

        return {'message': 'Pattern analysis failed'}

    def _audit_decisions_with_llm(self, decisions: List[Dict[str, Any]]) -> Dict[str, Any]:
        """Audit decisions using LLM."""
        if not self.llm_client.is_available():
            return {'message': 'LLM unavailable for decision audit'}

        prompt = self._create_decision_audit_prompt(decisions)
        response = self.llm_client.generate(prompt)

        if response:
            try:
                return json.loads(response)
            except:
                return {'audit': response}

        return {'message': 'Decision audit failed'}

    def _create_reflection_prompt(self, insights: List[Dict[str, Any]], timeframe: str,
                                focus_area: Optional[str]) -> str:
        """Create prompt for reflection analysis."""
        insights_summary = []
        for insight in insights[:10]:  # Limit to prevent prompt overflow
            insights_summary.append(f"- {insight.get('title', 'Unknown')}: {insight.get('reasoning', '')}")

        focus_text = f" focusing on {focus_area}" if focus_area else ""

        return f"""Analyze these development insights from the last {timeframe}{focus_text} and provide a reflection.

Insights analyzed:
{chr(10).join(insights_summary)}

Provide a JSON response with:
{{
  "summary": "Overall summary of the period",
  "key_themes": ["theme1", "theme2"],
  "progress_indicators": ["indicator1", "indicator2"],
  "patterns_noticed": ["pattern1", "pattern2"],
  "recommendations": ["rec1", "rec2"],
  "growth_areas": ["area1", "area2"]
}}

Focus on development patterns, architectural decisions, and learning progression."""

    def _create_pattern_analysis_prompt(self, patterns: List[Dict[str, Any]],
                                      insights: List[Dict[str, Any]]) -> str:
        """Create prompt for pattern analysis."""
        pattern_summary = []
        for pattern in patterns[:5]:
            pattern_summary.append(f"- {pattern['type']}: {pattern['pattern']} (frequency: {pattern['frequency']})")

        return f"""Analyze these recurring patterns in development insights:

Patterns detected:
{chr(10).join(pattern_summary)}

Provide a JSON response with:
{{
  "pattern_significance": "Analysis of what these patterns mean",
  "recommendations": ["actionable recommendations"],
  "potential_issues": ["potential problems to watch"],
  "optimization_opportunities": ["areas for improvement"]
}}"""

    def _create_decision_audit_prompt(self, decisions: List[Dict[str, Any]]) -> str:
        """Create prompt for decision audit."""
        decision_summary = []
        for decision in decisions[:10]:
            decision_summary.append(f"- {decision.get('title', 'Unknown')}: {decision.get('reasoning', '')}")

        return f"""Audit these architectural/strategic decisions for consistency and conflicts:

Decisions made:
{chr(10).join(decision_summary)}

Provide a JSON response with:
{{
  "consistency_score": 0.8,
  "conflicts_found": ["description of conflicts"],
  "alignment_assessment": "overall alignment analysis",
  "recommendations": ["suggestions for improvement"]
}}"""

    def _generate_fallback_reflection(self, insights: List[Dict[str, Any]], timeframe: str,
                                    focus_area: Optional[str]) -> Dict[str, Any]:
        """Generate basic reflection without LLM."""
        return {
            'summary': f'Analyzed {len(insights)} insights from the last {timeframe}',
            'key_themes': ['development', 'implementation'],
            'fallback': True,
            'message': 'LLM unavailable - basic analysis provided'
        }