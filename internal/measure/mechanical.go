package measure

import (
	"regexp"
	"strings"
	"unicode"
)

// Action verbs that indicate work scope.
var actionVerbs = []string{
	"create", "add", "implement", "build", "write",
	"update", "modify", "change", "edit", "adjust",
	"delete", "remove", "drop", "clean",
	"refactor", "extract", "rename", "move", "restructure",
	"fix", "repair", "resolve", "patch",
	"test", "verify", "validate",
	"migrate", "convert", "replace", "swap",
	"integrate", "connect", "wire",
}

// Conditional words that indicate branching complexity.
var conditionalWords = []string{
	"if", "when", "unless", "depending", "either",
	"otherwise", "alternatively", "in case", "whether",
	"optionally", "conditionally",
}

// Constraint words that indicate explicit bounds.
var constraintWords = []string{
	"must", "must not", "should not", "shall",
	"always", "never", "required", "forbidden",
	"ensure", "guarantee", "mandatory",
}

// Vague references that lower specificity.
var vaguePatterns = []string{
	"the code", "the file", "the function", "the class",
	"somewhere", "somehow", "something", "whatever",
	"the thing", "the stuff", "it", "that part",
	"the bug", "the issue", "the problem",
	"etc", "and so on", "and stuff",
}

// Concern separators that indicate multiple distinct tasks.
var concernSeparators = []string{
	" and ", " also ", " additionally ", " plus ",
	" as well as ", " along with ", " then ",
	" furthermore ", " moreover ",
}

// File path patterns.
var (
	filePathRe     = regexp.MustCompile(`(?:^|[\s"'(])([a-zA-Z0-9_./-]+\.[a-zA-Z]{1,10})(?:[\s"'),:;]|$)`)
	slashPathRe    = regexp.MustCompile(`(?:^|[\s"'(])([a-zA-Z0-9_.-]+(?:/[a-zA-Z0-9_.-]+)+)(?:[\s"'),:;]|$)`)
	funcNameRe     = regexp.MustCompile(`[a-zA-Z_]\w*\(`)
	lineNumberRe   = regexp.MustCompile(`(?:line|L)\s*\d+`)
	codeBlockRe    = regexp.MustCompile("(?s)```.*?```")
	inlineCodeRe   = regexp.MustCompile("`[^`]+`")
	numberedListRe = regexp.MustCompile(`(?m)^\s*\d+[\.\)]\s`)
	bulletListRe   = regexp.MustCompile(`(?m)^\s*[-*]\s`)
)

// EstimateTokens provides a rough token count for the input text.
func EstimateTokens(text string) int {
	return len(text) / 4
}

// EstimateOutputTokens estimates expected output token count from a prompt using defaults.
func EstimateOutputTokens(prompt string) int {
	verbs := CountActionVerbs(prompt)
	files := CountFileReferences(prompt)
	concerns := CountConcerns(prompt)
	return estimateOutputTokens(verbs, files, concerns, HasExamples(prompt), DefaultConfig().TokenEstimation)
}

// estimateOutputTokens uses config-driven multipliers.
func estimateOutputTokens(verbs, files, concerns int, hasExamples bool, p *TokenEstimationParams) int {
	estimate := p.Base + verbs*p.VerbMultiplier + files*p.FileMultiplier + concerns*p.ConcernMultiplier
	if hasExamples {
		estimate += p.ExampleBonus
	}
	return estimate
}

// CountFileReferences counts file paths and file name patterns in the prompt.
func CountFileReferences(prompt string) int {
	seen := make(map[string]bool)

	for _, match := range filePathRe.FindAllStringSubmatch(prompt, -1) {
		path := match[1]
		// Filter out common non-file patterns
		if isLikelyFilePath(path) {
			seen[path] = true
		}
	}

	for _, match := range slashPathRe.FindAllStringSubmatch(prompt, -1) {
		path := match[1]
		seen[path] = true
	}

	return len(seen)
}

// isLikelyFilePath filters false positives from file path detection.
func isLikelyFilePath(s string) bool {
	// Must have a recognized extension
	knownExts := []string{
		".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java",
		".rb", ".c", ".cpp", ".h", ".hpp", ".cs", ".swift", ".kt",
		".yaml", ".yml", ".json", ".toml", ".xml", ".html", ".css",
		".sql", ".sh", ".bash", ".zsh", ".md", ".txt", ".cfg", ".conf",
		".proto", ".graphql", ".tf", ".dockerfile",
	}

	lower := strings.ToLower(s)
	for _, ext := range knownExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// CountActionVerbs counts action verbs using default word list.
func CountActionVerbs(prompt string) int {
	return countWords(prompt, actionVerbs)
}

// countWords counts matches from a word list, with stemming support.
func countWords(prompt string, wordList []string) int {
	lower := strings.ToLower(prompt)
	words := tokenizeWords(lower)

	// Build set of stemmed words
	wordSet := make(map[string]bool, len(words))
	for _, w := range words {
		wordSet[w] = true
		if stem := stemWord(w); stem != w {
			wordSet[stem] = true
		}
	}

	count := 0
	for _, verb := range wordList {
		if strings.Contains(verb, " ") {
			if strings.Contains(lower, verb) {
				count++
			}
		} else {
			if wordSet[verb] {
				count++
			}
		}
	}
	return count
}

// stemWord performs basic suffix removal for verb matching.
func stemWord(w string) string {
	// Handle -ing (e.g., "creating" -> "create", "testing" -> "test")
	if strings.HasSuffix(w, "ting") && len(w) > 5 {
		return w[:len(w)-4] + "te" // creating -> create
	}
	if strings.HasSuffix(w, "ing") && len(w) > 4 {
		return w[:len(w)-3] // testing -> test
	}
	// Handle -ed (e.g., "updated" -> "update", "fixed" -> "fix")
	if strings.HasSuffix(w, "ted") && len(w) > 4 {
		return w[:len(w)-1] // updated -> update (remove trailing d -> "update")
	}
	if strings.HasSuffix(w, "ed") && len(w) > 3 {
		return w[:len(w)-2] // fixed -> fix
	}
	// Handle -es, -s (e.g., "fixes" -> "fix", "creates" -> "create")
	if strings.HasSuffix(w, "es") && len(w) > 3 {
		return w[:len(w)-2]
	}
	if strings.HasSuffix(w, "s") && len(w) > 3 {
		return w[:len(w)-1]
	}
	return w
}

// CountConditionals counts conditional/branching language using default word list.
func CountConditionals(prompt string) int {
	return countWords(prompt, conditionalWords)
}

// CountConcerns estimates distinct concerns using default separators.
func CountConcerns(prompt string) int {
	return countConcerns(prompt, concernSeparators)
}

// countConcerns estimates distinct concerns using provided separators.
func countConcerns(prompt string, seps []string) int {
	lower := strings.ToLower(prompt)

	concerns := 1

	for _, sep := range seps {
		concerns += strings.Count(lower, sep)
	}

	numberedItems := numberedListRe.FindAllString(prompt, -1)
	if len(numberedItems) > 1 {
		concerns += len(numberedItems) - 1
	}

	bulletItems := bulletListRe.FindAllString(prompt, -1)
	if len(bulletItems) > 1 {
		concerns += len(bulletItems) - 1
	}

	return concerns
}

// ScoreSpecificity measures how specific references are (0-1) using default vague patterns.
func ScoreSpecificity(prompt string) float64 {
	return scoreSpecificity(prompt, vaguePatterns)
}

// scoreSpecificity measures specificity using provided vague patterns.
func scoreSpecificity(prompt string, vagues []string) float64 {
	if len(prompt) == 0 {
		return 0
	}

	score := 0.0
	checks := 0.0

	fileRefs := CountFileReferences(prompt)
	checks++
	if fileRefs > 0 {
		score += 1.0
	}

	funcNames := funcNameRe.FindAllString(prompt, -1)
	checks++
	if len(funcNames) > 0 {
		score += 1.0
	}

	lineNums := lineNumberRe.FindAllString(prompt, -1)
	checks++
	if len(lineNums) > 0 {
		score += 1.0
	}

	inlineCode := inlineCodeRe.FindAllString(prompt, -1)
	checks++
	if len(inlineCode) > 0 {
		score += 1.0
	}

	lower := strings.ToLower(prompt)
	vagueCount := 0
	for _, vague := range vagues {
		if strings.Contains(lower, vague) {
			vagueCount++
		}
	}
	checks++
	if vagueCount == 0 {
		score += 1.0
	} else if vagueCount == 1 {
		score += 0.5
	}

	if checks == 0 {
		return 0
	}
	return clamp(score/checks, 0, 1)
}

// CountConstraints counts explicit constraint language using default word list.
func CountConstraints(prompt string) int {
	return countConstraints(prompt, constraintWords)
}

// countConstraints counts constraint language using provided word list.
// Multi-word patterns (e.g., "must not") take priority over single-word patterns.
func countConstraints(prompt string, words []string) int {
	lower := strings.ToLower(prompt)
	count := 0

	consumed := lower
	for _, c := range words {
		if !strings.Contains(c, " ") {
			continue
		}
		n := strings.Count(consumed, c)
		count += n
		if n > 0 {
			consumed = strings.ReplaceAll(consumed, c, strings.Repeat("_", len(c)))
		}
	}

	tokenized := tokenizeWords(consumed)
	for _, c := range words {
		if strings.Contains(c, " ") {
			continue
		}
		for _, w := range tokenized {
			if w == c {
				count++
			}
		}
	}

	return count
}

// HasExamples detects whether the prompt contains code examples or expected output.
func HasExamples(prompt string) bool {
	// Check for code blocks
	if codeBlockRe.MatchString(prompt) {
		return true
	}

	// Check for inline code with meaningful content (more than just a name)
	inlineMatches := inlineCodeRe.FindAllString(prompt, -1)
	for _, m := range inlineMatches {
		inner := m[1 : len(m)-1]
		// If inline code contains operators, braces, or multi-word, it's an example
		if strings.ContainsAny(inner, "{}()=;><+") || strings.Contains(inner, " ") {
			return true
		}
	}

	// Check for output markers
	lines := strings.Split(prompt, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "> ") || strings.HasPrefix(trimmed, "Output:") || strings.HasPrefix(trimmed, "Expected:") {
			return true
		}
	}

	return false
}

// CountQuestions counts embedded questions in the prompt.
func CountQuestions(prompt string) int {
	count := 0
	// Count sentences ending with ?
	for _, r := range prompt {
		if r == '?' {
			count++
		}
	}
	return count
}

// ScopeScore computes a 0-1 scope score using default parameters.
func ScopeScore(actionVerbs, fileRefs, conditionals, concerns int) float64 {
	return scopeScore(actionVerbs, fileRefs, conditionals, concerns, DefaultConfig().Scope)
}

// scopeScore computes scope using config-driven parameters.
func scopeScore(actionVerbs, fileRefs, conditionals, concerns int, p *ScopeParams) float64 {
	raw := float64(actionVerbs)*p.VerbWeight +
		float64(fileRefs)*p.FileRefWeight +
		float64(conditionals)*p.ConditionalWeight +
		float64(concerns)*p.ConcernWeight
	return clamp(raw/p.Denominator, 0, 1)
}

// ClarityScore computes a 0-1 clarity score using default parameters.
func ClarityScore(specificity float64, constraints int, hasExamples bool, questions int) float64 {
	return clarityScore(specificity, constraints, hasExamples, questions, DefaultConfig().Clarity)
}

// clarityScore computes clarity using config-driven parameters.
func clarityScore(specificity float64, constraints int, hasExamples bool, questions int, p *ClarityParams) float64 {
	score := specificity * p.SpecificityWeight

	constraintScore := float64(constraints) / float64(p.ConstraintCap)
	if constraintScore > 1.0 {
		constraintScore = 1.0
	}
	score += constraintScore * p.ConstraintWeight

	if hasExamples {
		score += p.ExampleWeight
	}

	questionPenalty := float64(questions) / float64(p.QuestionCap)
	if questionPenalty > 1.0 {
		questionPenalty = 1.0
	}
	score += (1.0 - questionPenalty) * p.QuestionWeight

	return clamp(score, 0, 1)
}

// DecompositionScore computes a 0-1 score for how well-decomposed the prompt is.
// Higher means better decomposed (single concern, small scope, high clarity).
func DecompositionScore(concerns int, scopeScore, clarityScore float64) float64 {
	// Single concern bonus: 1.0 for 1 concern, decays as concerns increase
	var singleConcernBonus float64
	if concerns <= 1 {
		singleConcernBonus = 1.0
	} else {
		singleConcernBonus = 1.0 / float64(concerns)
	}

	// Scope penalty: high scope reduces decomposition score
	scopeFactor := 1.0 - scopeScore*0.5

	// Clarity boost: clear prompts are better decomposed
	clarityBoost := 0.7 + clarityScore*0.3

	return clamp(singleConcernBonus*scopeFactor*clarityBoost, 0, 1)
}

// tokenizeWords splits text into lowercase words.
func tokenizeWords(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
