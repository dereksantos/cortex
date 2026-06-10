package study

import (
	"bytes"
	"regexp"
)

// Tier 1.5 of the boundary ladder (see BoundaryAnalyzer.Tier): the
// byte-grid (Tier 1) cuts at arbitrary offsets; full AST chunking
// (Tier 3) is per-language parser work. This layer captures most of
// Tier 3's benefit mechanically, for any format:
//
//   - per-format COHERENCE UNITS size chunks to the smallest span
//     that is self-interpretable without its neighbors (a function, a
//     paragraph, a run of records), and
//   - per-format boundary REGEXES snap a chunk's leading edge to the
//     nearest unit start, so sampled fragments tend to begin at a
//     whole declaration / heading / paragraph instead of mid-symbol.
//
// Evidence: the 2026-06-10 granularity sweep (docs/eval-journal.md).
// At equal total sample, grounding went 57% (245-line fragments, cut
// anywhere) → 100% (~60 lines ≈ one Go function) → 86% (~40 lines,
// below one unit). Unit-sized fragments are the optimum; boundary
// alignment removes the remaining cut-point luck.

// Coherence-unit targets in bytes. Only the code value is measured
// (Go, via the granularity sweep); prose and data are estimates to be
// tuned the same way once study-eval grows non-code fixtures.
const (
	unitBytesCode  = 3072 // ≈ one function/declaration (~60 lines)
	unitBytesProse = 1024 // ≈ a few paragraphs / one section opening
	unitBytesData  = 2048 // ≈ a run of records or one config table
)

// unitBytesFor returns the coherence-unit chunk target for a langFor
// output, or 0 for unknown formats (caller falls back to the
// window-derived default).
func unitBytesFor(lang string) int {
	switch lang {
	case "go", "py", "js", "ts", "rs", "java", "c", "cs", "swift",
		"kt", "scala", "rb", "sh", "lua", "sql", "hs", "tf":
		return unitBytesCode
	case "md", "txt", "rst":
		return unitBytesProse
	case "json", "yaml", "toml", "ini", "csv":
		return unitBytesData
	}
	return 0
}

// boundaryRe maps a langFor output to "this line starts a coherence
// unit". Formats absent from the map use the universal paragraph rule
// (a non-blank line preceded by a blank line). Precision is not
// critical — a false-positive boundary still lands on a plausible
// line start, and snapping is bounded by slack — so the patterns stay
// deliberately coarse: column-0 declaration keywords, headings, table
// headers.
var boundaryRe = map[string]*regexp.Regexp{
	"go":    regexp.MustCompile(`^(func|type|const|var|package|import)\b`),
	"py":    regexp.MustCompile(`^(def |class |async def |@\w)`),
	"js":    regexp.MustCompile(`^(function|class|const|let|var|export|import|async function)\b`),
	"ts":    regexp.MustCompile(`^(function|class|const|let|var|export|import|interface|type|async function)\b`),
	"rs":    regexp.MustCompile(`^(pub|fn|struct|enum|impl|trait|mod|use|macro_rules!)\b`),
	"java":  regexp.MustCompile(`^[A-Za-z@]`),
	"cs":    regexp.MustCompile(`^[A-Za-z@\[]`),
	"kt":    regexp.MustCompile(`^[A-Za-z@]`),
	"scala": regexp.MustCompile(`^[A-Za-z@]`),
	"swift": regexp.MustCompile(`^[A-Za-z@]`),
	"c":     regexp.MustCompile(`^[A-Za-z_#]`),
	"rb":    regexp.MustCompile(`^(def |class |module |require\b)`),
	"sh":    regexp.MustCompile(`^(function\b|\w+\s*\(\)\s*\{)`),
	"lua":   regexp.MustCompile(`^(function|local)\b`),
	"sql":   regexp.MustCompile(`(?i)^(create|alter|insert|select|drop|update|delete|with)\b`),
	"hs":    regexp.MustCompile(`^\w`),
	"tf":    regexp.MustCompile(`^(resource|module|variable|output|provider|data|locals|terraform)\b`),
	"md":    regexp.MustCompile(`^#{1,6}\s`),
	"toml":  regexp.MustCompile(`^\[`),
	"ini":   regexp.MustCompile(`^\[`),
	"yaml":  regexp.MustCompile(`^[^\s#]`),
	"json":  regexp.MustCompile(`^\s{0,2}["{\[]`),
}

// snapToBoundary returns the byte offset within body of the first
// coherence-boundary line in the chunk's first half, so the caller can
// drop the partial leading unit. Returns 0 when the chunk already
// starts at a boundary, or when no boundary appears within the slack
// (better a mid-unit start than losing over half the chunk).
//
// The first line's predecessor is unknown to the paragraph rule, so it
// is never treated as a paragraph start — callers that know the chunk
// sits at a unit start (offset 0) should skip snapping entirely.
func snapToBoundary(body []byte, lang string) int {
	if len(body) == 0 {
		return 0
	}
	slack := len(body) / 2
	re := boundaryRe[lang]
	off := 0
	prevBlank := false
	for off <= slack {
		rest := body[off:]
		end := bytes.IndexByte(rest, '\n')
		line := rest
		if end >= 0 {
			line = rest[:end]
		}
		var isBoundary bool
		if re != nil {
			isBoundary = re.Match(line)
		} else {
			isBoundary = prevBlank && len(bytes.TrimSpace(line)) > 0
		}
		if isBoundary {
			return off // off == 0 → already aligned, no snap
		}
		prevBlank = len(bytes.TrimSpace(line)) == 0
		if end < 0 {
			break
		}
		off += end + 1
	}
	return 0
}
