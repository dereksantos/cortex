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
	grepMaxHits        = 100     // cap so a broad pattern can't flood context
	grepMaxFileSize    = 1 << 20 // skip files larger than 1 MiB
	grepLineCap        = 300     // window width for a long matching line (centered on the match)
	grepMaxOutputBytes = 12000   // total-output ceiling: long-line hits (journal JSONL) flood context even after the per-line cap
)

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
func grep(ctx context.Context, tc ToolCall) (string, error) {
	pattern, err := tc.StringArg("pattern")
	if err != nil {
		return "", err
	}
	root := "."
	if p, _ := tc.StringArg("path"); strings.TrimSpace(p) != "" {
		root = p
	}
	printToolAction(fmt.Sprintf("grep(%s, %s)", pattern, root))
	re, err := regexp.Compile(pattern)
	if err != nil {
		// Surface the compile error as the observation (RE2 rejects PCRE features),
		// not a harness failure — the model fixes the pattern and retries.
		return fmt.Sprintf("invalid regex %q: %v (grep uses RE2 — no lookahead or backreferences)", pattern, err), nil
	}
	return grepFiles(ctx, root, re, grepMaxHits)
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
		fi, err := os.Stat(path)
		if err != nil || fi.Size() > grepMaxFileSize {
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
