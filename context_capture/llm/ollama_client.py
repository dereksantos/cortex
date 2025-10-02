"""
Ollama client for local LLM integration.
"""

import json
import time
from typing import Any, Dict, Optional

import requests
from context_capture.utils.config import Config


class OllamaClient:
    """Client for Ollama local LLM service."""

    def __init__(self, config: Optional[Config] = None, model: Optional[str] = None,
                 timeout: Optional[int] = None, base_url: str = "http://localhost:11434"):
        """
        Initialize Ollama client.

        Args:
            config: Configuration object
            model: Model name (overrides config)
            timeout: Request timeout in seconds
            base_url: Ollama service URL
        """
        self.config = config or Config()
        self.model = model or self.config.get('model.name', 'mistral:7b')
        self.timeout = timeout or self.config.get('model.timeout', 30)
        self.base_url = base_url.rstrip('/')

    def is_available(self) -> bool:
        """
        Check if Ollama service is available.

        Returns:
            True if service is reachable, False otherwise
        """
        try:
            response = requests.get(f"{self.base_url}/api/version", timeout=5)
            return response.status_code == 200
        except:
            return False

    def list_models(self) -> Optional[Dict[str, Any]]:
        """
        List available models.

        Returns:
            Dictionary with model information, or None if failed
        """
        try:
            response = requests.get(f"{self.base_url}/api/tags", timeout=10)
            if response.status_code == 200:
                return response.json()
        except:
            pass
        return None

    def is_model_available(self, model_name: Optional[str] = None) -> bool:
        """
        Check if specific model is available.

        Args:
            model_name: Model name (uses configured model if None)

        Returns:
            True if model is available, False otherwise
        """
        model_name = model_name or self.model
        models_info = self.list_models()

        if not models_info or 'models' not in models_info:
            return False

        available_models = [m['name'] for m in models_info['models']]
        return model_name in available_models

    def generate(self, prompt: str, **kwargs) -> Optional[str]:
        """
        Generate text using the model.

        Args:
            prompt: Input prompt
            **kwargs: Additional generation parameters

        Returns:
            Generated text, or None if failed
        """
        try:
            # Prepare generation parameters
            generation_params = {
                'model': self.model,
                'prompt': prompt,
                'stream': False,
                'options': {
                    'temperature': self.config.get('model.temperature', 0.3),
                    'num_predict': self.config.get('model.max_tokens', 500),
                }
            }

            # Override with provided parameters
            generation_params.update(kwargs)

            # Make request
            response = requests.post(
                f"{self.base_url}/api/generate",
                json=generation_params,
                timeout=self.timeout
            )

            if response.status_code == 200:
                result = response.json()
                return result.get('response', '')

        except Exception as e:
            if self.config.get('features.debug_mode', False):
                print(f"Ollama generation error: {e}")

        return None

    def analyze_event(self, event_data: Dict[str, Any]) -> Optional[Dict[str, Any]]:
        """
        Analyze an event using the LLM.

        Args:
            event_data: Event data to analyze

        Returns:
            Analysis result dictionary, or None if failed
        """
        prompt = create_analysis_prompt(event_data)
        response = self.generate(prompt)

        if response:
            return parse_analysis_response(response)

        return None


def create_analysis_prompt(event_data: Dict[str, Any]) -> str:
    """
    Create analysis prompt for event data.

    Args:
        event_data: Event data to analyze

    Returns:
        Formatted prompt string
    """
    tool_name = event_data.get('tool_name', 'Unknown')
    tool_input = event_data.get('tool_input', {})
    tool_result = event_data.get('tool_result_preview', '')
    datetime_str = event_data.get('datetime', '')

    prompt = f"""Analyze this Claude Code development event and determine its importance and category.

Event Details:
- Tool: {tool_name}
- Input: {json.dumps(tool_input, indent=2)[:500]}
- Result: {tool_result[:500]}
- Time: {datetime_str}

Analyze this event and respond with a JSON object containing:
{{
  "importance": <float 0.0-1.0>,
  "category": "<decisions|patterns|insights|strategies>",
  "reasoning": "<brief explanation>",
  "tags": ["<tag1>", "<tag2>"],
  "summary": "<one-line summary>",
  "should_capture": <boolean>
}}

Focus on:
- Architectural decisions (0.8+ importance)
- Development patterns (0.6+ importance)
- Key insights or learnings (0.7+ importance)
- Strategic choices (0.8+ importance)
- Problem-solving approaches (0.6+ importance)

Skip routine operations like file reads, simple commands, or temporary actions.

Response (JSON only):"""

    return prompt


def parse_analysis_response(response: str) -> Optional[Dict[str, Any]]:
    """
    Parse LLM analysis response.

    Args:
        response: Raw LLM response

    Returns:
        Parsed analysis dictionary, or None if parsing failed
    """
    try:
        # Try to extract JSON from response
        response = response.strip()

        # Find JSON block
        if '{' in response and '}' in response:
            start = response.find('{')
            end = response.rfind('}') + 1
            json_str = response[start:end]

            result = json.loads(json_str)

            # Validate required fields
            required_fields = ['importance', 'category', 'should_capture']
            if all(field in result for field in required_fields):
                # Ensure importance is in valid range
                result['importance'] = max(0.0, min(1.0, float(result['importance'])))
                return result

    except Exception:
        pass

    return None


def create_fallback_analysis(event_data: Dict[str, Any]) -> Dict[str, Any]:
    """
    Create fallback analysis when LLM is unavailable.

    Args:
        event_data: Event data to analyze

    Returns:
        Basic analysis dictionary
    """
    tool_name = event_data.get('tool_name', '')
    tool_input = event_data.get('tool_input', {})

    # Simple heuristic scoring
    importance = 0.5
    category = 'insights'
    tags = []

    # Decision-like tools
    if tool_name in ['Task', 'TodoWrite']:
        importance = 0.8
        category = 'decisions'
        tags.append('planning')

    # Code editing
    elif tool_name in ['Edit', 'MultiEdit', 'Write']:
        importance = 0.7
        category = 'patterns'
        tags.extend(['code', 'implementation'])

    # Performance/testing related
    elif tool_name == 'Bash':
        command = str(tool_input.get('command', '')).lower()
        if any(keyword in command for keyword in ['test', 'build', 'deploy', 'install']):
            importance = 0.6
            category = 'strategies'
            tags.append('deployment')

    return {
        'importance': importance,
        'category': category,
        'reasoning': f'Fallback analysis for {tool_name} tool',
        'tags': tags,
        'summary': f'{tool_name} operation',
        'should_capture': importance >= 0.5,
        'fallback': True
    }