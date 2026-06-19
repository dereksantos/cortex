package study

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dereksantos/cortex/pkg/llm"
)

// Phase-2 inference contract. The library only ever sees an InferFunc,
// so inference is fully mockable; the real LLM-backed implementation is
// built in the adapter layer using BuildInferPrompt + ParseInferResponse.
//
// The provenance contract is the whole point of this layer: sample-then-
// infer is exactly where models invent line numbers, and the eval suite
// grades on real file:line citations. So every claim must attribute to a
// sampled region's real range, and ValidateCitations strips anything
// that can't — turning "I didn't sample that" into a safe lead instead
// of a hallucinated citation.

// InferInput is what phase-2 inference sees: the sampled regions,
// labelled with their real line ranges, plus optional focus/goal.
type InferInput struct {
	Path    string
	RelPath string
	Sampled []SampledChunk
	Focus   *Focus
	Goal    string
	// Numbered overrides per-line "N| " snippet numbering: nil → the
	// format default (numberSnippetLines), true/false → forced. The
	// override exists so evals can isolate coordinate availability from
	// fragment granularity.
	Numbered *bool
}

// InferOutput is the raw inference result, before citation validation.
type InferOutput struct {
	Digest    string
	Citations []Citation
	Leads     []Lead
}

// InferFunc runs bounded inference over the sampled regions. It returns
// a digest, candidate citations (validated by the caller against the
// sampled ranges), and leads for off-sample regions.
type InferFunc func(ctx context.Context, in InferInput) (InferOutput, error)

// inferSystemPrompt states the role + the four hard provenance
// constraints. Kept as a const so tests can assert the markers are
// present and the contract can't silently drift.
const inferSystemPrompt = `You study large files by reading only a SAMPLE of their regions. You are given a set of sampled regions, each labelled with its real path and line range. Infer a concise digest of what these regions show, grounded ONLY in what you can see.

Hard rules (provenance contract):
1. Every claim in the digest MUST be attributable to one sampled region's real line range.
2. NEVER cite a line you did not see in a sampled region below.
3. If the answer needs a region you did NOT see, emit a lead (a pointer to where to look next), not a citation.
4. Citations are validated against the sampled ranges; any citation outside them is dropped, so never guess a line number.
5. Cite the NARROWEST line range containing the evidence — do not pad a citation beyond the lines that actually support the claim.
6. For repeating data (records, log lines): cite the line number of an instance you can see. When lines carry a visible "N| " prefix, N is the line number to cite — record ids or other field values are NOT line numbers.

Respond with a single JSON object and nothing else:
{"digest":"...","citations":[{"relpath":"...","line_start":N,"line_end":M,"claim":"..."}],"leads":[{"relpath":"...","near_line":N,"why":"..."}]}`

// BuildInferPrompt renders the (system, user) prompt pair. The user
// prompt labels every sampled region with its real relpath:line-line
// header so the model can only cite ranges it was actually shown.
func BuildInferPrompt(in InferInput) (system, user string) {
	var b strings.Builder
	if in.Goal != "" {
		fmt.Fprintf(&b, "Task: %s\n\n", in.Goal)
	}
	if d := describeFocus(in.Focus); d != "" {
		fmt.Fprintf(&b, "Focus: %s\n\n", d)
	}
	display := in.RelPath
	if display == "" {
		display = in.Path
	}
	fmt.Fprintf(&b, "Sampled regions of %s (a PARTIAL view — not the whole content):\n\n", display)
	for _, s := range in.Sampled {
		// Numbering is decided per region: a corpus mixes formats, and
		// the measured rule (code/records numbered, prose bare) is a
		// property of each file, not of the study target as a whole.
		numbered := numberSnippetLines(s.RelPath)
		if in.Numbered != nil {
			numbered = *in.Numbered
		}
		// Terse "@relpath:start-end" header: ~3-4 fewer tokens per region
		// than a fenced "----- … -----" rule (it adds up once a pass carries
		// many small fragments). relpath:start-end stays CONTIGUOUS so the
		// citationRelayed substring match (digest-of-digests relay) still
		// fires on the header text.
		fmt.Fprintf(&b, "@%s:%d-%d\n", s.RelPath, s.LineStart, s.LineEnd)
		if numbered {
			writeNumberedSnippet(&b, s.Snippet, s.LineStart)
		} else {
			b.WriteString(s.Snippet)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return inferSystemPrompt, b.String()
}

// numberSnippetLines reports whether snippets for this file get explicit
// per-line numbers in the prompt. Everything except prose needs them:
//
//   - Record data (NDJSON, CSV, …): without visible numbers the model
//     locates records by intrinsic keys (an id field) and emits
//     citations that fail validation (0% → 100% grounded when added).
//   - Code: the n=10 2×2 grid (eval-journal 2026-06-10) measured
//     unit-fragment sampling at 52% grounded unnumbered vs 100%
//     numbered — coordinates dominate granularity for citation
//     accuracy.
//   - Prose keeps bare snippets: sections anchor citations well
//     (measured 100% grounded unnumbered at unit granularity), and the
//     prefix would cost ~10-15% of the sample budget.
//
// Unknown formats stay unnumbered (conservative: spend budget on
// content until a measurement says otherwise).
func numberSnippetLines(path string) bool {
	switch lang := langFor(filepath.Ext(path)); lang {
	case "md", "txt", "rst", "unknown":
		return false
	default:
		return lang != ""
	}
}

// writeNumberedSnippet emits the snippet with each line prefixed by its
// absolute file line number ("123| …"), starting at base.
func writeNumberedSnippet(b *strings.Builder, snippet string, base int) {
	for i, line := range strings.Split(strings.TrimRight(snippet, "\n"), "\n") {
		fmt.Fprintf(b, "%d| %s\n", base+i, line)
	}
}

func describeFocus(f *Focus) string {
	if f == nil {
		return ""
	}
	var d string
	switch {
	case f.Symbol != "":
		d = "symbol " + f.Symbol
	case f.Query != "":
		d = f.Query
	case f.Lines[0] > 0 || f.Lines[1] > 0:
		d = fmt.Sprintf("lines %d-%d", f.Lines[0], f.Lines[1])
	}
	if f.Path != "" {
		if d == "" {
			return "path " + f.Path
		}
		return "path " + f.Path + ", " + d
	}
	return d
}

// citationMergeGapLines is the gap tolerance when merging sampled
// ranges for validation. Edge refinement (line snapping + boundary
// snapping, see RefineChunk) trims a line or two between byte-adjacent
// chunks, so ranges that were contiguous on disk can show pinhole gaps;
// a citation spanning such a gap has still effectively been seen.
const citationMergeGapLines = 2

// ValidateCitations keeps only citations whose relpath matches sampled
// chunks AND whose [line_start,line_end] is fully contained in the
// UNION of that file's sampled ranges (merged with a small gap
// tolerance). Containment in the union — not a single chunk — matters
// at unit-granularity sampling: a legitimate claim about one section
// spans several adjacent small fragments, all of which the model saw.
// Unverifiable citations are passed to onDrop (when non-nil) and
// excluded from the result.
func ValidateCitations(cits []Citation, sampled []SampledChunk, onDrop func(Citation)) []Citation {
	merged := mergeSampledRanges(sampled)
	valid := make([]Citation, 0, len(cits))
	for _, c := range cits {
		if citationInSample(c, merged) || citationRelayed(c, sampled) {
			valid = append(valid, c)
		} else if onDrop != nil {
			onDrop(c)
		}
	}
	return valid
}

// citationRelayed reports whether the citation is a VERBATIM relay: its
// exact "path:start-end" string appears inside a sampled snippet. This
// is the hierarchy contract — when studying digests of digests, the
// lower level's citations are visible as text, and propagating one
// upward unchanged preserves the provenance chain (the cited string is
// itself the evidence). Measured necessity: free-form relaying invents
// line ranges (7/11 fabricated on the first level-1 corpus run); exact
// string match admits only the faithful copies.
func citationRelayed(c Citation, sampled []SampledChunk) bool {
	if c.RelPath == "" || c.LineStart <= 0 || c.LineEnd < c.LineStart {
		return false
	}
	needle := fmt.Sprintf("%s:%d-%d", c.RelPath, c.LineStart, c.LineEnd)
	for _, s := range sampled {
		if strings.Contains(s.Snippet, needle) {
			return true
		}
	}
	return false
}

type lineRange struct{ start, end int }

// mergeSampledRanges collapses each relpath's sampled chunks into
// sorted, gap-tolerant line intervals.
func mergeSampledRanges(sampled []SampledChunk) map[string][]lineRange {
	byPath := map[string][]lineRange{}
	for _, s := range sampled {
		byPath[s.RelPath] = append(byPath[s.RelPath], lineRange{s.LineStart, s.LineEnd})
	}
	for p, rs := range byPath {
		sort.Slice(rs, func(i, j int) bool { return rs[i].start < rs[j].start })
		out := rs[:0]
		for _, r := range rs {
			if n := len(out); n > 0 && r.start <= out[n-1].end+1+citationMergeGapLines {
				if r.end > out[n-1].end {
					out[n-1].end = r.end
				}
				continue
			}
			out = append(out, r)
		}
		byPath[p] = out
	}
	return byPath
}

func citationInSample(c Citation, merged map[string][]lineRange) bool {
	if c.LineStart <= 0 || c.LineEnd < c.LineStart {
		return false
	}
	for _, r := range merged[c.RelPath] {
		if c.LineStart >= r.start && c.LineEnd <= r.end {
			return true
		}
	}
	return false
}

// inferJSON is the wire shape the model is asked to emit.
type inferJSON struct {
	Digest    string `json:"digest"`
	Citations []struct {
		RelPath   string `json:"relpath"`
		LineStart int    `json:"line_start"`
		LineEnd   int    `json:"line_end"`
		Claim     string `json:"claim"`
	} `json:"citations"`
	Leads []struct {
		RelPath  string `json:"relpath"`
		NearLine int    `json:"near_line"`
		Why      string `json:"why"`
	} `json:"leads"`
}

// ProviderInfer builds a provenance-constrained InferFunc from a
// Provider. A transport error is a real error; a malformed-JSON response
// (common from small models) is NOT fatal — it degrades to the salvaged
// prose as the digest with no citations, so the deepening loop keeps
// running and the provenance contract still forbids unverifiable
// citations. Centralizing this keeps both adapters (CLI + tool) robust.
func ProviderInfer(p llm.Provider) InferFunc {
	return func(ctx context.Context, in InferInput) (InferOutput, error) {
		sys, user := BuildInferPrompt(in)
		raw, err := p.GenerateWithSystem(ctx, user, sys)
		if err != nil {
			return InferOutput{}, err
		}
		out, perr := ParseInferResponse(raw)
		if perr != nil {
			return InferOutput{Digest: salvageDigest(raw)}, nil
		}
		return out, nil
	}
}

// ParseInferResponse extracts and decodes the JSON object from a model
// response, tolerating surrounding prose, code fences, and trailing
// commas.
func ParseInferResponse(raw string) (InferOutput, error) {
	obj, ok := extractJSONObject(raw)
	if !ok {
		return InferOutput{}, fmt.Errorf("study: no JSON object in inference response")
	}
	var j inferJSON
	err := json.Unmarshal([]byte(obj), &j)
	if err != nil {
		// Small models routinely emit trailing commas; repair and retry
		// once before giving up.
		err = json.Unmarshal([]byte(stripTrailingCommas(obj)), &j)
	}
	if err != nil {
		return InferOutput{}, fmt.Errorf("study: decode inference response: %w", err)
	}
	out := InferOutput{Digest: j.Digest}
	for _, c := range j.Citations {
		out.Citations = append(out.Citations, Citation{
			RelPath: c.RelPath, LineStart: c.LineStart, LineEnd: c.LineEnd, Claim: c.Claim,
		})
	}
	for _, l := range j.Leads {
		out.Leads = append(out.Leads, Lead{RelPath: l.RelPath, NearLine: l.NearLine, Why: l.Why})
	}
	return out, nil
}

// extractJSONObject returns the substring from the first '{' to the
// matching last '}'. Good enough for single-object responses wrapped in
// prose or ```json fences.
func extractJSONObject(s string) (string, bool) {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < start {
		return "", false
	}
	return s[start : end+1], true
}

var trailingCommaRE = regexp.MustCompile(`,(\s*[}\]])`)

// stripTrailingCommas removes commas that immediately precede a closing
// brace/bracket — the most common way small-model JSON is invalid.
func stripTrailingCommas(s string) string {
	return trailingCommaRE.ReplaceAllString(s, "$1")
}

var digestFieldRE = regexp.MustCompile(`"digest"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// salvageDigest best-effort extracts a digest from a response whose JSON
// couldn't be decoded: the "digest" field's value if present, else the
// fence-stripped, length-capped prose. Never returns citations — an
// unparseable response can't ground any.
func salvageDigest(raw string) string {
	if m := digestFieldRE.FindStringSubmatch(raw); len(m) == 2 {
		s := m[1]
		s = strings.ReplaceAll(s, `\"`, `"`)
		s = strings.ReplaceAll(s, `\n`, "\n")
		return strings.TrimSpace(s)
	}
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	const max = 2000
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
