package projectindex

import (
	"path/filepath"
	"regexp"
	"strings"
)

// outline.go is the non-Go tier of map: the cheapest structural outline a file's
// format affords, so a navigator gets line-span sections to read precisely
// instead of a blind blob. The tiers (see the study-navigator direction doc):
//
//	tier 2  code (py/js/ts/rs/…)  — regex declaration scan
//	tier 3  prose (md/rst/txt)    — heading scan, then paragraph blocks
//	tier 4  data (json/yaml/…)    — section/key markers
//	tier 5  anything else         — positional windows (a line-range table)
//
// Go files do NOT come here — they use the go/ast path (goSymbols), which is
// richer. "Unstructured" just means a lower tier, never no outline.

// outlineMaxSections caps an outline so a huge or pathological file can't emit
// thousands of sections. Past the cap, the positional tier widens its window.
const outlineMaxSections = 80

// outlineNameCap truncates a section label (often a whole source line) so the
// skeleton stays one-line-per-section.
const outlineNameCap = 72

// positionalMinWindow is the smallest line window the tier-5 fallback uses; it
// grows to keep the section count under outlineMaxSections.
const positionalMinWindow = 60

// outlineRe maps a language to "this line starts a coherence unit" — the same
// coarse, column-0 patterns study's boundary snapper uses. Precision isn't
// critical: a section just needs to begin at a plausible unit start. Go is
// absent (it uses the AST path); formats absent here fall to prose/positional.
var outlineRe = map[string]*regexp.Regexp{
	"py":   regexp.MustCompile(`^(def |class |async def |@\w)`),
	"js":   regexp.MustCompile(`^(export |function|class|const|let|var|async function)\b`),
	"ts":   regexp.MustCompile(`^(export |function|class|const|let|var|interface|type|enum|async function)\b`),
	"rs":   regexp.MustCompile(`^(pub|fn|struct|enum|impl|trait|mod|macro_rules!)\b`),
	"java": regexp.MustCompile(`^\s*(public|private|protected|static|class|interface|enum|@)\w`),
	"c":    regexp.MustCompile(`^[A-Za-z_].*[A-Za-z0-9_]\s*\(`),
	"rb":   regexp.MustCompile(`^(def |class |module )`),
	"sh":   regexp.MustCompile(`^(function\b|\w+\s*\(\)\s*\{)`),
	"lua":  regexp.MustCompile(`^(function|local function)\b`),
	"sql":  regexp.MustCompile(`(?i)^(create|alter|insert|drop)\b`),
	"tf":   regexp.MustCompile(`^(resource|module|variable|output|provider|data|locals|terraform)\b`),
	"md":   regexp.MustCompile(`^#{1,6}\s`),
	"rst":  regexp.MustCompile(`^[=~^"#*+-]{3,}\s*$`),
	"toml": regexp.MustCompile(`^\[`),
	"ini":  regexp.MustCompile(`^\[`),
	"yaml": regexp.MustCompile(`^[A-Za-z_][\w-]*:`),
	"json": regexp.MustCompile(`^\s{2,4}"[^"]+"\s*:`),
}

// outlineSymbols returns the structural outline of a NON-Go file, choosing the
// best tier for its format and falling back to positional windows when no
// pattern grips. Returns nil for an empty file or a small file with no structure
// (small files are read whole — an outline buys nothing).
func outlineSymbols(path string, data []byte) []Symbol {
	if len(data) == 0 {
		return nil
	}
	lines := splitLines(data)
	lang := fileLang(path)

	if re := outlineRe[lang]; re != nil {
		if syms := scanBoundaries(lines, re, sectionKind(lang)); len(syms) >= 2 {
			return syms
		}
	}
	// Prose with no headings: paragraph blocks (a non-blank line after a blank).
	if isProse(lang) {
		if syms := scanParagraphs(lines); len(syms) >= 2 {
			return syms
		}
	}
	// Floor: a positional table of contents — still navigable, just coarse.
	if len(lines) > positionalMinWindow {
		return positionalSections(len(lines))
	}
	return nil
}

// scanBoundaries turns every line matching re into a section running until the
// next match (or EOF). The label is the matched line, trimmed and capped.
func scanBoundaries(lines []string, re *regexp.Regexp, kind string) []Symbol {
	var starts []int
	for i, ln := range lines {
		if re.MatchString(ln) {
			starts = append(starts, i)
		}
	}
	if len(starts) == 0 {
		return nil
	}
	syms := make([]Symbol, 0, len(starts))
	for i, s := range starts {
		end := len(lines) - 1
		if i+1 < len(starts) {
			end = starts[i+1] - 1
		}
		syms = append(syms, Symbol{
			Name:    label(lines[s]),
			Kind:    kind,
			Line:    s + 1,
			EndLine: end + 1,
		})
	}
	return capSections(syms)
}

// scanParagraphs sections prose at blank-line boundaries (a non-blank line whose
// predecessor is blank starts a paragraph). The label is the paragraph's first
// line — usually its topic.
func scanParagraphs(lines []string) []Symbol {
	var starts []int
	prevBlank := true // line 0 starts the first paragraph
	for i, ln := range lines {
		blank := strings.TrimSpace(ln) == ""
		if !blank && prevBlank {
			starts = append(starts, i)
		}
		prevBlank = blank
	}
	if len(starts) < 2 {
		return nil
	}
	syms := make([]Symbol, 0, len(starts))
	for i, s := range starts {
		end := len(lines) - 1
		if i+1 < len(starts) {
			end = starts[i+1] - 1
		}
		syms = append(syms, Symbol{Name: label(lines[s]), Kind: "para", Line: s + 1, EndLine: end + 1})
	}
	return capSections(syms)
}

// positionalSections divides a file into equal line windows — the tier-5 floor
// for content with no internal structure. The window grows to keep the count
// under outlineMaxSections.
func positionalSections(total int) []Symbol {
	window := positionalMinWindow
	if n := total / outlineMaxSections; n > window {
		window = n
	}
	var syms []Symbol
	for start := 1; start <= total; start += window {
		end := start + window - 1
		if end > total {
			end = total
		}
		syms = append(syms, Symbol{Kind: "lines", Line: start, EndLine: end})
	}
	return syms
}

// capSections keeps an outline under outlineMaxSections by coalescing adjacent
// sections when there are too many — the label of the first in each merged group
// survives, and the span covers the group. Bounds the outline's own size.
func capSections(syms []Symbol) []Symbol {
	if len(syms) <= outlineMaxSections {
		return syms
	}
	group := (len(syms) + outlineMaxSections - 1) / outlineMaxSections
	var out []Symbol
	for i := 0; i < len(syms); i += group {
		j := i + group - 1
		if j >= len(syms) {
			j = len(syms) - 1
		}
		s := syms[i]
		s.EndLine = syms[j].EndLine
		out = append(out, s)
	}
	return out
}

// label trims and caps a line to a one-line section name.
func label(line string) string {
	s := strings.TrimSpace(line)
	if len(s) > outlineNameCap {
		s = s[:outlineNameCap] + "…"
	}
	return s
}

func splitLines(data []byte) []string {
	lines := strings.Split(string(data), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1] // trailing newline isn't a real line
	}
	return lines
}

func isProse(lang string) bool {
	switch lang {
	case "md", "rst", "txt", "":
		return true
	}
	return false
}

// sectionKind is the short label rendered in the skeleton's kind column for a
// boundary-scanned section, by format family.
func sectionKind(lang string) string {
	switch lang {
	case "md", "rst":
		return "head"
	case "toml", "ini", "yaml", "json":
		return "key"
	default:
		return "decl"
	}
}

// fileLang maps a file extension to the outline language key for the formats
// that have an outline pattern; "" for unknown (→ prose or positional).
func fileLang(path string) string {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "py":
		return "py"
	case "js", "jsx", "mjs", "cjs":
		return "js"
	case "ts", "tsx":
		return "ts"
	case "rs":
		return "rs"
	case "java":
		return "java"
	case "c", "cc", "cpp", "cxx", "h", "hpp", "hxx":
		return "c"
	case "rb":
		return "rb"
	case "sh", "bash", "zsh":
		return "sh"
	case "lua":
		return "lua"
	case "sql":
		return "sql"
	case "tf":
		return "tf"
	case "md", "markdown":
		return "md"
	case "rst":
		return "rst"
	case "txt":
		return "txt"
	case "toml":
		return "toml"
	case "ini", "conf", "cfg":
		return "ini"
	case "yaml", "yml":
		return "yaml"
	case "json", "jsonl", "ndjson":
		return "json"
	}
	return ""
}
