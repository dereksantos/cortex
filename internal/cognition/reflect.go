package cognition

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/llm"
)

// ReflectSystemPrompt guides the LLM to rerank and analyze candidates.
const ReflectSystemPrompt = `You are a context relevance evaluator for an AI coding assistant.
Given a query and candidate context results, your job is to:

1. RERANK the candidates by relevance to the query
2. DETECT any contradictions between candidates (e.g., one says "use X", another says "avoid X")

Respond in JSON format:
{
  "ranking": ["id1", "id2", "id3"],
  "contradictions": [
    {"ids": ["id1", "id2"], "reason": "Brief explanation of conflict"}
  ],
  "reasoning": "Brief explanation of your ranking logic"
}

Guidelines:
- Put the most relevant candidates first in the ranking
- Only include candidates that are actually relevant (you can omit irrelevant ones)
- Only report contradictions when candidates genuinely conflict, not just cover different topics
- Be concise in your reasoning`

// Reflect implements cognition.Reflector for LLM-based reranking.
// Takes ~200ms+ due to LLM call.
type Reflect struct {
	llm llm.Provider

	// ActivityLogger for logging contradictions to activity.log
	activityLogger *ActivityLogger
}

// NewReflect creates a new Reflect instance.
// If llm is nil, Reflect will gracefully degrade to returning candidates as-is.
func NewReflect(provider llm.Provider) *Reflect {
	return &Reflect{
		llm: provider,
	}
}

// SetActivityLogger sets the activity logger for contradiction logging.
func (r *Reflect) SetActivityLogger(logger *ActivityLogger) {
	r.activityLogger = logger
}

// Reflect reranks candidates using LLM-based relevance evaluation.
// Also detects contradictions between candidates.
func (r *Reflect) Reflect(ctx context.Context, q cognition.Query, candidates []cognition.Result) ([]cognition.Result, error) {
	// If no LLM or no candidates, return as-is
	if r.llm == nil || !r.llm.IsAvailable() || len(candidates) == 0 {
		return candidates, nil
	}

	// Build reranking prompt
	prompt := r.buildRerankPrompt(q, candidates)

	// Call LLM
	response, err := r.llm.GenerateWithSystem(ctx, prompt, ReflectSystemPrompt)
	if err != nil {
		// Graceful degradation: return candidates as-is
		return candidates, nil
	}

	// Parse response
	reranked, err := r.parseRerankResponse(response, candidates)
	if err != nil {
		// Graceful degradation: return candidates as-is
		return candidates, nil
	}

	return reranked, nil
}

// buildRerankPrompt creates the prompt for LLM reranking.
func (r *Reflect) buildRerankPrompt(q cognition.Query, candidates []cognition.Result) string {
	var sb strings.Builder

	sb.WriteString("Query: ")
	sb.WriteString(q.Text)
	sb.WriteString("\n\n")

	sb.WriteString("Candidates:\n\n")

	for i, c := range candidates {
		fmt.Fprintf(&sb, "%d. [ID: %s]\n", i+1, c.ID)
		fmt.Fprintf(&sb, "   Category: %s\n", c.Category)
		fmt.Fprintf(&sb, "   Content: %s\n", truncate(c.Content, 300))
		if len(c.Tags) > 0 {
			fmt.Fprintf(&sb, "   Tags: %s\n", strings.Join(c.Tags, ", "))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\nRerank these candidates by relevance to the query. Identify any contradictions.")

	return sb.String()
}

// rerankResponse represents the LLM's reranking output.
type rerankResponse struct {
	Ranking        []string        `json:"ranking"`
	Contradictions []contradiction `json:"contradictions"`
	Reasoning      string          `json:"reasoning"`
}

// contradiction represents a detected conflict between candidates.
type contradiction struct {
	IDs    []string `json:"ids"`
	Reason string   `json:"reason"`
}

// parseRerankResponse parses the LLM response and reorders candidates.
func (r *Reflect) parseRerankResponse(response string, candidates []cognition.Result) ([]cognition.Result, error) {
	// Find JSON in response
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")

	if start == -1 || end == -1 || end <= start {
		return candidates, fmt.Errorf("no JSON found in response")
	}

	jsonStr := response[start : end+1]

	var rr rerankResponse
	if err := json.Unmarshal([]byte(jsonStr), &rr); err != nil {
		return candidates, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Build ID to candidate map
	candidateMap := make(map[string]cognition.Result)
	for _, c := range candidates {
		candidateMap[c.ID] = c
	}

	// Drop any IDs from ranking/contradictions that weren't in the
	// input candidate set. A poisoned candidate can otherwise fabricate
	// IDs (either to inject phantom results, or to fabricate conflicts
	// against real entries to discredit them).
	rr.Ranking = filterKnownIDs(rr.Ranking, candidateMap)
	rr.Contradictions = filterKnownContradictions(rr.Contradictions, candidateMap)

	// Log detected contradictions for noise ratio analysis
	if r.activityLogger != nil && len(rr.Contradictions) > 0 {
		for _, cont := range rr.Contradictions {
			if len(cont.IDs) >= 2 {
				// Get summaries from the candidate contents
				insight1 := r.getSummary(candidateMap, cont.IDs[0])
				insight2 := r.getSummary(candidateMap, cont.IDs[1])
				// Ignoring error - logging should not block reranking
				_ = r.activityLogger.LogContradiction(insight1, insight2, cont.Reason)
			}
		}
	}

	// Reorder based on ranking
	var reranked []cognition.Result
	seenIDs := make(map[string]bool)

	for i, id := range rr.Ranking {
		if c, ok := candidateMap[id]; ok {
			// Update score based on new position
			c.Score = 1.0 - (float64(i) * 0.1)
			if c.Score < 0.1 {
				c.Score = 0.1
			}

			// Mark contradictions in metadata
			for _, cont := range rr.Contradictions {
				if containsID(cont.IDs, id) {
					if c.Metadata == nil {
						c.Metadata = make(map[string]any)
					}
					c.Metadata["contradiction"] = cont.Reason
					c.Metadata["conflicts_with"] = cont.IDs
				}
			}

			reranked = append(reranked, c)
			seenIDs[id] = true
		}
	}

	// Add any candidates that weren't in the ranking (LLM might have omitted some)
	for _, c := range candidates {
		if !seenIDs[c.ID] {
			c.Score = 0.05 // Low score for omitted candidates
			reranked = append(reranked, c)
		}
	}

	return reranked, nil
}

// containsID checks if ids contains the target id.
func containsID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

// filterKnownIDs returns only the IDs that are present in candidateMap,
// preserving order. Used to drop fabricated IDs returned by Reflect.
func filterKnownIDs(ids []string, candidateMap map[string]cognition.Result) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := candidateMap[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// filterKnownContradictions drops any contradiction that references an
// ID not in candidateMap. Even one fabricated ID invalidates the whole
// contradiction — the LLM's reasoning was based on a non-existent peer.
func filterKnownContradictions(conts []contradiction, candidateMap map[string]cognition.Result) []contradiction {
	out := make([]contradiction, 0, len(conts))
	for _, c := range conts {
		allKnown := true
		for _, id := range c.IDs {
			if _, ok := candidateMap[id]; !ok {
				allKnown = false
				break
			}
		}
		if allKnown {
			out = append(out, c)
		}
	}
	return out
}

// truncate shortens a string to maxLen, adding "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// getSummary extracts a brief summary from a candidate for logging.
// Returns the truncated content or the ID if content is not available.
func (r *Reflect) getSummary(candidateMap map[string]cognition.Result, id string) string {
	if c, ok := candidateMap[id]; ok {
		summary := c.Content
		if summary == "" {
			summary = id
		}
		return truncate(summary, 50)
	}
	return id
}
