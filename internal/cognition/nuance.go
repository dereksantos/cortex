package cognition

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/pkg/llm"
)

// NuanceExtractionPrompt asks LLM to identify implementation gotchas.
const NuanceExtractionPrompt = `Given this coding pattern/decision:

%s

What are the common implementation gotchas or easy-to-miss details?

Focus on:
1. ORDERING issues - what must happen before or after something else
2. EDGE CASES - nil checks, empty inputs, boundary conditions
3. INTEGRATION gotchas - how this interacts with error handling, returns, cleanup

Keep each nuance to one specific, actionable detail.

Respond in JSON:
{
  "nuances": [
    {"detail": "specific thing to remember", "why": "brief reason"}
  ]
}

If nothing notable, respond: NO_NUANCE`

// Nuance represents an implementation detail extracted from a pattern.
type Nuance struct {
	Detail string `json:"detail"`
	Why    string `json:"why"`
}

// nuanceResponse represents the LLM's nuance extraction output.
type nuanceResponse struct {
	Nuances []Nuance `json:"nuances"`
}

// ExtractNuances uses LLM to find implementation gotchas for a pattern.
// Returns nil, nil if the LLM is unavailable or finds nothing notable.
func ExtractNuances(ctx context.Context, provider llm.Provider, patternContent string) ([]Nuance, error) {
	if provider == nil || !provider.IsAvailable() {
		return nil, nil
	}

	if patternContent == "" {
		return nil, nil
	}

	// Truncate content if too long
	content := patternContent
	if len(content) > 1500 {
		content = content[:1500] + "..."
	}

	prompt := fmt.Sprintf(NuanceExtractionPrompt, content)

	response, err := provider.GenerateWithSystem(ctx, prompt, llm.AnalysisSystemPrompt)
	if err != nil {
		return nil, fmt.Errorf("failed to extract nuances: %w", err)
	}

	// Check for NO_NUANCE response
	if strings.Contains(strings.ToUpper(response), "NO_NUANCE") {
		return nil, nil
	}

	// Parse JSON response
	nuances, err := parseNuanceResponse(response)
	if err != nil {
		// Graceful degradation: return nil on parse error
		return nil, nil
	}

	return nuances, nil
}

// parseNuanceResponse parses the LLM response into nuances.
func parseNuanceResponse(response string) ([]Nuance, error) {
	// Find JSON in response
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")

	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no JSON found in response")
	}

	jsonStr := response[start : end+1]

	var nr nuanceResponse
	if err := json.Unmarshal([]byte(jsonStr), &nr); err != nil {
		return nil, fmt.Errorf("failed to parse nuance JSON: %w", err)
	}

	return nr.Nuances, nil
}
