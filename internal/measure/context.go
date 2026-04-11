package measure

import (
	"strings"
)

// ContextScore evaluates a piece of context for injection worthiness.
type ContextScore struct {
	Clarity    float64 `json:"clarity"`     // 0-1: is the content unambiguous and specific?
	TokenCost  int     `json:"token_cost"`  // estimated token count
	Redundancy float64 `json:"redundancy"`  // 0-1: 0=novel project info, 1=generic knowledge
	Worth      float64 `json:"worth"`       // 0-1: composite injection value
}

// Generic phrases an LLM likely already knows — low value for injection.
var genericPhrases = []string{
	"best practice", "best practices",
	"follow conventions", "use conventions",
	"handle errors", "error handling",
	"write tests", "add tests",
	"keep it simple", "clean code",
	"use dependency injection",
	"follow solid principles",
	"use version control",
	"document your code",
	"follow the style guide",
	"use meaningful names",
	"avoid magic numbers",
}

// ScoreForInjection evaluates whether context content is worth injecting.
// Uses mechanical signals only (no LLM), designed to be fast (<1ms).
func ScoreForInjection(content string) *ContextScore {
	if len(content) == 0 {
		return &ContextScore{}
	}

	clarity := scoreContextClarity(content)
	tokenCost := EstimateTokens(content)
	redundancy := scoreRedundancy(content)
	worth := calculateWorth(clarity, redundancy, tokenCost)

	return &ContextScore{
		Clarity:    clarity,
		TokenCost:  tokenCost,
		Redundancy: redundancy,
		Worth:      worth,
	}
}

// scoreContextClarity evaluates clarity of context content.
// Reuses mechanical signals: specificity (file paths, function names, inline code)
// and checks for concrete values vs vague language.
func scoreContextClarity(content string) float64 {
	specificity := ScoreSpecificity(content)

	// Bonus for concrete values: numbers, quoted strings, specific identifiers
	concreteSignals := 0.0
	checks := 0.0

	// Has specific numbers (port numbers, limits, timeouts, versions)
	checks++
	for _, r := range content {
		if r >= '0' && r <= '9' {
			concreteSignals++
			break
		}
	}

	// Has quoted or backtick-wrapped identifiers
	checks++
	if strings.Contains(content, "`") || strings.Contains(content, "\"") {
		concreteSignals++
	}

	// Has action-oriented language (decisions, constraints)
	checks++
	lower := strings.ToLower(content)
	actionWords := []string{"use ", "avoid ", "prefer ", "chose ", "decided ", "must ", "never ", "always "}
	for _, w := range actionWords {
		if strings.Contains(lower, w) {
			concreteSignals++
			break
		}
	}

	concreteness := 0.0
	if checks > 0 {
		concreteness = concreteSignals / checks
	}

	// Blend specificity and concreteness
	return clamp(specificity*0.6+concreteness*0.4, 0, 1)
}

// scoreRedundancy estimates how likely this content is generic knowledge
// that an LLM already knows (high = generic, low = novel project-specific).
func scoreRedundancy(content string) float64 {
	lower := strings.ToLower(content)

	// Count generic phrase matches
	genericCount := 0
	for _, phrase := range genericPhrases {
		if strings.Contains(lower, phrase) {
			genericCount++
		}
	}

	// Project-specific signals that reduce redundancy
	specificSignals := 0
	// File paths
	if CountFileReferences(content) > 0 {
		specificSignals++
	}
	// Function names
	if funcNameRe.MatchString(content) {
		specificSignals++
	}
	// Inline code
	if inlineCodeRe.MatchString(content) {
		specificSignals++
	}
	// Specific proper nouns (capitalized words that aren't sentence starters)
	words := strings.Fields(content)
	for i, w := range words {
		if i > 0 && len(w) > 2 && w[0] >= 'A' && w[0] <= 'Z' && !isCommonWord(w) {
			specificSignals++
			break
		}
	}

	// More generic phrases → higher redundancy
	// More specific signals → lower redundancy
	genericScore := float64(genericCount) / 3.0 // 3+ generic phrases = max
	if genericScore > 1.0 {
		genericScore = 1.0
	}

	specificScore := float64(specificSignals) / 3.0 // 3+ specific signals = max reduction
	if specificScore > 1.0 {
		specificScore = 1.0
	}

	return clamp(genericScore-specificScore*0.5, 0, 1)
}

// calculateWorth computes the composite injection value.
// High clarity + low redundancy + reasonable token cost = high worth.
func calculateWorth(clarity, redundancy float64, tokenCost int) float64 {
	// Normalize token cost: 0-50 tokens = cheap (1.0), 500+ = expensive (0.5)
	tokenFactor := 1.0
	if tokenCost > 50 {
		tokenFactor = 1.0 - float64(tokenCost-50)/1000.0
		if tokenFactor < 0.5 {
			tokenFactor = 0.5
		}
	}

	worth := clarity * (1.0 - redundancy) * tokenFactor
	return clamp(worth, 0, 1)
}

// isCommonWord checks if a capitalized word is just a common English word.
func isCommonWord(w string) bool {
	common := map[string]bool{
		"The": true, "This": true, "That": true, "These": true,
		"When": true, "Where": true, "What": true, "Which": true,
		"How": true, "Why": true, "Who": true,
		"Use": true, "Add": true, "Fix": true, "Get": true, "Set": true,
		"For": true, "With": true, "From": true, "Into": true,
		"All": true, "Any": true, "Each": true, "Every": true,
		"Not": true, "But": true, "And": true, "Also": true,
		"New": true, "Old": true,
	}
	return common[w]
}
