// Package llm provides LLM client implementations
package llm

import (
	"context"
	"encoding/json"
	"strings"
)

// AnalysisSystemPrompt guides the LLM to extract coding-productivity-relevant context
const AnalysisSystemPrompt = `You are a context extraction system for AI coding assistants. Your goal is to identify information from development events that will help future coding sessions be more productive.

Extract context that is:
- ACTIONABLE: Decisions, conventions, or patterns to follow in future code
- DURABLE: Won't be stale in a week (not "fixed bug in line 42")
- TEACHABLE: Can be applied to similar future situations

Prioritize extracting:
- Explicit decisions ("we chose X over Y because...")
- User corrections ("don't do X, do Y instead")
- Project conventions (naming, structure, preferred libraries)
- Architectural constraints (what NOT to do, technologies to avoid)
- Patterns that should be replicated
- Error handling approaches
- Testing strategies

Ignore or mark as low importance:
- Routine edits without decisions
- Debugging steps that led nowhere
- File contents without context about why
- Temporary workarounds
- Version-specific fixes

When you identify a constraint (something NOT to do), make it explicit and prominent.
For example: "CONSTRAINT: No Redis - use PostgreSQL for all data storage"

Respond concisely. Future AI assistants will use your extracted context to give better, project-aware suggestions.`

// Provider defines the interface for LLM backends
type Provider interface {
	// Generate produces a response for the given prompt
	Generate(ctx context.Context, prompt string) (string, error)

	// GenerateWithSystem includes system context (for context injection)
	GenerateWithSystem(ctx context.Context, prompt, system string) (string, error)

	// IsAvailable checks if the provider is ready
	IsAvailable() bool

	// Name returns the provider identifier
	Name() string
}

// GenerateRequest represents a generation request
type GenerateRequest struct {
	Prompt string
	System string // Optional system/context message
}

// GenerateResponse represents a generation response
type GenerateResponse struct {
	Output  string
	Model   string
	Latency int64 // milliseconds
}

// Analysis represents the structured analysis of an event
type Analysis struct {
	Summary    string   `json:"summary"`
	Category   string   `json:"category"`
	Importance int      `json:"importance"`
	Tags       []string `json:"tags"`
	Reasoning  string   `json:"reasoning"`
}

// parseAnalysisJSON parses the LLM response into structured analysis
func parseAnalysisJSON(response string) (*Analysis, error) {
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")

	if start == -1 || end == -1 || end <= start {
		return nil, nil
	}

	jsonStr := response[start : end+1]

	var analysis Analysis
	if err := json.Unmarshal([]byte(jsonStr), &analysis); err != nil {
		return nil, err
	}

	return &analysis, nil
}
