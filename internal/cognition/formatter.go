package cognition

import (
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/pkg/cognition"
)

// Formatter formats results for injection into Claude Code prompts.
type Formatter struct {
	// MaxResults is the maximum number of results to include.
	MaxResults int

	// MaxContentLen is the maximum length of each result's content.
	MaxContentLen int

	// IncludeTags controls whether to include tags in output.
	IncludeTags bool

	// IncludeContradictions controls whether to surface contradictions.
	IncludeContradictions bool
}

// NewFormatter creates a Formatter with default settings.
func NewFormatter() *Formatter {
	return &Formatter{
		MaxResults:            3,
		MaxContentLen:         500,
		IncludeTags:           true,
		IncludeContradictions: true,
	}
}

// FormatForInjection formats results as markdown for Claude Code injection.
// The output is designed to be prepended to the user's prompt.
// Optionally accepts a SessionContext to include enrichments from background processing.
func (f *Formatter) FormatForInjection(results []cognition.Result, sessionCtx ...*cognition.SessionContext) string {
	if len(results) == 0 {
		return ""
	}

	var sb strings.Builder

	sb.WriteString("## Relevant Context from Cortex\n\n")

	// Limit results
	limit := f.MaxResults
	if len(results) < limit {
		limit = len(results)
	}

	// Check for contradictions first
	if f.IncludeContradictions {
		contradictions := f.extractContradictions(results[:limit])
		if len(contradictions) > 0 {
			sb.WriteString("**Note:** Some context may conflict:\n")
			for _, c := range contradictions {
				fmt.Fprintf(&sb, "- %s\n", c)
			}
			sb.WriteString("\n")
		}
	}

	// Format each result
	for i := 0; i < limit; i++ {
		r := results[i]

		// Category header
		categoryLabel := f.categoryLabel(r.Category)
		fmt.Fprintf(&sb, "### %s\n", categoryLabel)

		// Content
		content := r.Content
		if len(content) > f.MaxContentLen {
			content = content[:f.MaxContentLen] + "..."
		}
		sb.WriteString(content)
		sb.WriteString("\n")

		// Tags
		if f.IncludeTags && len(r.Tags) > 0 {
			fmt.Fprintf(&sb, "*Tags: %s*\n", strings.Join(r.Tags, ", "))
		}

		sb.WriteString("\n")
	}

	sb.WriteString("---\n\n")

	// Apply enrichments from SessionContext if provided
	if len(sessionCtx) > 0 && sessionCtx[0] != nil {
		sb.WriteString(f.formatEnrichments(sessionCtx[0]))
	}

	return sb.String()
}

// formatEnrichments adds any background-processed enrichments from SessionContext.
// This is the single point where all agentic enrichments are formatted.
func (f *Formatter) formatEnrichments(ctx *cognition.SessionContext) string {
	var sb strings.Builder

	// Nuances from Think
	if len(ctx.ExtractedNuances) > 0 {
		var allNuances []cognition.Nuance
		for _, ns := range ctx.ExtractedNuances {
			allNuances = append(allNuances, ns...)
		}

		if len(allNuances) > 0 {
			// Limit nuances
			maxNuances := 5
			if len(allNuances) > maxNuances {
				allNuances = allNuances[:maxNuances]
			}

			sb.WriteString("### Implementation Notes\n")
			sb.WriteString("*Gotchas to remember:*\n")
			for _, n := range allNuances {
				fmt.Fprintf(&sb, "- **%s** — %s\n", n.Detail, n.Why)
			}
			sb.WriteString("\n")
		}
	}

	// Future enrichments can be added here:
	// - TopicWeights summary
	// - Proactive insights from Dream
	// - etc.

	return sb.String()
}

// FormatCompact creates a more compact format for space-constrained contexts.
func (f *Formatter) FormatCompact(results []cognition.Result) string {
	if len(results) == 0 {
		return ""
	}

	var sb strings.Builder

	sb.WriteString("<cortex-context>\n")

	limit := f.MaxResults
	if len(results) < limit {
		limit = len(results)
	}

	for i := 0; i < limit; i++ {
		r := results[i]

		content := r.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}

		fmt.Fprintf(&sb, "- [%s] %s\n", r.Category, content)
	}

	sb.WriteString("</cortex-context>\n\n")

	return sb.String()
}

// categoryLabel returns a human-friendly label for a category.
func (f *Formatter) categoryLabel(category string) string {
	switch strings.ToLower(category) {
	case "decision":
		return "Decision"
	case "pattern":
		return "Pattern"
	case "constraint":
		return "Constraint"
	case "correction":
		return "Correction"
	case "insight":
		return "Insight"
	case "strategy":
		return "Strategy"
	default:
		return "Context"
	}
}

// extractContradictions finds and formats contradiction warnings from results.
func (f *Formatter) extractContradictions(results []cognition.Result) []string {
	var contradictions []string
	seen := make(map[string]bool)

	for _, r := range results {
		if r.Metadata == nil {
			continue
		}

		if reason, ok := r.Metadata["contradiction"].(string); ok {
			if !seen[reason] {
				contradictions = append(contradictions, reason)
				seen[reason] = true
			}
		}
	}

	return contradictions
}

// EstimateTokens provides a rough token count estimate for the formatted output.
// Uses ~4 chars per token as a rough approximation.
func (f *Formatter) EstimateTokens(results []cognition.Result) int {
	formatted := f.FormatForInjection(results)
	return len(formatted) / 4
}
