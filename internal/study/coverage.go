package study

import (
	"bytes"
	"strings"
)

// langFor returns the language hint derived from a file extension.
// Lowercase, no leading dot. "unknown" when nothing fits — callers
// fall back to non-blank-only counting.
func langFor(ext string) string {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	switch ext {
	case "go":
		return "go"
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
	case "cs":
		return "cs"
	case "swift":
		return "swift"
	case "kt", "kts":
		return "kt"
	case "scala":
		return "scala"
	case "rb":
		return "rb"
	case "sh", "bash", "zsh":
		return "sh"
	case "toml":
		return "toml"
	case "yaml", "yml":
		return "yaml"
	case "conf", "cfg", "ini":
		return "ini"
	case "tf":
		return "tf"
	case "lua":
		return "lua"
	case "sql":
		return "sql"
	case "hs":
		return "hs"
	case "md", "markdown":
		return "md"
	case "txt":
		return "txt"
	case "rst":
		return "rst"
	}
	return "unknown"
}

// effectiveLinesOf counts the non-blank, non-comment lines in the
// given byte slice. The language hint dispatches to a comment-line
// detector; unknown languages get a non-blank-only count.
//
// The "comment" category collapses single-line markers (//, #, --) and
// multi-line C-style /* ... */ blocks. Markdown / plain text / rst /
// no-extension files treat every non-blank line as effective (there's
// no useful comment convention).
func effectiveLinesOf(content []byte, lang string) int {
	if len(content) == 0 {
		return 0
	}
	lines := bytes.Split(content, []byte{'\n'})
	switch lang {
	case "go", "js", "ts", "rs", "java", "c", "cs", "swift", "kt", "scala":
		return countCStyle(lines)
	case "py", "rb", "sh", "toml", "yaml", "ini", "tf":
		return countHashStyle(lines)
	case "lua", "sql", "hs":
		return countDashStyle(lines)
	case "md", "txt", "rst", "unknown", "":
		return countNonBlank(lines)
	}
	return countNonBlank(lines)
}

// countNonBlank — every line with non-whitespace content counts.
func countNonBlank(lines [][]byte) int {
	n := 0
	for _, l := range lines {
		if len(bytes.TrimSpace(l)) > 0 {
			n++
		}
	}
	return n
}

// countHashStyle — non-blank lines whose first non-whitespace token is
// not '#' count.
func countHashStyle(lines [][]byte) int {
	n := 0
	for _, l := range lines {
		t := bytes.TrimSpace(l)
		if len(t) == 0 {
			continue
		}
		if t[0] == '#' {
			continue
		}
		n++
	}
	return n
}

// countDashStyle — non-blank lines not starting with "--" count.
func countDashStyle(lines [][]byte) int {
	n := 0
	for _, l := range lines {
		t := bytes.TrimSpace(l)
		if len(t) == 0 {
			continue
		}
		if len(t) >= 2 && t[0] == '-' && t[1] == '-' {
			continue
		}
		n++
	}
	return n
}

// countCStyle — non-blank lines that are not entirely inside a
// /* ... */ block and don't start with "//" count. The /* */ scan is
// stateful: a line that opens a block contributes nothing (even if
// content follows the opener); a line that closes a block also
// contributes nothing (even if content trails). This is a heuristic,
// not a parser — sufficient for an effective-LOC proxy.
func countCStyle(lines [][]byte) int {
	n := 0
	inBlock := false
	for _, l := range lines {
		t := bytes.TrimSpace(l)
		if len(t) == 0 {
			continue
		}
		if inBlock {
			// Look for the close marker; if found, exit block but
			// don't count this line.
			if idx := bytes.Index(t, []byte("*/")); idx >= 0 {
				inBlock = false
			}
			continue
		}
		if bytes.HasPrefix(t, []byte("//")) {
			continue
		}
		if bytes.HasPrefix(t, []byte("/*")) {
			// Single-line /* ... */ closing on same line?
			if bytes.Contains(t[2:], []byte("*/")) {
				// Whole line is a comment — skip.
				continue
			}
			inBlock = true
			continue
		}
		n++
	}
	return n
}
