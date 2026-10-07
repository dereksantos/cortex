// writesanity_test.go pins issue #224's mechanical lever: after a .go file
// lands, writeSanityNote reports EVERY package-name problem in one note —
// duplicate package-level declarations (the okResponse-collision class) and
// bare references to names that exist nowhere (the itoa / FinishReason
// class) — and stays silent for everything it must not judge. Stdlib-only,
// table-driven, per the Constraints section.
package tools

import (
	"context"
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// notWindowsTag is the //go:build expression naming the OTHER platform from
// the one the tests run on, so the constraint fixtures are a genuinely
// exclusive pair on every GOOS (windows when building for windows, anything
// else otherwise). A test that hard-coded "!windows" would assert a
// DUPLICATE declaration on Windows, where both of its twins build.
var notWindowsTag = func() string {
	if build.Default.GOOS == "windows" {
		return "linux"
	}
	return "!windows"
}()

// writeGoFiles creates dir/files (map name→content) and returns dir.
func writeGoFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	return dir
}

func TestWriteSanityNote(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string // package files, written to a temp dir
		target  string            // the file "just written" (base name in files)
		want    []string          // substrings the note must contain
		wantNil bool              // the note must be empty
	}{
		{
			name: "clean package is silent",
			files: map[string]string{
				"a.go": "package p\n\nimport \"fmt\"\n\nfunc Helper() string { return fmt.Sprint(1) }\n",
				"b.go": "package p\n\nfunc Use() string { return Helper() }\n",
			},
			target:  "b.go",
			wantNil: true,
		},
		{
			name: "duplicate package-level const across files (okResponse class)",
			files: map[string]string{
				"a.go":      "package p\n\nconst okResponse = 200\n",
				"a_test.go": "package p\n\nconst okResponse = 200\n\nfunc TestX(t *testing.T) {}\n",
			},
			target: "a_test.go",
			want:   []string{"const okResponse is declared 2 times"},
		},
		{
			name: "duplicate func across sibling test files",
			files: map[string]string{
				"a_test.go": "package p\n\nfunc helper() string { return \"a\" }\n",
				"b_test.go": "package p\n\nfunc helper() string { return \"b\" }\n",
			},
			target: "b_test.go",
			want:   []string{"func helper is declared 2 times"},
		},
		{
			name: "undefined identifiers reported ALL at once (seven-name class)",
			files: map[string]string{
				"a.go": "package p\n\nfunc Use() {\n\titoa(1)\n\teditFileCallResp()\n\tremovePathCallResp()\n\tFinishReason{}\n\tgot()\n\twant()\n\tmissingThing()\n}\n",
			},
			target: "a.go",
			want: []string{
				"name problem(s)",
				`"itoa" is undefined`,
				`"editFileCallResp" is undefined`,
				`"removePathCallResp" is undefined`,
				`"FinishReason" is undefined`,
				`"got" is undefined`,
				`"want" is undefined`,
				`"missingThing" is undefined`,
			},
		},
		{
			name: "a helper defined in a sibling test file is NOT undefined",
			files: map[string]string{
				"a_test.go": "package p\n\nfunc helper() string { return \"a\" }\n",
				"b_test.go": "package p\n\nfunc TestB(t int) { _ = helper() }\n",
			},
			target:  "b_test.go",
			wantNil: true,
		},
		{
			name: "external test package files are not merged in",
			files: map[string]string{
				"a.go":        "package p\n\nconst okResponse = 200\n",
				"ext_test.go": "package p_test\n\nconst okResponse = 200\n",
			},
			target:  "a.go",
			wantNil: true, // p_test's const is a different package: no collision with p's
		},
		{
			name: "non-Go file is byte-identical silence",
			files: map[string]string{
				"notes.md": "# notes\nnothing to parse here\n",
			},
			target:  "notes.md",
			wantNil: true,
		},
		{
			name: "a file that does not parse is silent",
			files: map[string]string{
				"a.go": "package p\n\nfunc Broken( {\n",
			},
			target:  "a.go",
			wantNil: true, // syntax errors belong to the model's own build
		},
		{
			name: "locals params struct fields and receivers are never flagged",
			files: map[string]string{
				"a.go": `package p

import "fmt"

type Shape struct {
	Area float64
}

func (s Shape) String() string { return fmt.Sprintf("%v", s.Area) }

func Use() {
	local := 1
	defer func() { _ = local }()
	for i := range []int{local} {
		fmt.Println(i)
	}
	if v, ok := map[string]int{}["k"]; ok {
		fmt.Println(v)
	}
}
`,
			},
			target:  "a.go",
			wantNil: true,
		},
		{
			name: "builtins and universe names are never flagged",
			files: map[string]string{
				"a.go": "package p\n\nfunc Use() {\n\tvar e error = nil\n\tvar a any = 1\n\tn := len([]int{1})\n\t_ = append([]int{}, n)\n\tprintln(e, a, nil, true, false)\n}\n",
			},
			target:  "a.go",
			wantNil: true,
		},
		{
			name: "selector parts are not judged",
			files: map[string]string{
				"a.go": "package p\n\nimport \"strings\"\n\nfunc Use() string { return strings.TrimSpace(someReceiver().Field) }\n",
			},
			target:  "a.go",
			wantNil: true, // someReceiver is undefined, but as a selector base it is the model's build to name; no noise
		},
		{
			name: "a file with a dot import gets no undefined report",
			files: map[string]string{
				"a.go": "package p\n\nimport . \"fmt\"\n\nfunc Use() { Println(madeUpName()) }\n",
			},
			target:  "a.go",
			wantNil: true, // madeUpName could be dot-imported: silent
		},
		{
			name: "methods with the same name in different files do not collide",
			files: map[string]string{
				"a.go": "package p\n\ntype A struct{}\n\nfunc (A) Do() {}\n",
				"b.go": "package p\n\ntype B struct{}\n\nfunc (B) Do() {}\n",
			},
			target:  "b.go",
			wantNil: true,
		},
		{
			name: "duplicate type declared twice in the SAME file",
			files: map[string]string{
				"a.go": "package p\n\ntype T struct{}\n\ntype T struct{}\n",
			},
			target: "a.go",
			want:   []string{"type T is declared 2 times"},
		},
		{
			name: "both classes in one note",
			files: map[string]string{
				"a.go":      "package p\n\nconst dup = 1\n",
				"b_test.go": "package p\n\nconst dup = 2\n\nfunc TestB(t int) { missingHelper() }\n",
			},
			target: "b_test.go",
			want: []string{
				"2 name problem(s)",
				"const dup is declared 2 times",
				`"missingHelper" is undefined`,
			},
		},
		{
			name: "a different package's file in the same dir is not scanned",
			files: map[string]string{
				"a.go": "package p\n\nfunc onlyInP() {}\n",
				"q.go": "package q\n\nfunc onlyInP() {}\n", // same name, different package clause: legal
			},
			target:  "a.go",
			wantNil: true,
		},
		{
			// The untrusted / hook-off paths are "silent no-ops" BY
			// CONSTRUCTION: writeSanityNote has no trust or mode gate at all —
			// it is never consulted for anything but a .go extension. With no
			// applicable format command and an untrusted-looking workspace the
			// note's absence/presence must depend ONLY on the file content:
			// a clean package stays byte-identical, a broken one still gets
			// its note (the check guards nobody's trust, it helps everybody).
			name: "trust and mode are irrelevant — silence depends only on content",
			files: map[string]string{
				"a.go": "package p\n\nfunc Clean() {}\n",
			},
			target:  "a.go",
			wantNil: true,
		},
		{
			// A sibling file that fails to parse must not crash or flood the
			// check: it is dropped from the package set, and the written file
			// (clean on its own terms except for a name the broken sibling
			// declared — the documented conservative boundary) reports at
			// worst that one name, never a cascade.
			name: "an unparseable sibling is dropped, the check still runs",
			files: map[string]string{
				"broken.go": "package p\n\nfunc Broken( {\n",
				"a.go":      "package p\n\nfunc Use() { helper() }\n",
			},
			target: "a.go",
			want:   []string{`"helper" is undefined`}, // declared only in the broken file: the accepted boundary
		},
		{
			// The same broken sibling must not make a name that exists in a
			// HEALTHY file look undefined.
			name: "an unparseable sibling does not mask healthy definitions",
			files: map[string]string{
				"broken.go": "package p\n\nfunc Broken( {\n",
				"good.go":   "package p\n\nfunc helper() {}\n",
				"a.go":      "package p\n\nfunc Use() { helper() }\n",
			},
			target:  "a.go",
			wantNil: true,
		},
		{
			// Import qualifiers are exempt: a file importing "fmt" and
			// calling fmt.Println must be silent even though fmt is declared
			// nowhere here (selector sides are never judged, and the path
			// element is whitelisted for bare use).
			name: "import qualifiers never look undefined",
			files: map[string]string{
				"a.go": "package p\n\nimport (\n\t\"fmt\"\n\trenamed \"strings/other\"\n)\n\nfunc Use() {\n\tfmt.Println(renamed.X)\n\tvar _ = fmt.Sprint\n}\n",
			},
			target:  "a.go",
			wantNil: true,
		},
		{
			// A named (aliased) import's alias is a declaration too.
			name: "a named import alias is not undefined",
			files: map[string]string{
				"a.go": "package p\n\nimport f \"fmt\"\n\nfunc Use() { f.Println(1) }\n",
			},
			target:  "a.go",
			wantNil: true,
		},
		{
			// Blank imports declare nothing usable but must not flag anything.
			name: "a blank import is silent",
			files: map[string]string{
				"a.go": "package p\n\nimport _ \"embed\"\n\nfunc Use() {}\n",
			},
			target:  "a.go",
			wantNil: true,
		},
		{
			// Named return results used bare after signature must not be
			// flagged (Field names include results).
			name: "named results are declarations not references",
			files: map[string]string{
				"a.go": "package p\n\nfunc Use() (n int, err error) {\n\tn = 1\n\treturn n, err\n}\n",
			},
			target:  "a.go",
			wantNil: true,
		},
		{
			// Labels (continue/break targets) are declarations.
			name: "loop labels are not undefined",
			files: map[string]string{
				"a.go": "package p\n\nfunc Use() {\nouter:\n\tfor i := 0; i < 2; i++ {\n\t\tfor j := 0; j < 2; j++ {\n\t\t\t_ = i + j\n\t\t\tcontinue outer\n\t\t}\n\t}\n}\n",
			},
			target:  "a.go",
			wantNil: true,
		},
		{
			// A type-switch bind and a range pair are local declarations.
			name: "type-switch and range bindings are not undefined",
			files: map[string]string{
				"a.go": "package p\n\nfunc Use(v any, m map[string]int) {\n\tswitch x := v.(type) {\n\tcase int:\n\t\t_ = x\n\t}\n\tfor k, val := range m {\n\t\t_ = k + val\n\t}\n}\n",
			},
			target:  "a.go",
			wantNil: true,
		},
		{
			// Struct-literal field names on a locally declared struct.
			name: "composite literal fields of a local struct are not undefined",
			files: map[string]string{
				"a.go": "package p\n\ntype P struct{ A, B int }\n\nfunc Use() int {\n\tx := P{A: 1, B: 2}\n\treturn x.A + x.B\n}\n",
			},
			target:  "a.go",
			wantNil: true,
		},
		{
			// THE CLASS, not just the one instance: a keyed composite literal's
			// key resolves in the literal TYPE's field set, which this check
			// cannot see — so a key on an imported type, on a type from a
			// sibling file, or on an embedded/promoted field is never a bare
			// reference. Reporting `"Timeout" is undefined` for
			// `&http.Client{Timeout: d}` is noise-on-clean in code that appears
			// in every repo.
			name: "keyed composite literal keys on an imported type are not undefined",
			files: map[string]string{
				"a.go": `package p

import (
	"net/http"
	"os/exec"
	"time"
)

func Use() *http.Client {
	srv := &http.Server{Addr: ":0", ReadHeaderTimeout: time.Second}
	_ = srv
	cmd := exec.Cmd{Path: "/bin/true", Args: []string{"true"}}
	_ = cmd
	return &http.Client{Timeout: 5 * time.Second}
}
`,
			},
			target:  "a.go",
			wantNil: true,
		},
		{
			// …and on a type declared in a SIBLING file of the same package,
			// which the written file's own declarations cannot account for.
			name: "composite literal keys of a sibling file's struct are not undefined",
			files: map[string]string{
				"types.go": "package p\n\ntype P struct {\n\tA int\n\tB string\n}\n",
				"a.go":     "package p\n\nfunc Use() int {\n\tx := P{A: 1, B: \"two\"}\n\treturn x.A\n}\n",
			},
			target:  "a.go",
			wantNil: true,
		},
		{
			// An embedded field is keyed by its TYPE name and reachable through
			// its promoted field; neither name is declared in the written file.
			name: "keys for embedded and promoted fields are not undefined",
			files: map[string]string{
				"types.go": "package p\n\ntype Base struct{ ID string }\n\ntype Doc struct {\n\tBase\n\tTitle string\n}\n",
				"a.go":     "package p\n\nfunc Use() string {\n\td := Doc{Base: Base{ID: \"x\"}, Title: \"t\"}\n\treturn d.ID\n}\n",
			},
			target:  "a.go",
			wantNil: true,
		},
		{
			// The exemption is for KEYS only: a guessed TYPE is precisely the
			// name class this check exists to catch, and a keyed literal must
			// not hide it.
			name: "a guessed struct literal type is still reported undefined",
			files: map[string]string{
				"a.go": "package p\n\nfunc Use() { _ = MadeUpType{Field: 1} }\n",
			},
			target: "a.go",
			want:   []string{`"MadeUpType" is undefined`},
		},
		{
			// A build-constrained sibling pair declaring the SAME package-level
			// name is legal Go, and this repo is full of them (fslock's
			// flock_unix.go / flock_windows.go, lineedit's termios_real.go /
			// termios_stub.go). Merging them into one package tells the model to
			// "reuse one declaration or rename the new one" — break working
			// platform code.
			name: "mutually exclusive //go:build twins are not a duplicate declaration",
			files: map[string]string{
				"flock_unix.go":    "//go:build !windows\n\npackage p\n\nfunc flock() error { return nil }\n",
				"flock_windows.go": "//go:build windows\n\npackage p\n\nfunc flock() error { return nil }\n",
				"p.go":             "package p\n\nfunc Use() error { return flock() }\n",
			},
			target:  "p.go",
			wantNil: true,
		},
		{
			// The same pair named by the GOOS filename-suffix convention instead
			// of a constraint line.
			name: "GOOS-suffixed twins are not a duplicate declaration",
			files: map[string]string{
				"get_unix.go":    "package p\n\nfunc getTermios() {}\n",
				"get_windows.go": "package p\n\nfunc getTermios() {}\n",
				"p.go":           "package p\n\nfunc Use() { getTermios() }\n",
			},
			target:  "p.go",
			wantNil: true,
		},
		{
			// The mirror of the two above, and the one that matters more: the
			// sibling that DOES build here still contributes its names, so a
			// helper it declares is not "undefined" — the filter removes only
			// what never compiles alongside this file.
			name: "the build-constrained sibling that builds here still counts",
			files: map[string]string{
				"helper_unix.go":    "//go:build " + notWindowsTag + "\n\npackage p\n\nfunc helper() string { return \"u\" }\n",
				"helper_windows.go": "//go:build windows\n\npackage p\n\nfunc helper() string { return \"w\" }\n",
				"p.go":              "package p\n\nfunc Use() string { return helper() }\n",
			},
			target:  "p.go",
			wantNil: true,
		},
		{
			// …and a genuine collision with the sibling that DOES build is still
			// reported: constraint filtering must not turn into blanket silence
			// for every package that uses build tags.
			name: "a duplicate against the sibling that builds here is still reported",
			files: map[string]string{
				"helper_unix.go":    "//go:build " + notWindowsTag + "\n\npackage p\n\nfunc helper() string { return \"u\" }\n",
				"helper_windows.go": "//go:build windows\n\npackage p\n\nfunc helper() string { return \"w\" }\n",
				"p.go":              "package p\n\nfunc helper() string { return \"p\" }\n",
			},
			target: "p.go",
			want:   []string{"func helper is declared 2 times"},
		},
		{
			// Methods on a type in a SIBLING file: the method name never
			// enters the package name set and the selector side is exempt.
			name: "calling a sibling file's method is not undefined",
			files: map[string]string{
				"a.go": "package p\n\ntype T struct{}\n",
				"b.go": "package p\n\nfunc (T) Do() int { return 1 }\n",
			},
			target:  "b.go",
			wantNil: true,
		},
		{
			// A const declared in one non-test file and used in a test file:
			// #224's locate-the-helper class, the positive direction.
			name: "a package const used from a test file is not undefined",
			files: map[string]string{
				"a.go":      "package p\n\nconst maxRetries = 3\n",
				"a_test.go": "package p\n\nfunc TestA() int { return maxRetries }\n",
			},
			target:  "a_test.go",
			wantNil: true,
		},
		{
			// The note rides under the model-visible path, never an absolute
			// one (writeSanityNote is handed displayName separately).
			name: "the note names the display path not an absolute path",
			files: map[string]string{
				"a.go": "package p\n\nfunc Use() { nope() }\n",
			},
			target: "a.go",
			want:   []string{"note: a.go:"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeGoFiles(t, tt.files)
			target := filepath.Join(dir, tt.target)
			note := writeSanityNote(target, tt.target)
			if tt.wantNil {
				if note != "" {
					t.Errorf("expected silence, got note:\n%s", note)
				}
				return
			}
			if note == "" {
				t.Fatalf("expected a note containing %q, got none", tt.want)
			}
			for _, w := range tt.want {
				if !strings.Contains(note, w) {
					t.Errorf("note missing %q; note was:\n%s", w, note)
				}
			}
		})
	}
}

// TestWriteSanityNoteProblemCountCountsEveryName pins the issue's core
// promise: one note lists EVERY undefined name (and every collision), so a
// session fixes them in one pass — not one per build round.
func TestWriteSanityNoteProblemCountCountsEveryName(t *testing.T) {
	dir := writeGoFiles(t, map[string]string{
		"a_test.go": "package p\n\nimport \"fmt\"\n\nfunc TestA(t int) { fmt.Println(a1, a2, a3, a4) }\n",
	})
	note := writeSanityNote(filepath.Join(dir, "a_test.go"), "a_test.go")
	if !strings.Contains(note, "4 name problem(s)") {
		t.Errorf("expected the note to count all 4 problems at once; note was:\n%s", note)
	}
	for _, name := range []string{"a1", "a2", "a3", "a4"} {
		if !strings.Contains(note, `"`+name+`" is undefined`) {
			t.Errorf("note missing %q; note was:\n%s", name, note)
		}
	}
}

// TestWriteSanityNoteNeverFailsTheWrite is the contract at the tool layer:
// a .go file with undefined references still WRITES (no error) and the note
// rides appended to the result; the note never replaces or vetoes it.
func TestWriteSanityNoteNeverFailsTheWrite(t *testing.T) {
	dir := writeGoFiles(t, map[string]string{
		"keep.go": "package p\n",
	})
	target := filepath.Join(dir, "bad_guess.go")
	out, _, err := Execute(context.Background(), ToolCall{Function: FunctionCall{
		Name:      FunctionWriteFile,
		Arguments: `{"path":"` + target + `","content":"package p\n\nfunc Test() { helperThatDoesNotExist() }\n"}`,
	}}, headlessDeps{})
	if err != nil {
		t.Fatalf("the sanity note must never fail the write, got error: %v", err)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("the file must exist on disk despite the note: %v", statErr)
	}
	if !strings.Contains(out, "wrote ") {
		t.Errorf("result must keep the tool's own summary, got: %q", out)
	}
	if !strings.Contains(out, `"helperThatDoesNotExist" is undefined`) {
		t.Errorf("result must carry the write-sanity note, got: %q", out)
	}
	if first := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]; !strings.HasPrefix(first, "wrote ") {
		t.Errorf("the note must ride AFTER the write summary, got: %q", out)
	}
}

// TestEditFileCarriesWriteSanityNote is the edit_file side of the same
// contract: an edit that lands a guessed identifier in a .go file gets the
// one-pass note, and the edit still applies.
func TestEditFileCarriesWriteSanityNote(t *testing.T) {
	dir := writeGoFiles(t, map[string]string{
		"a.go": "package p\n\nfunc Use() { realHelper() }\n",
	})
	target := filepath.Join(dir, "a.go")
	out, _, err := Execute(context.Background(), ToolCall{Function: FunctionCall{
		Name:      FunctionEditFile,
		Arguments: `{"path":"` + target + `","old_string":"realHelper()","new_string":"guessedHelper()"}`,
	}}, headlessDeps{})
	if err != nil {
		t.Fatalf("the sanity note must never fail the edit, got error: %v", err)
	}
	if !strings.Contains(out, "edited") {
		t.Errorf("result must keep the edit summary, got: %q", out)
	}
	if !strings.Contains(out, `"guessedHelper" is undefined`) {
		t.Errorf("result must carry the write-sanity note, got: %q", out)
	}
}
