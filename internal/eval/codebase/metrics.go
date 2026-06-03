package codebase

import (
	"regexp"
	"sort"
	"strings"
)

// TraceRow is the minimal subset of internal/eval/dagtrace.Row this
// package needs. Mirroring the JSON tags here lets the extractor consume
// dag_traces.jsonl directly without importing the writer package and
// keeps the metric layer testable with synthetic inputs.
type TraceRow struct {
	TurnID        string         `json:"turn_id"`
	NodeID        string         `json:"node_id"`
	ParentID      string         `json:"parent_node_id"`
	QualifiedName string         `json:"qualified_name"`
	OK            bool           `json:"ok"`
	Out           map[string]any `json:"out"`
}

// Metrics is the mechanical-extraction result for one cell. Every field
// is a fact derived from the answer text + trace rows; nothing here
// requires an LLM. Slice 3 adds judge_pass alongside this.
type Metrics struct {
	HopCount   int `json:"hop_count"`   // decide.next invocations
	ReadCount  int `json:"read_count"`  // act.read_file invocations
	ShellCount int `json:"shell_count"` // act.run_shell invocations
	NeedMore   int `json:"need_more"`   // synth responses ending in NEED_MORE:

	// CitationCount is the count of file/line references in the answer.
	// CitationRate is CitationCount / max(1, ClaimCount).
	ClaimCount    int     `json:"claim_count"`
	CitationCount int     `json:"citation_count"`
	CitationRate  float64 `json:"citation_rate"`

	HedgeCount          int `json:"hedge_count"`
	UnverifiedTailCount int `json:"unverified_tail_count"`

	MustCitePathsSatisfied bool     `json:"must_cite_paths_satisfied"`
	MustNotInventClean     bool     `json:"must_not_invent_clean"`
	MustNotInventHits      []string `json:"must_not_invent_hits,omitempty"`

	// BudgetTokens is the value from the first sense.estimate_scope row
	// for this turn (Out["budget_tokens"]). 0 when the node didn't run
	// (e.g. classifier short-circuited).
	BudgetTokens int `json:"budget_tokens"`

	BudgetTokenInRange bool `json:"budget_token_in_range"`
}

// Bound captures one pass/fail check the runner emits per fixture.
// Keeps the messaging close to the metric so the dashboard (slice 4)
// can show "hop_count=4, expected 2..5 → pass" without re-deriving the
// bounds.
type Bound struct {
	Name string `json:"name"`
	Pass bool   `json:"pass"`
	Want string `json:"want"`
	Got  string `json:"got"`
}

// CitationRegex matches "file/path.go" and "file/path.go:123" — the
// shapes synth answers actually use. The "." + extension is required
// (avoids matching plain prose) and the extension is alphanumeric so
// "v3.0" / "n.b." don't trip the counter. Subdirectories with hyphens
// or dots in the leaf are common (eval-suite-codebase-reading.md), so
// we allow them.
var CitationRegex = regexp.MustCompile(`[\w./-]+\.[A-Za-z0-9]+(?::\d+(?:-\d+)?)?`)

// HedgeRegex matches the hedge phrases the suite asks the answer to
// avoid. Word-boundary anchors prevent "may be" from matching inside
// "maybeline".
var HedgeRegex = regexp.MustCompile(`(?i)\b(not directly confirmed|appears to(?: be)?|may be\b|not seen in|cannot verify|seems to be|likely|possibly)\b`)

// unverifiedHeadingRegex matches "Unverified" / "Unverified Claims" as
// a standalone heading line (markdown "## Unverified", bare
// "Unverified:", emphasized "**Unverified Claims**", etc.).
var unverifiedHeadingRegex = regexp.MustCompile(`(?im)^\s*(?:#+\s*|\*\*)?unverified(?:\s+claims)?(?:\*\*)?\s*:?\s*$`)

// stripPromptEcho returns the answer with the fixture's prompt removed.
// Used by must_not_invent so that identifier-shaped sentinels in the
// fixture prompt itself don't false-positive when the model quotes the
// question back. Best-effort: removes both the exact prompt text and
// its trimmed-line shape.
func stripPromptEcho(answer, prompt string) string {
	if prompt == "" {
		return answer
	}
	out := answer
	out = strings.ReplaceAll(out, prompt, "")
	for _, ln := range strings.Split(prompt, "\n") {
		ln = strings.TrimSpace(ln)
		if len(ln) < 8 {
			continue // skip noise-prone short lines
		}
		out = strings.ReplaceAll(out, ln, "")
	}
	return out
}

// Extract computes the mechanical metric set from an answer text + the
// trace rows for that turn. trace should already be filtered to the
// fixture's turn_id; the runner is responsible for that filter.
func Extract(answer string, trace []TraceRow, fx *Fixture) Metrics {
	m := Metrics{}
	for _, r := range trace {
		switch r.QualifiedName {
		case "decide.next":
			m.HopCount++
		case "act.read_file":
			m.ReadCount++
		case "act.run_shell":
			m.ShellCount++
		case "decide.coding_turn":
			if r.Out != nil {
				if resp, _ := r.Out["response"].(string); strings.Contains(resp, "NEED_MORE:") {
					m.NeedMore++
				}
			}
		case "sense.estimate_scope":
			// Take the last NONZERO budget across this turn's
			// estimate_scope rows. Skipping zeros means a fallback row
			// (budget_tokens:0) can't shadow the real estimate, and
			// "last wins" picks the converged value when --auto-retry
			// re-ran the scope estimate.
			if r.Out != nil {
				if v := coerceInt(r.Out["budget_tokens"]); v > 0 {
					m.BudgetTokens = v
				}
			}
		}
	}

	m.ClaimCount = countClaims(answer)
	m.CitationCount = countCitations(answer)
	if m.ClaimCount > 0 {
		m.CitationRate = float64(m.CitationCount) / float64(m.ClaimCount)
	}
	m.HedgeCount = len(HedgeRegex.FindAllStringIndex(answer, -1))
	m.UnverifiedTailCount = countUnverifiedTail(answer)

	if fx != nil {
		m.MustCitePathsSatisfied = checkMustCite(answer, fx.Expected.MustCitePaths)
		// Run invention detection against the answer with the prompt
		// echo stripped. Otherwise sentinels that legitimately appear in
		// the user's question (the model echoes it back) read as
		// hallucinated inventions when they aren't.
		m.MustNotInventHits = findInventions(stripPromptEcho(answer, fx.Prompt), fx.Expected.MustNotInvent)
		m.MustNotInventClean = len(m.MustNotInventHits) == 0
		m.BudgetTokenInRange = inBudgetRange(m.BudgetTokens, fx.Expected.BudgetTokenMin, fx.Expected.BudgetTokenMax)
	}
	return m
}

// Evaluate scores the fixture's bounds against extracted metrics. The
// runner uses the returned Bound list both as the pass/fail criterion
// (all-pass = task_success=true) and as the human-readable explanation
// in --baseline reports.
func Evaluate(m Metrics, exp Expectation) []Bound {
	var bounds []Bound

	if exp.HopCountMin > 0 {
		bounds = append(bounds, Bound{
			Name: "hop_count_min",
			Pass: m.HopCount >= exp.HopCountMin,
			Want: ">= " + itoa(exp.HopCountMin),
			Got:  itoa(m.HopCount),
		})
	}
	if exp.HopCountMax > 0 {
		bounds = append(bounds, Bound{
			Name: "hop_count_max",
			Pass: m.HopCount <= exp.HopCountMax,
			Want: "<= " + itoa(exp.HopCountMax),
			Got:  itoa(m.HopCount),
		})
	}
	if exp.ReadCountMin > 0 {
		bounds = append(bounds, Bound{
			Name: "read_count_min",
			Pass: m.ReadCount >= exp.ReadCountMin,
			Want: ">= " + itoa(exp.ReadCountMin),
			Got:  itoa(m.ReadCount),
		})
	}
	if exp.ReadCountMax > 0 {
		bounds = append(bounds, Bound{
			Name: "read_count_max",
			Pass: m.ReadCount <= exp.ReadCountMax,
			Want: "<= " + itoa(exp.ReadCountMax),
			Got:  itoa(m.ReadCount),
		})
	}
	// inspect_count = read + shell. Lets a fixture say "I want the model
	// to inspect the file once" without dictating read_file vs cat.
	inspectCount := m.ReadCount + m.ShellCount
	if exp.InspectCountMin > 0 {
		bounds = append(bounds, Bound{
			Name: "inspect_count_min",
			Pass: inspectCount >= exp.InspectCountMin,
			Want: ">= " + itoa(exp.InspectCountMin),
			Got:  itoa(inspectCount),
		})
	}
	if exp.InspectCountMax > 0 {
		bounds = append(bounds, Bound{
			Name: "inspect_count_max",
			Pass: inspectCount <= exp.InspectCountMax,
			Want: "<= " + itoa(exp.InspectCountMax),
			Got:  itoa(inspectCount),
		})
	}
	if exp.CitationRateMin > 0 {
		bounds = append(bounds, Bound{
			Name: "citation_rate_min",
			Pass: m.CitationRate >= exp.CitationRateMin,
			Want: ">= " + ftoa(exp.CitationRateMin),
			Got:  ftoa(m.CitationRate),
		})
	}
	if exp.HedgeCountMax != 0 {
		max := exp.HedgeCountMax
		if max < 0 {
			max = 0 // -1 sentinel = explicit zero hedges
		}
		bounds = append(bounds, Bound{
			Name: "hedge_count_max",
			Pass: m.HedgeCount <= max,
			Want: "<= " + itoa(max),
			Got:  itoa(m.HedgeCount),
		})
	}
	if exp.UnverifiedTailCountMax != 0 {
		max := exp.UnverifiedTailCountMax
		if max < 0 {
			max = 0
		}
		bounds = append(bounds, Bound{
			Name: "unverified_tail_count_max",
			Pass: m.UnverifiedTailCount <= max,
			Want: "<= " + itoa(max),
			Got:  itoa(m.UnverifiedTailCount),
		})
	}
	if len(exp.MustCitePaths) > 0 {
		bounds = append(bounds, Bound{
			Name: "must_cite_paths",
			Pass: m.MustCitePathsSatisfied,
			Want: "any of " + strings.Join(exp.MustCitePaths, ", "),
			Got:  yn(m.MustCitePathsSatisfied),
		})
	}
	if len(exp.MustNotInvent) > 0 {
		bounds = append(bounds, Bound{
			Name: "must_not_invent",
			Pass: m.MustNotInventClean,
			Want: "no hits in " + strings.Join(exp.MustNotInvent, ", "),
			Got:  strings.Join(m.MustNotInventHits, ", "),
		})
	}
	if exp.BudgetTokenMin > 0 || exp.BudgetTokenMax > 0 {
		bounds = append(bounds, Bound{
			Name: "budget_tokens",
			Pass: m.BudgetTokenInRange,
			Want: itoa(exp.BudgetTokenMin) + ".." + itoa(exp.BudgetTokenMax),
			Got:  itoa(m.BudgetTokens),
		})
	}
	return bounds
}

// AllPass reports whether every bound in bs passed. Empty bs returns
// true so a fixture with no expectations defined doesn't fail by
// vacuous truth — callers should set expectations on every shipped
// fixture (validate catches missing IDs but not missing bounds today).
func AllPass(bs []Bound) bool {
	for _, b := range bs {
		if !b.Pass {
			return false
		}
	}
	return true
}

// FilterByTurn returns rows whose TurnID matches turnID. Convenience
// wrapper for callers that load the full dag_traces.jsonl.
func FilterByTurn(rows []TraceRow, turnID string) []TraceRow {
	var out []TraceRow
	for _, r := range rows {
		if r.TurnID == turnID {
			out = append(out, r)
		}
	}
	return out
}

// SortedHopReadShell returns the trace counts as a stable three-tuple
// for compact reporting. Useful in slice-4 diffs.
func SortedHopReadShell(m Metrics) []int {
	return []int{m.HopCount, m.ReadCount, m.ShellCount}
}

// --- internal helpers ---

func countClaims(answer string) int {
	if strings.TrimSpace(answer) == "" {
		return 0
	}
	// A "claim" is either a list-item line (- foo, * foo, 1. foo) or a
	// non-empty paragraph (blank-line separated block of prose). This
	// is intentionally coarse — we match the doc's definition without
	// trying to NL-parse the answer.
	var claims int
	paragraphs := splitParagraphs(answer)
	for _, p := range paragraphs {
		lines := strings.Split(p, "\n")
		bullets := 0
		for _, ln := range lines {
			tr := strings.TrimSpace(ln)
			if tr == "" {
				continue
			}
			if isListItem(tr) {
				bullets++
			}
		}
		if bullets > 0 {
			claims += bullets
			continue
		}
		// Heading-only paragraphs ("## Summary") aren't claims.
		nonEmpty := 0
		for _, ln := range lines {
			tr := strings.TrimSpace(ln)
			if tr == "" || strings.HasPrefix(tr, "#") {
				continue
			}
			nonEmpty++
		}
		if nonEmpty > 0 {
			claims++
		}
	}
	return claims
}

// lineAnchorRegex matches just the file:line[-line] shape — used to
// weight pinpoint citations higher than bare paths in countCitations.
var lineAnchorRegex = regexp.MustCompile(`[\w./-]+\.[A-Za-z0-9]+:\d+(?:-\d+)?`)

// lineAnchorWeight is how many "credit units" one file:line citation
// counts for vs a bare path. The doc names "file:line" as the canonical
// pinpoint shape; weighting it heavier nudges the rate downward when
// answers cite vague paths repeatedly.
const lineAnchorWeight = 2

func countCitations(answer string) int {
	matches := CitationRegex.FindAllString(answer, -1)
	// Dedup by exact match so a paragraph repeating "repl.go" three
	// times still counts as one citation. The spec says "matches
	// divided by claim count"; dedup keeps the rate honest when the
	// model is repetitive.
	seen := map[string]struct{}{}
	for _, m := range matches {
		seen[m] = struct{}{}
	}
	// Weight file:line anchors higher than bare-path citations. A
	// pinpoint answer that cites foo.go:42 gets more credit than one
	// that mentions foo.go three times in paragraphs without line
	// numbers.
	anchored := map[string]struct{}{}
	for _, m := range lineAnchorRegex.FindAllString(answer, -1) {
		anchored[m] = struct{}{}
	}
	total := len(seen) + len(anchored)*(lineAnchorWeight-1)
	return total
}

func countUnverifiedTail(answer string) int {
	loc := unverifiedHeadingRegex.FindStringIndex(answer)
	if loc == nil {
		return 0
	}
	tail := answer[loc[1]:]
	// Count list items until we hit another heading or the doc ends.
	var n int
	for _, ln := range strings.Split(tail, "\n") {
		tr := strings.TrimSpace(ln)
		if tr == "" {
			continue
		}
		if strings.HasPrefix(tr, "#") || (strings.HasPrefix(tr, "**") && strings.HasSuffix(tr, "**") && !isListItem(tr)) {
			break
		}
		if isListItem(tr) {
			n++
		}
	}
	return n
}

func checkMustCite(answer string, paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, p := range paths {
		if strings.Contains(answer, p) {
			return true
		}
	}
	return false
}

// identifierShape recognizes strings that are pure source-identifiers
// (e.g. MAX_TODOS, NewCodingTurnHandler). For those, partial-substring
// match would false-positive on the real symbol — MAX_TODO inside
// MAX_TODOS. Word-boundary regex avoids that. Paths/non-identifier
// strings stay on the cheaper Contains check.
var identifierShape = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func findInventions(answer string, forbidden []string) []string {
	var hits []string
	for _, s := range forbidden {
		if s == "" {
			continue
		}
		if identifierShape.MatchString(s) {
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(s) + `\b`)
			if re.MatchString(answer) {
				hits = append(hits, s)
			}
			continue
		}
		if strings.Contains(answer, s) {
			hits = append(hits, s)
		}
	}
	sort.Strings(hits)
	return hits
}

func inBudgetRange(got, lo, hi int) bool {
	if lo == 0 && hi == 0 {
		return true
	}
	if lo > 0 && got < lo {
		return false
	}
	if hi > 0 && got > hi {
		return false
	}
	return true
}

func splitParagraphs(s string) []string {
	// "\n\n" boundary is the typical paragraph split for Markdown
	// answers. Trailing whitespace is normalized so a stray "\n \n"
	// still splits.
	normalized := strings.ReplaceAll(s, "\r\n", "\n")
	normalized = regexp.MustCompile(`\n[ \t]+\n`).ReplaceAllString(normalized, "\n\n")
	parts := strings.Split(normalized, "\n\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

func isListItem(line string) bool {
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ") {
		return true
	}
	// Numbered list: "1." through "999." prefix.
	for i := 0; i < len(line) && i < 4; i++ {
		c := line[i]
		if c == '.' && i > 0 {
			rest := line[i+1:]
			return strings.HasPrefix(rest, " ")
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return false
}

func coerceInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case float32:
		return int(x)
	}
	return 0
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func ftoa(f float64) string {
	// Three-decimal fixed format keeps the dashboard's "rate=0.823" shape stable.
	whole := int(f * 1000)
	if whole < 0 {
		return "-" + ftoa(-f)
	}
	intPart := whole / 1000
	fracPart := whole % 1000
	frac := itoa(fracPart)
	for len(frac) < 3 {
		frac = "0" + frac
	}
	return itoa(intPart) + "." + frac
}

func yn(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
