package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Issue #228: tests that EXECUTE a shell command must use forms portable
// across GNU and BSD (macOS) tools. Bare `sed -i 'SCRIPT' file` is the
// recurring offender: BSD sed reads the next argument as a backup suffix, so
// it tries to write `SCRIPT`-bak and fails. `sed -i.bak` (suffix attached to
// the flag) is portable. #203 and #227 both bit CI this way after the Linux
// loop's checks passed.
//
// Detector tables that only PARSE command strings (TestDetectInPlaceRewrites
// and friends) exercise exactly the non-portable shapes on purpose and must
// not be flagged. The tell of an EXECUTED command is that it reaches a bash
// tool call: a `bashCall(t, ...)` line, or a command table fed to
// `bashCall(t, tc.cmd)`.

// bareSedIn is a sed -i whose script argument is NOT attached as a suffix:
// `sed -i '...'` or `sed -i -e '...'` (env-prefixed forms like LC_ALL=C
// match too). `sed -i.bak '...'` does not match: the character right after
// the `-i` is the suffix (a letter/digit/`.`/`_`), not whitespace.
var bareSedIn = regexp.MustCompile(`(^|[\s;|&` + "`" + `])([A-Za-z_][A-Za-z0-9_]*=.)*sed\s+-i[\s]`)

// tcCmdField is the table-driven pattern: the struct field carries the
// command that is later EXECUTED via bashCall(t, tc.cmd).
var tcCmdField = regexp.MustCompile(`(?m)^\s*cmd\s+string`)

// TestBareSedInFlags pins the detector itself against fixtures, so the
// acceptance criterion (fails on an executed bare `sed -i '…' f`, passes on
// portable / detector-only shapes) is checked without touching the tree.
// The fixture lines are assembled from fragments so this file's own source
// never contains an executable-looking bare form: the tree-walk in
// TestNoExecutedBareSedInInTests must stay clean on this very file.
func TestBareSedInFlags(t *testing.T) {
	// `sed -i` + space is the non-portable bare form; `sed -i.bak` is the
	// portable one. Assembled here so neither appears verbatim above.
	bare := "sed -i" + " "
	portable := "sed -i" + ".bak"
	exec := "bashCall(t, "
	for _, tc := range []struct {
		name  string
		snips []string
		want  bool // should the guard flag this file?
	}{
		{"executed bare sed -i", []string{
			exec + "`" + bare + "'s/x/y/' main.go`)",
		}, true},
		{"executed bare sed -i -e", []string{
			exec + "`" + bare + "-e 's/x/y/' main.go`)",
		}, true},
		{"executed portable sed -i.bak", []string{
			exec + "`" + portable + " 's/x/y/' main.go`)",
		}, false},
		{"detector table: bare form parsed, never executed", []string{
			"{" + "`" + bare + "'s/x/y/' f.go`" + ", []string{\"f.go\"}}",
			"detectInPlaceRewrites(tc.cmd)",
		}, false},
		{"detector table with tc.cmd field: commands are only parsed", []string{
			"cmd      string",
			"{\"bare form\", `" + bare + "'s/x/y/' f.go`}",
			"detectInPlaceRewrites(tc.cmd)",
		}, false},
		{"table whose tc.cmd IS executed: flagged", []string{
			"cases := []struct {\n\tname string\n\tcmd  string\n\t} {\n",
			"{\"sed note\", `" + bare + "'s/x/y/' f.go && true`},\n",
			"}\n",
			"for _, tc := range cases {\n\tt.Run(tc.name, func(t *testing.T) {\n",
			"bashCall(t, tc.cmd)",
		}, true},
		{"env-prefixed bare sed -i executed", []string{
			exec + "`LC_ALL=C " + bare + "'s/x/y/' f.go`)",
		}, true},
		{"sed without -i is a read: fine anywhere", []string{
			exec + "`sed -n '1,10p' f.go`)",
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := flagsBareSedIn(strings.Join(tc.snips, "\n")); got != tc.want {
				t.Errorf("flagsBareSedIn(%v) = %v, want %v", tc.snips, got, tc.want)
			}
		})
	}
}

// TestNoExecutedBareSedInInTests walks every *_test.go in the repo and fails
// on any command string that executes a bare `sed -i` (see package comment)
// — the class of failure that only shows up when CI runs on macOS.
func TestNoExecutedBareSedInInTests(t *testing.T) {
	root, err := findRepoRoot(".")
	if err != nil {
		t.Fatalf("finding repo root: %v", err)
	}
	var flagged []string
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == ".cortex" || strings.HasPrefix(info.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if flagsBareSedIn(string(data)) {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			flagged = append(flagged, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(flagged) > 0 {
		t.Errorf("executed bare `sed -i '…'` (GNU-only) in test command strings: %v — use the portable `sed -i.bak` form (issue #228)", flagged)
	}
}

// flagsBareSedIn reports whether src EXECUTES a bare `sed -i` command: a
// bareSedIn occurrence on a `bashCall(t, ...)` line, or in the table that a
// `for` loop ranges over and whose tc.cmd is passed to bashCall.
func flagsBareSedIn(src string) bool {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "bashCall(t,") {
			continue
		}
		if bareSedIn.MatchString(line) {
			return true
		}
		// Table-driven execution: this bashCall takes tc.cmd from the table
		// the enclosing loop ranges over, so the bare form lives in the
		// table's string literals, not on this line.
		if strings.Contains(line, "bashCall(t, tc.cmd)") && tableFeedsTCExecuted(lines, i) {
			return true
		}
	}
	return false
}

// tableFeedsTCExecuted reports whether the table that the `for ...` loop
// enclosing bashCall line lineIdx ranges over carries a bareSedIn command in
// one of its string literals (the command is executed when the loop body
// calls bashCall(t, tc.cmd)).
func tableFeedsTCExecuted(lines []string, lineIdx int) bool {
	// Find the enclosing `for ... range` (the loop body below lineIdx
	// executes tc.cmd, so its range expression is the table).
	start := -1
	for j := lineIdx; j >= 0; j-- {
		if strings.Contains(lines[j], "range ") {
			start = j
			break
		}
		if strings.HasPrefix(strings.TrimSpace(lines[j]), "func ") {
			return false // left the enclosing function: no loop found
		}
	}
	if start < 0 {
		return false
	}
	// Inline literal: `for _, tc := range []struct{...}{ ... }` — the table
	// is the loop's range expression (between the loop header and the
	// bashCall line); a detector-only table would never feed bashCall.
	if strings.Contains(lines[start], "range []struct") {
		return bareSedIn.MatchString(strings.Join(lines[start:lineIdx+1], "\n"))
	}
	// Named variable: `for _, tc := range cases` — the variable is declared
	// above in this function. The struct type is read to confirm the table
	// carries a command field; the literals run from the declaration to the
	// bashCall line.
	for j := start - 1; j >= 0; j-- {
		if strings.HasPrefix(strings.TrimSpace(lines[j]), "func ") {
			return false
		}
		if !strings.Contains(lines[j], "= []struct") {
			continue
		}
		if !tcCmdField.MatchString(structType(lines, j, start)) {
			return false
		}
		return bareSedIn.MatchString(strings.Join(lines[j:lineIdx+1], "\n"))
	}
	return false
}

// structType returns the struct type text of the `x := []struct{...}`
// declaration at line decl (the field list, from `struct` to its closing
// `}`); start bounds the scan. The type is what distinguishes a command
// table (with a `cmd string` field) from a non-command table (wantSubs/
// wantFiles/...) that just happens to share a `cmd string` line above the
// loop.
func structType(lines []string, decl, start int) string {
	for i := decl; i < start; i++ {
		k := strings.Index(lines[i], "struct")
		if k < 0 {
			continue
		}
		brace := strings.IndexByte(lines[i][k:], '{')
		if brace < 0 {
			continue
		}
		depth := 0
		for l := i; l < start; l++ {
			from := 0
			if l == i {
				from = k + brace + 1
			}
			for _, r := range lines[l][from:] {
				switch r {
				case '{':
					depth++
				case '}':
					depth--
					if depth == 0 {
						if l == i {
							return lines[i][k:]
						}
						end := from
						for j := from; j < len(lines[l]); j++ {
							if lines[l][j] == '}' {
								end = j + 1
								break
							}
						}
						parts := append([]string{lines[i][k:]}, lines[i+1:l]...)
						parts = append(parts, lines[l][:end])
						return strings.Join(parts, "\n")
					}
				}
			}
		}
	}
	return ""
}

// findRepoRoot walks up from start until a go.mod sits next to a .git dir
// (this repo); it falls back to the topmost directory that has a go.mod, so
// the guard still walks the right tree if the .git dir is renamed.
func findRepoRoot(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	topmost := ""
	dir := abs
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				return dir, nil
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			topmost = dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if topmost == "" {
		return "", os.ErrNotExist
	}
	return topmost, nil
}
