// Package llm provides LLM client implementations
package llm

import (
	"context"
	"encoding/json"
	"regexp"
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

// GenerationStats holds token usage statistics from an LLM call.
type GenerationStats struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// TotalTokens returns the sum of input and output tokens.
func (s GenerationStats) TotalTokens() int {
	return s.InputTokens + s.OutputTokens
}

// Provider defines the interface for LLM backends
type Provider interface {
	// Generate produces a response for the given prompt
	Generate(ctx context.Context, prompt string) (string, error)

	// GenerateWithSystem includes system context (for context injection)
	GenerateWithSystem(ctx context.Context, prompt, system string) (string, error)

	// GenerateWithStats produces a response and returns token usage statistics
	GenerateWithStats(ctx context.Context, prompt string) (string, GenerationStats, error)

	// IsAvailable checks if the provider is ready
	IsAvailable() bool

	// Name returns the provider identifier
	Name() string
}

// Embedder defines the interface for embedding text into vectors.
// This is separate from Provider since embedding is mechanical (fast, deterministic)
// while generation is agentic (slow, variable).
type Embedder interface {
	// Embed converts text to a vector embedding
	Embed(ctx context.Context, text string) ([]float32, error)

	// IsEmbeddingAvailable checks if embedding is ready
	IsEmbeddingAvailable() bool
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

// BuildAnalysisPrompt creates a prompt for event analysis
func BuildAnalysisPrompt(toolName, filePath, toolResult string) string {
	return `Analyze this development event and provide insights:

Tool: ` + toolName + `
File: ` + filePath + `
Result: ` + toolResult + `

Respond in JSON format:
{
  "summary": "Brief summary (1 sentence)",
  "category": "decision|pattern|insight|strategy|constraint",
  "importance": 1-10,
  "tags": ["tag1", "tag2"],
  "reasoning": "Why this is important for future coding sessions"
}

JSON:`
}

// ParseAnalysisWithFallback parses analysis response, returning a fallback on failure.
//
// Post-parse, the analysis Summary and Reasoning are checked for prompt-
// injection signatures via IsLikelyPromptInjection. If either fires, the
// analysis is neutered (Importance → 1, "flagged:prompt-injection" tag
// added) rather than dropped — keeping the data lets an operator inspect
// the original later; the importance floor keeps it from influencing
// future retrieval/inject decisions.
func ParseAnalysisWithFallback(response string) *Analysis {
	analysis, err := parseAnalysisJSON(response)
	if err != nil || analysis == nil {
		return &Analysis{
			Summary:    response,
			Category:   "insight",
			Importance: 5,
			Tags:       []string{},
			Reasoning:  "Could not parse structured response",
		}
	}
	if IsLikelyPromptInjection(analysis.Summary) || IsLikelyPromptInjection(analysis.Reasoning) {
		analysis.Importance = 1
		analysis.Tags = append(analysis.Tags, "flagged:prompt-injection")
	}
	return analysis
}

// IsLikelyPromptInjection returns true when `text` looks like an indirect
// prompt-injection payload. Used to neuter insights extracted from a
// source that may have been attacker-controlled (a poisoned README, a
// crafted commit message, a session log containing pasted attacker text).
//
// The detector is intentionally narrow — false positives suppress real
// insights, which is its own damage. We catch known attack shapes and
// accept that novel phrasings will get through. Defense-in-depth: the
// retrieval pipeline frames everything as untrusted anyway (see
// formatRecallResults), so a missed flag here is not a single point of
// failure.
//
// Patterns covered:
//   - "ignore (all|prior|previous|earlier) instructions"
//   - "disregard ... instructions"
//   - "forget ... instructions"
//   - "ignore everything (above|before)"
//   - "new instructions:" (leading or after a newline)
//   - role-override: "you are now <name>", "pretend you are <name>"
//   - chat-template delimiter smuggling: </system>, <|im_start|>, <|endoftext|>
//   - "override (your) system prompt"
func IsLikelyPromptInjection(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	return promptInjectionRE.MatchString(text)
}

// promptInjectionRE compiles the union of known indirect-prompt-injection
// shapes into a single case-insensitive regex. Anchored on whole-word
// boundaries where it matters; intentionally NOT anchored to the start
// of the string because the payload often comes after a benign preamble.
var promptInjectionRE = func() *regexp.Regexp {
	patterns := []string{
		`(?i)\bignore\s+(?:all\s+)?(?:prior|previous|earlier|the\s+above)\s+instructions?\b`,
		`(?i)\bdisregard\s+(?:the\s+)?(?:prior|previous|above|earlier)?\s*instructions?\b`,
		`(?i)\bforget\s+(?:all\s+)?(?:everything|earlier|prior|previous|all)\s+(?:instructions?|messages?|context|turns)\b`,
		`(?i)\bignore\s+everything\s+(?:above|before)\b`,
		`(?i)(?:^|\n)\s*new\s+instructions?\s*:`,
		`(?i)\byou\s+are\s+now\s+\w+\b`,
		`(?i)\bpretend\s+(?:you|to\s+be)\s+(?:are\s+)?(?:a\s+different|an?)\s+\w+\s+(?:assistant|model|ai)\b`,
		`(?i)</\s*system\s*>`,
		`<\|im_start\|>`,
		`<\|endoftext\|>`,
		`(?i)\boverride\s+(?:your\s+)?system\s+prompt\b`,
	}
	return regexp.MustCompile(strings.Join(patterns, "|"))
}()

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
