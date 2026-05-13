package cognition

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/dereksantos/cortex/internal/journal"
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

	// Journal output (slice R1). When set, Reflect emits reflect.rerank
	// entries to <journalDir>/reflect/ after each rerank operation.
	journalDir string
}

// SetJournalDir wires the project's <ContextDir>/journal/ root for
// reflect.rerank emission. Empty disables journal emission.
func (r *Reflect) SetJournalDir(dir string) { r.journalDir = dir }

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
	reranked, parsed, err := r.parseRerankResponseV2(response, candidates)
	if err != nil {
		// Graceful degradation: return candidates as-is
		return candidates, nil
	}

	// Best-effort journal emission (slice R1). Errors logged, never returned.
	r.emitRerankToJournal(q, candidates, reranked, parsed)

	return reranked, nil
}

// emitRerankToJournal writes a reflect.rerank entry capturing this
// rerank's inputs, outputs, and contradictions. No-op when journalDir is
// empty.
func (r *Reflect) emitRerankToJournal(q cognition.Query, input, ranked []cognition.Result, parsed rerankResponse) {
	if r.journalDir == "" || len(ranked) == 0 {
		return
	}
	inputIDs := make([]string, len(input))
	inputContents := make(map[string]string, len(input))
	for i, c := range input {
		inputIDs[i] = c.ID
		// Snapshot content so counterfactual replay (slice X2) can
		// reconstruct candidates without a storage round-trip. Skipped
		// when ID is empty or content already captured (last-write-wins
		// for duplicate IDs in input — Reflect treats them as one).
		if c.ID != "" {
			inputContents[c.ID] = c.Content
		}
	}
	rankedIDs := make([]string, len(ranked))
	for i, c := range ranked {
		rankedIDs[i] = c.ID
	}
	var contradictions []journal.ContradictionRecord
	for _, c := range parsed.Contradictions {
		contradictions = append(contradictions, journal.ContradictionRecord{
			IDs:    c.IDs,
			Reason: c.Reason,
		})
	}
	payload := journal.ReflectRerankPayload{
		QueryText:      q.Text,
		InputIDs:       inputIDs,
		InputContents:  inputContents,
		RankedIDs:      rankedIDs,
		Contradictions: contradictions,
		Reasoning:      parsed.Reasoning,
	}
	entry, err := journal.NewReflectRerankEntry(payload)
	if err != nil {
		log.Printf("reflect: build journal entry: %v", err)
		return
	}
	classDir := filepath.Join(r.journalDir, "reflect")
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: classDir,
		Fsync:    journal.FsyncPerBatch,
	})
	if err != nil {
		log.Printf("reflect: open journal writer: %v", err)
		return
	}
	defer w.Close()
	if _, err := w.Append(entry); err != nil {
		log.Printf("reflect: append journal entry: %v", err)
	}
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

// parseRerankResponseV2 wraps parseRerankResponse to also surface the
// parsed rerankResponse, which the journal emitter needs to record
// contradictions.
func (r *Reflect) parseRerankResponseV2(response string, candidates []cognition.Result) ([]cognition.Result, rerankResponse, error) {
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")
	if start == -1 || end == -1 || end <= start {
		return candidates, rerankResponse{}, fmt.Errorf("no JSON found in response")
	}
	var rr rerankResponse
	if err := json.Unmarshal([]byte(response[start:end+1]), &rr); err != nil {
		return candidates, rerankResponse{}, fmt.Errorf("failed to parse JSON: %w", err)
	}
	reranked, _ := r.parseRerankResponse(response, candidates)
	return reranked, rr, nil
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
