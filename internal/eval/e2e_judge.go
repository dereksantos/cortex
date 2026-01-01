package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/pkg/llm"
)

// CodeReviewSystemPrompt guides the LLM to evaluate code semantically.
const CodeReviewSystemPrompt = `You are a code review judge evaluating generated code against specific criteria.
Your job is to perform SEMANTIC evaluation - not just pattern matching.

For each criterion, determine if the code meets the requirement by understanding:
- The intent behind the code, not just surface patterns
- Whether the implementation correctly achieves the criterion's goal
- The quality and correctness of the approach

Respond in JSON format:
{
  "evaluations": [
    {
      "criterion": "The exact criterion text",
      "passed": true/false,
      "confidence": 0.0-1.0,
      "reasoning": "Brief explanation of why the code passes or fails this criterion"
    }
  ]
}

Guidelines:
- Be thorough but concise in your reasoning
- Confidence should reflect how certain you are (1.0 = certain, 0.5 = uncertain)
- Focus on semantic correctness, not just syntactic patterns
- Consider edge cases and potential issues
- If code is incomplete or has errors, mark relevant criteria as failed`

// CodeReviewJudge uses an LLM to evaluate generated code against criteria.
type CodeReviewJudge struct {
	provider llm.Provider
}

// NewCodeReviewJudge creates a new CodeReviewJudge instance.
// If provider is nil, EvaluateCode will gracefully return nil results.
func NewCodeReviewJudge(provider llm.Provider) *CodeReviewJudge {
	return &CodeReviewJudge{
		provider: provider,
	}
}

// IsAvailable returns true if the judge can perform evaluations.
func (j *CodeReviewJudge) IsAvailable() bool {
	return j.provider != nil && j.provider.IsAvailable()
}

// EvaluateCode uses the LLM to evaluate generated code against criteria.
// Returns nil (not error) for graceful degradation when:
// - Provider is nil or unavailable
// - No criteria provided
// - JSON parsing fails
func (j *CodeReviewJudge) EvaluateCode(ctx context.Context, generatedCode map[string]string, criteria []string) ([]CodeReviewResult, error) {
	// Graceful degradation: return nil if provider unavailable
	if j.provider == nil || !j.provider.IsAvailable() {
		return nil, nil
	}

	// Graceful degradation: return nil if no criteria
	if len(criteria) == 0 {
		return nil, nil
	}

	// Build evaluation prompt
	prompt := j.buildEvaluationPrompt(generatedCode, criteria)

	// Call LLM
	response, err := j.provider.GenerateWithSystem(ctx, prompt, CodeReviewSystemPrompt)
	if err != nil {
		// Graceful degradation: return nil on LLM errors
		return nil, nil
	}

	// Parse response
	results := j.parseJudgeResponse(response, criteria)
	return results, nil
}

// buildEvaluationPrompt creates the prompt for code evaluation.
func (j *CodeReviewJudge) buildEvaluationPrompt(generatedCode map[string]string, criteria []string) string {
	var sb strings.Builder

	sb.WriteString("Evaluate the following generated code against the given criteria.\n\n")

	sb.WriteString("=== GENERATED CODE ===\n\n")

	for filePath, content := range generatedCode {
		sb.WriteString(fmt.Sprintf("--- %s ---\n", filePath))
		sb.WriteString(content)
		sb.WriteString("\n\n")
	}

	sb.WriteString("=== CRITERIA TO EVALUATE ===\n\n")

	for i, criterion := range criteria {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, criterion))
	}

	sb.WriteString("\nEvaluate each criterion and respond in JSON format.")

	return sb.String()
}

// judgeResponse represents the LLM's evaluation output.
type judgeResponse struct {
	Evaluations []judgeEvaluation `json:"evaluations"`
}

// judgeEvaluation represents a single criterion evaluation from the LLM.
type judgeEvaluation struct {
	Criterion  string  `json:"criterion"`
	Passed     bool    `json:"passed"`
	Confidence float64 `json:"confidence"`
	Reasoning  string  `json:"reasoning"`
}

// parseJudgeResponse parses the LLM response and extracts evaluation results.
// Handles missing criteria by creating failed results for them.
func (j *CodeReviewJudge) parseJudgeResponse(response string, criteria []string) []CodeReviewResult {
	// Find JSON in response
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")

	if start == -1 || end == -1 || end <= start {
		// Graceful degradation: return nil if no JSON found
		return nil
	}

	jsonStr := response[start : end+1]

	var jr judgeResponse
	if err := json.Unmarshal([]byte(jsonStr), &jr); err != nil {
		// Graceful degradation: return nil if JSON parsing fails
		return nil
	}

	// Build map of criterion to evaluation for quick lookup
	evalMap := make(map[string]judgeEvaluation)
	for _, eval := range jr.Evaluations {
		evalMap[eval.Criterion] = eval
	}

	// Build results, handling missing criteria
	results := make([]CodeReviewResult, 0, len(criteria))

	for _, criterion := range criteria {
		if eval, ok := evalMap[criterion]; ok {
			results = append(results, CodeReviewResult{
				Criterion:  criterion,
				Passed:     eval.Passed,
				Reasoning:  eval.Reasoning,
				Confidence: eval.Confidence,
			})
		} else {
			// Try fuzzy matching - criterion might be slightly different
			found := false
			for evalCriterion, eval := range evalMap {
				if strings.Contains(strings.ToLower(evalCriterion), strings.ToLower(criterion)) ||
					strings.Contains(strings.ToLower(criterion), strings.ToLower(evalCriterion)) {
					results = append(results, CodeReviewResult{
						Criterion:  criterion,
						Passed:     eval.Passed,
						Reasoning:  eval.Reasoning,
						Confidence: eval.Confidence,
					})
					found = true
					break
				}
			}

			if !found {
				// Criterion was not evaluated by LLM - mark as failed with explanation
				results = append(results, CodeReviewResult{
					Criterion:  criterion,
					Passed:     false,
					Reasoning:  "Criterion was not evaluated by the judge",
					Confidence: 0.0,
				})
			}
		}
	}

	return results
}
