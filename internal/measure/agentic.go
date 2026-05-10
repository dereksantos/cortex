package measure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/pkg/llm"
)

const agenticSystemPrompt = "You are a prompt quality evaluator for AI coding assistants. Evaluate prompts for scope, clarity, and decomposability. Return only valid JSON with no markdown formatting or additional text."

// measureAgentic runs all agentic measurements and combines results.
func measureAgentic(ctx context.Context, provider llm.Provider, prompt string, contextWindow int) (*AgenticResult, error) {
	result := &AgenticResult{}

	// Scope classification
	scope, explanation, err := classifyScope(ctx, provider, prompt)
	if err != nil {
		return nil, fmt.Errorf("classify scope: %w", err)
	}
	result.ScopeClassification = scope
	result.ScopeExplanation = explanation

	// Clarity assessment
	clarity, ambiguities, missing, err := assessClarity(ctx, provider, prompt)
	if err != nil {
		return nil, fmt.Errorf("assess clarity: %w", err)
	}
	result.ClarityScore = clarity
	result.Ambiguities = ambiguities
	result.MissingConstraints = missing

	// Decomposability check
	decomposable, subTasks, independent, err := checkDecomposability(ctx, provider, prompt)
	if err != nil {
		return nil, fmt.Errorf("check decomposability: %w", err)
	}
	result.Decomposable = decomposable
	result.SubTasks = subTasks
	result.IndependentSubs = independent

	// Context window fit
	fit, fitExplanation, err := scoreContextWindowFit(ctx, provider, prompt, contextWindow)
	if err != nil {
		return nil, fmt.Errorf("score context window fit: %w", err)
	}
	result.ContextWindowFit = fit
	result.FitExplanation = fitExplanation

	return result, nil
}

// scopeResponse is the expected JSON from scope classification.
type scopeResponse struct {
	Classification string `json:"classification"`
	Explanation    string `json:"explanation"`
}

func classifyScope(ctx context.Context, provider llm.Provider, prompt string) (string, string, error) {
	p := fmt.Sprintf(`Classify the scope of this coding prompt. How large/complex is the expected change?

Prompt: %s

Classifications:
- trivial: Single line change, rename, typo fix
- small: Single function or small targeted fix
- medium: Multi-function changes within a single file
- large: Multi-file changes, architectural modifications
- epic: Cross-cutting system-wide changes

Return JSON: {"classification": "trivial|small|medium|large|epic", "explanation": "brief reason"}`, prompt)

	resp, err := provider.GenerateWithSystem(ctx, p, agenticSystemPrompt)
	if err != nil {
		return "medium", "LLM unavailable", err
	}

	var result scopeResponse
	if err := parseJSON(resp, &result); err != nil {
		return "medium", resp, nil
	}

	valid := map[string]bool{"trivial": true, "small": true, "medium": true, "large": true, "epic": true}
	if !valid[result.Classification] {
		result.Classification = "medium"
	}

	return result.Classification, result.Explanation, nil
}

// clarityResponse is the expected JSON from clarity assessment.
type clarityResponse struct {
	ClarityScore       float64  `json:"clarity_score"`
	Ambiguities        []string `json:"ambiguities"`
	MissingConstraints []string `json:"missing_constraints"`
}

func assessClarity(ctx context.Context, provider llm.Provider, prompt string) (float64, []string, []string, error) {
	p := fmt.Sprintf(`Evaluate the clarity of this coding prompt. A clear prompt is unambiguous, specific, and well-constrained.

Prompt: %s

Evaluate:
1. clarity_score: 0.0-1.0 (1.0 = perfectly clear, no ambiguity)
2. ambiguities: List any ambiguous parts that could be interpreted multiple ways
3. missing_constraints: List important constraints that should be specified but aren't

Return JSON: {"clarity_score": 0.X, "ambiguities": ["..."], "missing_constraints": ["..."]}`, prompt)

	resp, err := provider.GenerateWithSystem(ctx, p, agenticSystemPrompt)
	if err != nil {
		return 0.5, nil, nil, err
	}

	var result clarityResponse
	if err := parseJSON(resp, &result); err != nil {
		return 0.5, nil, nil, nil
	}

	result.ClarityScore = clamp(result.ClarityScore, 0, 1)
	return result.ClarityScore, result.Ambiguities, result.MissingConstraints, nil
}

// decomposabilityResponse is the expected JSON from decomposability check.
type decomposabilityResponse struct {
	Decomposable    bool     `json:"decomposable"`
	SubTasks        []string `json:"sub_tasks"`
	IndependentSubs int      `json:"independent_subs"`
}

func checkDecomposability(ctx context.Context, provider llm.Provider, prompt string) (bool, []string, int, error) {
	p := fmt.Sprintf(`Analyze whether this coding prompt should be broken into smaller, independent sub-tasks for better results with small context windows.

Prompt: %s

Evaluate:
1. decomposable: true if the prompt contains multiple independent concerns that would be better as separate prompts
2. sub_tasks: If decomposable, list the independent sub-tasks
3. independent_subs: How many of the sub-tasks can be done independently (no dependencies between them)

Return JSON: {"decomposable": true|false, "sub_tasks": ["..."], "independent_subs": N}`, prompt)

	resp, err := provider.GenerateWithSystem(ctx, p, agenticSystemPrompt)
	if err != nil {
		return false, nil, 0, err
	}

	var result decomposabilityResponse
	if err := parseJSON(resp, &result); err != nil {
		return false, nil, 0, nil
	}

	return result.Decomposable, result.SubTasks, result.IndependentSubs, nil
}

// contextFitResponse is the expected JSON from context window fit scoring.
type contextFitResponse struct {
	FitScore    float64 `json:"fit_score"`
	Explanation string  `json:"explanation"`
}

func scoreContextWindowFit(ctx context.Context, provider llm.Provider, prompt string, windowTokens int) (float64, string, error) {
	p := fmt.Sprintf(`Estimate whether the response to this coding prompt will fit within a %d token context window.

Prompt: %s

Consider:
- How much code needs to be written or modified
- How much explanation/documentation is expected
- Whether the response needs to include file paths, imports, tests, etc.

Return JSON: {"fit_score": 0.X, "explanation": "brief reason"}
- 1.0 = easily fits with room to spare
- 0.7 = fits but tight
- 0.4 = may not fit, would benefit from decomposition
- 0.1 = very unlikely to fit`, windowTokens, prompt)

	resp, err := provider.GenerateWithSystem(ctx, p, agenticSystemPrompt)
	if err != nil {
		return 0.5, "LLM unavailable", err
	}

	var result contextFitResponse
	if err := parseJSON(resp, &result); err != nil {
		return 0.5, resp, nil
	}

	result.FitScore = clamp(result.FitScore, 0, 1)
	return result.FitScore, result.Explanation, nil
}

// parseJSON extracts and parses JSON from an LLM response.
func parseJSON(response string, target interface{}) error {
	// Try direct parse first
	if err := json.Unmarshal([]byte(response), target); err == nil {
		return nil
	}

	// Extract JSON from response (handle markdown wrapping)
	jsonStart := strings.Index(response, "{")
	jsonEnd := strings.LastIndex(response, "}")
	if jsonStart >= 0 && jsonEnd > jsonStart {
		jsonStr := response[jsonStart : jsonEnd+1]
		if err := json.Unmarshal([]byte(jsonStr), target); err == nil {
			return nil
		}
	}

	return fmt.Errorf("no valid JSON found in response")
}
