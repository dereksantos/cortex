package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dereksantos/cortex/internal/projectscan"
)

// grep.go is the content locator study never had: a pure-Go regex search over the
// working tree returning capped file:line:text matches (never file bodies) — how a
// model jumps to a symbol, and how journal recall works. See docs/study-subagent.md §3.

const FunctionGrep = "grep"

const (
	grepMaxHits        = 100  // cap so a broad pattern can't flood context
	grepLineCap        = 1200 // window width for a long matching line (centered on the match)
	grepMaxOutputBytes = 6000 // total-output ceiling: long-line hits (journal JSONL) flood a small model's window even after the per-line cap
)

var filenameLikeRe = regexp.MustCompile(`[A-Za-z0-9_-]\.[A-Za-z0-9]`)

// GrepTool is the search declaration. The description names the RE2 dialect (no
// lookahead/backreferences) with one example so a model that emits PCRE adapts.
var GrepTool = newTool(FunctionGrep,
	"Search file contents for a regular expression and get back file:line:text "+
		"matches (never whole files). The fast way to locate a symbol, string, or "+
		"pattern across a path — then read_file the line you want. Uses RE2 syntax "+
		"(no lookahead or backreferences); e.g. `func.*Resolve` works, `(?=...)` does "+
		"not. Matches are capped; narrow the pattern or path if you need more.",
	objectSchema(map[string]any{
		"pattern": stringProp("The RE2 regular expression to search for (no lookahead/backreferences)."),
		"path":    stringProp("File or directory to search, relative to the working directory. A directory is searched recursively. Default: the whole project ('.')."),
	}, "pattern"))

// grep dispatches the grep tool: parse pattern + path, run the scan. A regex that
// fails to compile returns its error as the observation so the model retries.
func grep(ctx context.Context, tc ToolCall, deps ToolDeps) (string, error) {
	pattern, err := tc.StringArg("pattern")
	if err != nil {
		return "", err
	}
	root := "."
	if p, _ := tc.StringArg("path"); strings.TrimSpace(p) != "" {
		root = p
	}
	printToolAction(fmt.Sprintf("grep(%s, %s)", pattern, root))
	// Filesystem access goes through the session's workdir anchor (the "."
	// default included); the display line above keeps the relative path. The
	// broad-journal heuristic keys on the RELATIVE root — the shape the model
	// wrote — so it runs before anchoring (workdir.go).
	if isBroadJournalGrep(root, pattern) {
		hits, _ := grepJournalInstructionSummary(ctx, resolveWorkdir(deps, root), pattern, 5)
		return "journal grep pattern is too broad for large JSONL records. The query-filtered root-file candidate summary below is the bounded evidence; if it answers the goal, stop and answer from it instead of reading raw JSONL records.\n\nRoot-file candidate hits:\n" + hits, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		// Surface the compile error as the observation (RE2 rejects PCRE features),
		// not a harness failure — the model fixes the pattern and retries.
		return fmt.Sprintf("invalid regex %q: %v (grep uses RE2 — no lookahead or backreferences)", pattern, err), nil
	}
	return grepFiles(ctx, resolveWorkdir(deps, root), re, grepMaxHits)
}

func isBroadJournalGrep(root, pattern string) bool {
	root = filepath.ToSlash(strings.TrimSpace(root))
	if !strings.Contains(root, ".cortex/journal") {
		return false
	}
	p := strings.TrimSpace(pattern)
	if p == "" {
		return true
	}
	if strings.Contains(p, `\.`) || filenameLikeRe.MatchString(p) {
		return false
	}
	terms := strings.FieldsFunc(strings.ToLower(p), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_'
	})
	if len(terms) > 0 {
		broadTerms := map[string]bool{
			"context":    true,
			"file":       true,
			"read":       true,
			"repository": true,
			"root":       true,
			"seed":       true,
		}
		allBroad := true
		for _, term := range terms {
			if term == "" {
				continue
			}
			if !broadTerms[term] {
				allBroad = false
				break
			}
		}
		if allBroad {
			return true
		}
	}
	fields := strings.Fields(p)
	return len(fields) <= 2
}

var rootInstructionFileRe = regexp.MustCompile(`AGENTS\.md|CLAUDE\.md`)

func grepJournalInstructionSummary(ctx context.Context, root, pattern string, cap int) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("grep %s: %w", root, err)
	}
	absRoot, _ := filepath.Abs(root)
	ignore := projectscan.LoadIgnoreSet(absRoot)
	queryRe, queryTerms := broadJournalQueryMatcher(pattern)

	type hit struct {
		file  string
		line  int
		match string
	}
	var hits []hit
	counts := map[string]int{}
	full := func() bool { return len(hits) >= cap }

	scanFile := func(path, display string) error {
		if full() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		line := 0
		for sc.Scan() {
			line++
			text := sc.Bytes()
			if bytes.IndexByte(text, 0) >= 0 {
				return nil
			}
			if !matchesBroadJournalQuery(text, queryRe, queryTerms) {
				continue
			}
			for _, loc := range rootInstructionFileRe.FindAllIndex(text, -1) {
				m := string(text[loc[0]:loc[1]])
				counts[m]++
				if !full() {
					hits = append(hits, hit{file: display, line: line, match: m})
				}
			}
		}
		return nil
	}

	if !info.IsDir() {
		if err := scanFile(root, filepath.ToSlash(root)); err != nil {
			return "", err
		}
	} else {
		walkErr := filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() {
				if path != absRoot && ignore.IsDirExcluded(path, d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if ignore.IsFileExcluded(path) {
				return nil
			}
			rel, relErr := filepath.Rel(absRoot, path)
			if relErr != nil {
				rel = path
			}
			display := filepath.ToSlash(filepath.Join(root, rel))
			return scanFile(path, display)
		})
		if walkErr != nil && ctx.Err() != nil {
			return "", ctx.Err()
		}
	}

	if len(hits) == 0 {
		return "(no matches)", nil
	}
	var b strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&b, "%s:%d:%s\n", h.file, h.line, h.match)
	}
	if len(counts) > 0 {
		b.WriteString("summary:")
		bestName, bestCount := "", 0
		for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
			if n := counts[name]; n > 0 {
				fmt.Fprintf(&b, " %s=%d", name, n)
				if n > bestCount {
					bestName, bestCount = name, n
				}
			}
		}
		if bestName != "" {
			fmt.Fprintf(&b, "\nmost_frequent_candidate: %s", bestName)
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func broadJournalQueryMatcher(pattern string) (*regexp.Regexp, []string) {
	if re, err := regexp.Compile(pattern); err == nil {
		return re, nil
	}
	return nil, broadJournalTerms(pattern)
}

func matchesBroadJournalQuery(text []byte, re *regexp.Regexp, terms []string) bool {
	if re != nil {
		return re.Match(text)
	}
	lower := strings.ToLower(string(text))
	for _, term := range terms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func broadJournalTerms(pattern string) []string {
	raw := strings.FieldsFunc(strings.ToLower(pattern), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_'
	})
	var terms []string
	for _, term := range raw {
		if term != "" {
			terms = append(terms, term)
		}
	}
	return terms
}

// grepFiles walks root (reusing projectscan's ignore set), matches re per line,
// and returns up to cap "file:line:text" hits. Pure Go: filepath.WalkDir +
// regexp + bufio, no exec. It checks ctx.Err() in the walk so the per-request
// deadline / cancellation interrupts a scan of a huge tree.
func grepFiles(ctx context.Context, root string, re *regexp.Regexp, cap int) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("grep %s: %w", root, err)
	}
	absRoot, _ := filepath.Abs(root)
	ignore := projectscan.LoadIgnoreSet(absRoot)

	var hits []string
	hitBytes := 0
	truncated := false
	full := func() bool { return len(hits) >= cap || hitBytes >= grepMaxOutputBytes }

	scanFile := func(path, display string) error {
		if full() {
			truncated = true
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		line := 0
		for sc.Scan() {
			line++
			text := sc.Bytes()
			if bytes.IndexByte(text, 0) >= 0 {
				return nil // binary file — skip the rest
			}
			if loc := re.FindIndex(text); loc != nil {
				if full() {
					truncated = true
					return nil
				}
				hit := fmt.Sprintf("%s:%d:%s", display, line, capLine(text, loc[0]))
				hits = append(hits, hit)
				hitBytes += len(hit) + 1
			}
		}
		return nil
	}

	if !info.IsDir() {
		if err := scanFile(root, filepath.ToSlash(root)); err != nil {
			return "", err
		}
	} else {
		walkErr := filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() {
				if path != absRoot && ignore.IsDirExcluded(path, d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if ignore.IsFileExcluded(path) {
				return nil
			}
			if full() {
				truncated = true
				return filepath.SkipAll
			}
			rel, relErr := filepath.Rel(absRoot, path)
			if relErr != nil {
				rel = path
			}
			display := filepath.ToSlash(filepath.Join(root, rel))
			return scanFile(path, display)
		})
		if walkErr != nil && ctx.Err() != nil {
			return "", ctx.Err()
		}
	}

	if len(hits) == 0 {
		return "(no matches)", nil
	}
	out := strings.Join(hits, "\n")
	if truncated {
		out += fmt.Sprintf("\n… (capped at %d matches / %dKB — narrow the pattern or path)", cap, grepMaxOutputBytes/1000)
	}
	return out, nil
}

// capLine returns a grepLineCap window CENTERED on match offset `at`, keeping a hit deep in a long (JSONL) line visible.
func capLine(b []byte, at int) string {
	s := strings.TrimRight(string(b), "\r")
	if len(s) <= grepLineCap {
		return s
	}
	start := at - grepLineCap/2
	if start < 0 {
		start = 0
	}
	if start+grepLineCap > len(s) {
		start = len(s) - grepLineCap
	}
	return "…" + s[start:start+grepLineCap] + "…"
}
