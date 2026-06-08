package study

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
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
	fmt.Fprintf(&b, "Sampled regions of %s (a PARTIAL view — not the whole file):\n\n", display)
	for _, s := range in.Sampled {
		fmt.Fprintf(&b, "----- %s:%d-%d -----\n%s\n\n", s.RelPath, s.LineStart, s.LineEnd, s.Snippet)
	}
	return inferSystemPrompt, b.String()
}

func describeFocus(f *Focus) string {
	if f == nil {
		return ""
	}
	switch {
	case f.Symbol != "":
		return "symbol " + f.Symbol
	case f.Query != "":
		return f.Query
	case f.Lines[0] > 0 || f.Lines[1] > 0:
		return fmt.Sprintf("lines %d-%d", f.Lines[0], f.Lines[1])
	}
	return ""
}

// ValidateCitations keeps only citations whose relpath matches a sampled
// chunk AND whose [line_start,line_end] is fully contained in that
// chunk's range. Unverifiable citations are passed to onDrop (when
// non-nil) and excluded from the result.
func ValidateCitations(cits []Citation, sampled []SampledChunk, onDrop func(Citation)) []Citation {
	valid := make([]Citation, 0, len(cits))
	for _, c := range cits {
		if citationInSample(c, sampled) {
			valid = append(valid, c)
		} else if onDrop != nil {
			onDrop(c)
		}
	}
	return valid
}

func citationInSample(c Citation, sampled []SampledChunk) bool {
	if c.LineStart <= 0 || c.LineEnd < c.LineStart {
		return false
	}
	for _, s := range sampled {
		if s.RelPath == c.RelPath && c.LineStart >= s.LineStart && c.LineEnd <= s.LineEnd {
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
