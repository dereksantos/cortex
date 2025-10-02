"""
Audit agent for analyzing decision consistency and system health.
"""

import json
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

from context_capture.llm.ollama_client import OllamaClient
from context_capture.utils.config import Config


class AuditAgent:
    """Agent for auditing decisions, patterns, and system consistency."""

    def __init__(self, config: Optional[Config] = None):
        """
        Initialize audit agent.

        Args:
            config: Configuration object
        """
        self.config = config or Config()
        self.llm_client = OllamaClient(self.config)

    def audit_decision_consistency(self, look_back_days: int = 30) -> Dict[str, Any]:
        """
        Audit decisions for consistency and conflicts.

        Args:
            look_back_days: Number of days to analyze

        Returns:
            Dictionary with audit results
        """
        try:
            # Gather decision insights
            start_date = datetime.now() - timedelta(days=look_back_days)
            decisions = self._gather_decisions_from_period(start_date, datetime.now())

            if not decisions:
                return {
                    'message': f'No decisions found in the last {look_back_days} days',
                    'period': f"Last {look_back_days} days"
                }

            # Analyze for conflicts
            conflicts = self._detect_decision_conflicts(decisions)

            # Analyze consistency patterns
            consistency_analysis = self._analyze_decision_consistency(decisions)

            # Generate recommendations
            recommendations = self._generate_consistency_recommendations(decisions, conflicts)

            return {
                'period': f"Last {look_back_days} days",
                'decisions_analyzed': len(decisions),
                'conflicts_found': len(conflicts),
                'consistency_score': consistency_analysis.get('score', 0.0),
                'conflicts': conflicts,
                'consistency_analysis': consistency_analysis,
                'recommendations': recommendations,
                'audit_timestamp': datetime.now().isoformat()
            }

        except Exception as e:
            return {'error': f"Decision consistency audit failed: {str(e)}"}

    def audit_pattern_health(self, min_pattern_frequency: int = 3) -> Dict[str, Any]:
        """
        Audit the health of recurring patterns.

        Args:
            min_pattern_frequency: Minimum frequency to consider a pattern

        Returns:
            Dictionary with pattern health audit
        """
        try:
            # Gather all insights for pattern analysis
            insights = self._gather_all_insights()

            if len(insights) < min_pattern_frequency * 2:
                return {
                    'message': f'Not enough insights for pattern health analysis (need at least {min_pattern_frequency * 2})',
                    'insights_found': len(insights)
                }

            # Detect patterns
            patterns = self._detect_patterns(insights, min_pattern_frequency)

            # Analyze pattern health
            pattern_health = self._analyze_pattern_health(patterns, insights)

            # Identify problematic patterns
            problem_patterns = self._identify_problem_patterns(patterns)

            # Generate pattern recommendations
            pattern_recommendations = self._generate_pattern_recommendations(patterns, problem_patterns)

            return {
                'insights_analyzed': len(insights),
                'patterns_found': len(patterns),
                'healthy_patterns': len([p for p in patterns if p.get('health_score', 0) > 0.7]),
                'problematic_patterns': len(problem_patterns),
                'pattern_health': pattern_health,
                'problem_patterns': problem_patterns,
                'recommendations': pattern_recommendations,
                'audit_timestamp': datetime.now().isoformat()
            }

        except Exception as e:
            return {'error': f"Pattern health audit failed: {str(e)}"}

    def audit_knowledge_coverage(self) -> Dict[str, Any]:
        """
        Audit knowledge base coverage and gaps.

        Returns:
            Dictionary with coverage audit results
        """
        try:
            # Analyze coverage by category
            coverage_by_category = self._analyze_category_coverage()

            # Analyze temporal coverage
            temporal_coverage = self._analyze_temporal_coverage()

            # Identify knowledge gaps
            knowledge_gaps = self._identify_knowledge_gaps(coverage_by_category, temporal_coverage)

            # Calculate overall coverage score
            coverage_score = self._calculate_coverage_score(coverage_by_category, temporal_coverage)

            return {
                'coverage_score': coverage_score,
                'category_coverage': coverage_by_category,
                'temporal_coverage': temporal_coverage,
                'knowledge_gaps': knowledge_gaps,
                'recommendations': self._generate_coverage_recommendations(knowledge_gaps),
                'audit_timestamp': datetime.now().isoformat()
            }

        except Exception as e:
            return {'error': f"Knowledge coverage audit failed: {str(e)}"}

    def audit_system_health(self) -> Dict[str, Any]:
        """
        Perform comprehensive system health audit.

        Returns:
            Dictionary with system health results
        """
        try:
            from context_capture.utils.status import StatusMonitor
            from context_capture.core.queue import QueueManager

            monitor = StatusMonitor(self.config)
            queue_manager = QueueManager(self.config)

            # Get basic system status
            system_status = monitor.get_detailed_status()

            # Analyze queue health
            queue_health = self._analyze_queue_health(queue_manager)

            # Check configuration health
            config_health = self._analyze_config_health()

            # Analyze processing efficiency
            processing_efficiency = self._analyze_processing_efficiency()

            # Calculate overall health score
            health_score = self._calculate_system_health_score(
                system_status, queue_health, config_health, processing_efficiency
            )

            return {
                'health_score': health_score,
                'system_status': system_status,
                'queue_health': queue_health,
                'config_health': config_health,
                'processing_efficiency': processing_efficiency,
                'recommendations': self._generate_system_recommendations(health_score),
                'audit_timestamp': datetime.now().isoformat()
            }

        except Exception as e:
            return {'error': f"System health audit failed: {str(e)}"}

    def _gather_decisions_from_period(self, start_date: datetime, end_date: datetime) -> List[Dict[str, Any]]:
        """Gather decision insights from a time period."""
        decisions = []
        decisions_dir = self.config.knowledge_dir / 'decisions'

        if decisions_dir.exists():
            for md_file in decisions_dir.glob('*.md'):
                file_date = datetime.fromtimestamp(md_file.stat().st_mtime)
                if start_date <= file_date <= end_date:
                    file_decisions = self._parse_insights_from_file(md_file, 'decisions')
                    decisions.extend(file_decisions)

        return decisions

    def _gather_all_insights(self) -> List[Dict[str, Any]]:
        """Gather all insights from knowledge base."""
        insights = []
        categories = ['decisions', 'patterns', 'insights', 'strategies']

        for category in categories:
            cat_dir = self.config.knowledge_dir / category
            if cat_dir.exists():
                for md_file in cat_dir.glob('*.md'):
                    file_insights = self._parse_insights_from_file(md_file, category)
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

    def _detect_decision_conflicts(self, decisions: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
        """Detect conflicting decisions."""
        conflicts = []

        # Group decisions by topic/area
        decision_groups = {}
        for decision in decisions:
            tags = decision.get('tags', '').lower()
            title = decision.get('title', '').lower()

            # Extract key terms for grouping
            key_terms = self._extract_key_terms(f"{tags} {title}")
            for term in key_terms:
                if term not in decision_groups:
                    decision_groups[term] = []
                decision_groups[term].append(decision)

        # Look for conflicts within groups
        for term, group_decisions in decision_groups.items():
            if len(group_decisions) > 1:
                group_conflicts = self._analyze_group_conflicts(group_decisions, term)
                conflicts.extend(group_conflicts)

        return conflicts

    def _extract_key_terms(self, text: str) -> List[str]:
        """Extract key terms from text for conflict detection."""
        # Simple keyword extraction
        keywords = ['api', 'database', 'auth', 'architecture', 'framework', 'security', 'performance']
        found_terms = []

        for keyword in keywords:
            if keyword in text:
                found_terms.append(keyword)

        return found_terms

    def _analyze_group_conflicts(self, decisions: List[Dict[str, Any]], term: str) -> List[Dict[str, Any]]:
        """Analyze conflicts within a group of related decisions."""
        conflicts = []

        # Simple conflict detection based on opposing keywords
        opposing_pairs = [
            ('sql', 'nosql'),
            ('rest', 'graphql'),
            ('monolith', 'microservice'),
            ('sync', 'async'),
        ]

        for i, decision1 in enumerate(decisions):
            for j, decision2 in enumerate(decisions[i+1:], i+1):
                text1 = f"{decision1.get('title', '')} {decision1.get('reasoning', '')}".lower()
                text2 = f"{decision2.get('title', '')} {decision2.get('reasoning', '')}".lower()

                for term1, term2 in opposing_pairs:
                    if term1 in text1 and term2 in text2:
                        conflicts.append({
                            'type': 'opposing_approaches',
                            'area': term,
                            'decision1': decision1,
                            'decision2': decision2,
                            'conflict_terms': [term1, term2]
                        })

        return conflicts

    def _analyze_decision_consistency(self, decisions: List[Dict[str, Any]]) -> Dict[str, Any]:
        """Analyze overall decision consistency."""
        if not self.llm_client.is_available():
            return self._fallback_consistency_analysis(decisions)

        # Use LLM for sophisticated analysis
        prompt = self._create_consistency_analysis_prompt(decisions)
        response = self.llm_client.generate(prompt)

        if response:
            try:
                return json.loads(response)
            except:
                return {'analysis': response, 'score': 0.7}

        return self._fallback_consistency_analysis(decisions)

    def _detect_patterns(self, insights: List[Dict[str, Any]], min_frequency: int) -> List[Dict[str, Any]]:
        """Detect recurring patterns in insights."""
        patterns = []

        # Tool usage patterns
        tool_usage = {}
        for insight in insights:
            tool = insight.get('tool', 'unknown')
            if tool not in tool_usage:
                tool_usage[tool] = []
            tool_usage[tool].append(insight)

        for tool, tool_insights in tool_usage.items():
            if len(tool_insights) >= min_frequency:
                health_score = self._calculate_pattern_health(tool_insights)
                patterns.append({
                    'type': 'tool_usage',
                    'pattern': tool,
                    'frequency': len(tool_insights),
                    'health_score': health_score,
                    'insights': tool_insights
                })

        # Tag patterns
        tag_usage = {}
        for insight in insights:
            tags = insight.get('tags', '').split('#')
            for tag in tags:
                tag = tag.strip()
                if tag:
                    if tag not in tag_usage:
                        tag_usage[tag] = []
                    tag_usage[tag].append(insight)

        for tag, tag_insights in tag_usage.items():
            if len(tag_insights) >= min_frequency:
                health_score = self._calculate_pattern_health(tag_insights)
                patterns.append({
                    'type': 'tag_frequency',
                    'pattern': tag,
                    'frequency': len(tag_insights),
                    'health_score': health_score,
                    'insights': tag_insights
                })

        return patterns

    def _calculate_pattern_health(self, pattern_insights: List[Dict[str, Any]]) -> float:
        """Calculate health score for a pattern."""
        if not pattern_insights:
            return 0.0

        # Factors: recency, importance distribution, consistency
        total_importance = sum(float(i.get('importance', '0.5')) for i in pattern_insights)
        avg_importance = total_importance / len(pattern_insights)

        # Recency factor (recent patterns are healthier)
        recent_count = 0
        cutoff_date = datetime.now() - timedelta(days=7)

        for insight in pattern_insights:
            try:
                insight_date = datetime.fromisoformat(insight.get('datetime', ''))
                if insight_date > cutoff_date:
                    recent_count += 1
            except:
                pass

        recency_factor = recent_count / len(pattern_insights)

        # Combined health score
        health_score = (avg_importance * 0.6) + (recency_factor * 0.4)
        return min(1.0, health_score)

    def _analyze_pattern_health(self, patterns: List[Dict[str, Any]],
                              insights: List[Dict[str, Any]]) -> Dict[str, Any]:
        """Analyze overall pattern health."""
        if not patterns:
            return {'message': 'No patterns found for health analysis'}

        total_health = sum(p.get('health_score', 0) for p in patterns)
        avg_health = total_health / len(patterns)

        healthy_patterns = [p for p in patterns if p.get('health_score', 0) > 0.7]
        unhealthy_patterns = [p for p in patterns if p.get('health_score', 0) < 0.4]

        return {
            'average_health_score': avg_health,
            'healthy_patterns_count': len(healthy_patterns),
            'unhealthy_patterns_count': len(unhealthy_patterns),
            'total_patterns': len(patterns),
            'health_distribution': {
                'healthy': len(healthy_patterns),
                'moderate': len(patterns) - len(healthy_patterns) - len(unhealthy_patterns),
                'unhealthy': len(unhealthy_patterns)
            }
        }

    def _identify_problem_patterns(self, patterns: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
        """Identify problematic patterns."""
        problem_patterns = []

        for pattern in patterns:
            health_score = pattern.get('health_score', 0)
            frequency = pattern.get('frequency', 0)

            problems = []

            if health_score < 0.4:
                problems.append('Low health score')

            if frequency > 20:  # Very high frequency might indicate overuse
                problems.append('Potentially overused')

            # Check for patterns that might indicate bad practices
            pattern_name = pattern.get('pattern', '').lower()
            if any(bad in pattern_name for bad in ['error', 'fail', 'temp', 'hack', 'todo']):
                problems.append('Potentially indicates technical debt')

            if problems:
                problem_patterns.append({
                    **pattern,
                    'problems': problems
                })

        return problem_patterns

    def _analyze_category_coverage(self) -> Dict[str, Any]:
        """Analyze coverage by category."""
        categories = ['decisions', 'patterns', 'insights', 'strategies']
        coverage = {}

        for category in categories:
            cat_dir = self.config.knowledge_dir / category
            if cat_dir.exists():
                files = list(cat_dir.glob('*.md'))
                insights_count = 0

                for file_path in files:
                    try:
                        content = file_path.read_text()
                        insights_count += content.count('---')
                    except:
                        pass

                coverage[category] = {
                    'files': len(files),
                    'insights': insights_count,
                    'coverage_score': min(1.0, insights_count / 10)  # Normalize to 10 insights
                }
            else:
                coverage[category] = {'files': 0, 'insights': 0, 'coverage_score': 0.0}

        return coverage

    def _analyze_temporal_coverage(self) -> Dict[str, Any]:
        """Analyze temporal coverage of insights."""
        now = datetime.now()
        periods = {
            'last_day': now - timedelta(days=1),
            'last_week': now - timedelta(weeks=1),
            'last_month': now - timedelta(days=30)
        }

        coverage = {}
        for period_name, start_date in periods.items():
            insights = self._gather_insights_from_period(start_date, now)
            coverage[period_name] = {
                'insights_count': len(insights),
                'coverage_score': min(1.0, len(insights) / 5)  # Normalize to 5 insights per period
            }

        return coverage

    def _gather_insights_from_period(self, start_date: datetime, end_date: datetime) -> List[Dict[str, Any]]:
        """Gather insights from a time period."""
        insights = []
        categories = ['decisions', 'patterns', 'insights', 'strategies']

        for category in categories:
            cat_dir = self.config.knowledge_dir / category
            if cat_dir.exists():
                for md_file in cat_dir.glob('*.md'):
                    file_date = datetime.fromtimestamp(md_file.stat().st_mtime)
                    if start_date <= file_date <= end_date:
                        file_insights = self._parse_insights_from_file(md_file, category)
                        insights.extend(file_insights)

        return insights

    def _fallback_consistency_analysis(self, decisions: List[Dict[str, Any]]) -> Dict[str, Any]:
        """Fallback consistency analysis without LLM."""
        # Simple heuristic analysis
        unique_tools = set(d.get('tool', 'unknown') for d in decisions)
        unique_tags = set()

        for decision in decisions:
            tags = decision.get('tags', '').split('#')
            unique_tags.update(tag.strip() for tag in tags if tag.strip())

        consistency_score = 0.7  # Default moderate score

        return {
            'score': consistency_score,
            'analysis': f'Analyzed {len(decisions)} decisions with {len(unique_tools)} different tools',
            'metrics': {
                'total_decisions': len(decisions),
                'unique_tools': len(unique_tools),
                'unique_tags': len(unique_tags)
            },
            'fallback': True
        }

    def _generate_consistency_recommendations(self, decisions: List[Dict[str, Any]],
                                           conflicts: List[Dict[str, Any]]) -> List[str]:
        """Generate recommendations for improving consistency."""
        recommendations = []

        if conflicts:
            recommendations.append(f"Address {len(conflicts)} decision conflicts found")

        if len(decisions) < 5:
            recommendations.append("Capture more architectural decisions for better analysis")

        # Tool diversity check
        tools = [d.get('tool', 'unknown') for d in decisions]
        unique_tools = set(tools)
        if len(unique_tools) > len(decisions) * 0.8:
            recommendations.append("Consider standardizing decision-making tools")

        return recommendations

    def _generate_pattern_recommendations(self, patterns: List[Dict[str, Any]],
                                        problem_patterns: List[Dict[str, Any]]) -> List[str]:
        """Generate pattern improvement recommendations."""
        recommendations = []

        if problem_patterns:
            recommendations.append(f"Review {len(problem_patterns)} problematic patterns")

        healthy_count = len([p for p in patterns if p.get('health_score', 0) > 0.7])
        if healthy_count < len(patterns) * 0.6:
            recommendations.append("Focus on improving pattern health through consistent practices")

        return recommendations

    def _identify_knowledge_gaps(self, category_coverage: Dict[str, Any],
                               temporal_coverage: Dict[str, Any]) -> List[str]:
        """Identify knowledge gaps."""
        gaps = []

        # Category gaps
        for category, coverage in category_coverage.items():
            if coverage['coverage_score'] < 0.3:
                gaps.append(f"Low coverage in {category} category")

        # Temporal gaps
        for period, coverage in temporal_coverage.items():
            if coverage['coverage_score'] < 0.3:
                gaps.append(f"Low activity in {period}")

        return gaps

    def _calculate_coverage_score(self, category_coverage: Dict[str, Any],
                                temporal_coverage: Dict[str, Any]) -> float:
        """Calculate overall coverage score."""
        category_scores = [c['coverage_score'] for c in category_coverage.values()]
        temporal_scores = [c['coverage_score'] for c in temporal_coverage.values()]

        avg_category = sum(category_scores) / len(category_scores) if category_scores else 0
        avg_temporal = sum(temporal_scores) / len(temporal_scores) if temporal_scores else 0

        return (avg_category + avg_temporal) / 2

    def _generate_coverage_recommendations(self, gaps: List[str]) -> List[str]:
        """Generate coverage improvement recommendations."""
        recommendations = []

        if gaps:
            recommendations.append("Address identified knowledge gaps")
            for gap in gaps[:3]:  # Top 3 gaps
                recommendations.append(f"Improve: {gap}")

        return recommendations

    def _analyze_queue_health(self, queue_manager) -> Dict[str, Any]:
        """Analyze queue system health."""
        try:
            stats = queue_manager.get_queue_stats()

            # Calculate health metrics
            pending_count = stats.get('pending', {}).get('count', 0)
            processing_count = stats.get('processing', {}).get('count', 0)

            health_score = 1.0
            issues = []

            if pending_count > 100:
                health_score -= 0.3
                issues.append(f"High pending queue: {pending_count} items")

            if processing_count > 10:
                health_score -= 0.2
                issues.append(f"High processing queue: {processing_count} items")

            return {
                'health_score': max(0.0, health_score),
                'queue_stats': stats,
                'issues': issues
            }
        except Exception as e:
            return {'error': f"Queue health analysis failed: {str(e)}"}

    def _analyze_config_health(self) -> Dict[str, Any]:
        """Analyze configuration health."""
        issues = []
        health_score = 1.0

        # Check key configuration values
        threshold = self.config.get('capture.importance_threshold', 0.5)
        if threshold > 0.8:
            issues.append("Very high importance threshold may miss insights")
            health_score -= 0.1

        batch_size = self.config.get('performance.batch_size', 5)
        if batch_size > 20:
            issues.append("Large batch size may impact performance")
            health_score -= 0.1

        return {
            'health_score': max(0.0, health_score),
            'issues': issues,
            'config_values': {
                'importance_threshold': threshold,
                'batch_size': batch_size
            }
        }

    def _analyze_processing_efficiency(self) -> Dict[str, Any]:
        """Analyze processing efficiency."""
        try:
            # Simple efficiency analysis based on queue ratios
            from context_capture.core.queue import QueueManager
            queue_manager = QueueManager(self.config)
            stats = queue_manager.get_queue_stats()

            processed_count = stats.get('processed', {}).get('count', 0)
            pending_count = stats.get('pending', {}).get('count', 0)

            if processed_count + pending_count == 0:
                efficiency_score = 1.0
            else:
                efficiency_score = processed_count / (processed_count + pending_count)

            return {
                'efficiency_score': efficiency_score,
                'processed_items': processed_count,
                'pending_items': pending_count
            }
        except Exception:
            return {'efficiency_score': 0.5, 'note': 'Unable to calculate efficiency'}

    def _calculate_system_health_score(self, system_status: Dict[str, Any],
                                     queue_health: Dict[str, Any],
                                     config_health: Dict[str, Any],
                                     processing_efficiency: Dict[str, Any]) -> float:
        """Calculate overall system health score."""
        scores = [
            1.0 if system_status.get('agent', {}).get('running') else 0.0,  # Agent running
            1.0 if system_status.get('llm', {}).get('ollama_running') else 0.5,  # LLM available
            queue_health.get('health_score', 0.5),
            config_health.get('health_score', 0.5),
            processing_efficiency.get('efficiency_score', 0.5)
        ]

        return sum(scores) / len(scores)

    def _generate_system_recommendations(self, health_score: float) -> List[str]:
        """Generate system improvement recommendations."""
        recommendations = []

        if health_score < 0.7:
            recommendations.append("System health below optimal - review issues")

        if health_score < 0.5:
            recommendations.append("Critical system issues detected - immediate attention needed")

        return recommendations

    def _create_consistency_analysis_prompt(self, decisions: List[Dict[str, Any]]) -> str:
        """Create prompt for LLM consistency analysis."""
        decision_summary = []
        for decision in decisions[:10]:
            decision_summary.append(f"- {decision.get('title', 'Unknown')}: {decision.get('reasoning', '')}")

        return f"""Analyze these architectural decisions for consistency and potential conflicts:

Decisions:
{chr(10).join(decision_summary)}

Provide a JSON response with:
{{
  "score": 0.8,
  "analysis": "Overall consistency assessment",
  "strengths": ["What's working well"],
  "concerns": ["Potential issues"],
  "recommendations": ["How to improve consistency"]
}}"""