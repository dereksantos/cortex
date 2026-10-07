package tools

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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
// not be flagged. The tell of an EXECUTED command is that its string literal
// reaches a shell-executing helper: `bashCall` (defined in two packages —
// internal/tools' `bashCall(t, command)` and cmd/cortex'
// `bashCall(id, command)`), `bashCallResp` (a scripted bash tool-call
// reply), or the tool dispatcher `Execute` (tools.Execute / Execute). The
// guard parses each _test.go with go/ast and inspects every string literal
// that is an argument to such a call — whatever the first argument is, and
// whether the command literal wraps onto the next line — while literals that
// only reach a detector (a table, a comment, a local variable) stay clean.
// A bare form in a command table IS flagged when that table's command field
// is what the enclosing loop passes to a shell-executing call: the literal
// then reaches the shell at loop time.

// bareSedIn is a sed -i whose script argument is NOT attached as a suffix:
// `sed -i '...'` or `sed -i -e '...'` (env-prefixed forms like LC_ALL=C
// match too). `sed -i.bak '...'` does not match: the character right after
// the `-i` is the suffix (a letter/digit/`.`/`_`), not whitespace.
var bareSedIn = regexp.MustCompile(`(^|[\s;|&` + "`" + `])([A-Za-z_][A-Za-z0-9_]*=.)*sed\s+-i[\s]`)

// shellExecHelpers are the identifiers of test helpers that EXECUTE the
// command they are given (a bare `sed -i '…'` passed to any of them hits a
// real shell). `bashCall` is matched by identifier because two packages
// define it with different signatures (see the package comment); `Execute`
// is matched as the tool-dispatcher name in both packages (tools.Execute is
// the selector form, the same ident).
var shellExecHelpers = map[string]bool{
	"bashCall":     true,
	"bashCallResp": true,
	"Execute":      true,
}

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
	src := "package guard\n\nfunc body(t *testing.T) {\n"
	for _, tc := range []struct {
		name string
		body string
		want bool // should the guard flag this file?
	}{
		{"executed bare sed -i", "bashCall(t, `" + bare + "'s/x/y/' main.go`)\n", true},
		{"executed bare sed -i -e", "bashCall(t, `" + bare + "-e 's/x/y/' main.go`)\n", true},
		{"executed portable sed -i.bak", "bashCall(t, `" + portable + " 's/x/y/' main.go`)\n", false},
		{"detector table: bare form parsed, never executed", "cmds := []string{`" + bare + "'s/x/y/' f.go`}\n" +
			"for _, cmd := range cmds {\n\tdetectInPlaceRewrites(cmd)\n}\n", false},
		{"detector table with tc.cmd field: commands are only parsed", "cases := []struct {\n\tname string\n\tcmd  string\n} {\n" +
			"{\"bare form\", `" + bare + "'s/x/y/' f.go`},\n}\n" +
			"for _, tc := range cases {\n\tdetectInPlaceRewrites(tc.cmd)\n}\n", false},
		{"table whose tc.cmd IS executed: flagged", "cases := []struct {\n\tname string\n\tcmd  string\n} {\n" +
			"{\"sed note\", `" + bare + "'s/x/y/' f.go && true`},\n}\n" +
			"for _, tc := range cases {\n\tt.Run(tc.name, func(t *testing.T) {\n\t\tbashCall(t, tc.cmd)\n\t})\n}\n", true},
		{"env-prefixed bare sed -i executed", "bashCall(t, `LC_ALL=C " + bare + "'s/x/y/' f.go`)\n", true},
		{"inline range table whose tc.cmd IS executed: flagged",
			"for _, tc := range []struct{ name, cmd string }{{\"x\", `" + bare + "'s/x/y/' f.go`}} {\n" +
				"\tbashCall(t, tc.cmd)\n}\n", true},
		{"inline range table: bare form in a non-cmd column: not flagged",
			"for _, tc := range []struct{ name, cmd string }{{`" + bare + "'s/x/y/' f.go`, `echo hi`}} {\n" +
				"\tbashCall(t, tc.cmd)\n}\n", false},
		{"sed without -i is a read: fine anywhere", "bashCall(t, `sed -n '1,10p' f.go`)\n", false},
		{"cmd/cortex bashCall(id, command): flagged", "bashCall(\"c1\", \"" + bare + "'s/x/y/' f.go\")\n", true},
		{"cmd/cortex bashCallResp(command): flagged", "bashCallResp(`" + bare + "'s/x/y/' f.go`)\n", true},
		{"tools.Execute of a bash call: flagged", "Execute(context.Background(), bashCall(t, `" + bare + "'s/x/y/' f.go`), deps)\n", true},
		{"cmd/cortex Execute of a bash call: flagged", "tools.Execute(context.Background(), bashCall(`" + bare + "'s/x/y/' f.go`), deps)\n", true},
		{"call split across lines: flagged", "bashCall(t,\n\t`" + bare + "'s/x/y/' f.go`)\n", true},
		{"helper called with the same name but no sed: not flagged", "bashCall(t, `echo ok`)\n", false},
		{"a comment mentioning the bare form: not flagged", "bashCall(t, `echo ok`) // `" + bare + "'s/x/y/' f.go`\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := flagsBareSedIn(src + tc.body + "}\n"); got != tc.want {
				t.Errorf("flagsBareSedIn(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestNoExecutedBareSedInInTests walks every *_test.go in the repo and fails
// on any command string that executes a bare `sed -i` (see package comment)
// — the class of failure that only shows up when CI runs on macOS. A walk
// error (an unreadable file or directory mid-walk) fails the test too: a
// guard that did not scan everything must not pass.
func TestNoExecutedBareSedInInTests(t *testing.T) {
	root, err := findRepoRoot(".")
	if err != nil {
		t.Fatalf("finding repo root: %v", err)
	}
	var flagged []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") {
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
// bareSedIn string literal that is an argument to a shell-executing call
// (bashCall / bashCallResp / Execute — see shellExecHelpers), or one that
// lives in a command table whose command field the enclosing loop passes to
// such a call (the literal reaches the shell at loop time). It parses the
// source with go/parser, so wrapped calls, nested calls, and string
// literals inside comments are all decided by the AST rather than line
// heuristics. A detector-only table (whose literals never reach such a
// call) stays clean no matter how large.
func flagsBareSedIn(src string) bool {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "shell_portability_check.go", src, 0)
	if err != nil {
		// The walk only parses files that are part of the build; a parse
		// failure would also fail `go build`, so it cannot be a real test
		// file. A silent pass here would be a false pass, so fail loudly.
		panic(fmt.Sprintf("flagsBareSedIn: unparseable source: %v", err))
	}
	bareLits := []*ast.BasicLit{}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if !bareSedIn.MatchString(lit.Value) && !bareSedIn.MatchString(strings.Trim(lit.Value, "\"`")) {
			return true
		}
		bareLits = append(bareLits, lit)
		return true
	})
	for _, lit := range bareLits {
		for _, call := range execCallsIn(file, file, fset) {
			if inside(lit, call, fset) {
				return true
			}
		}
		if tableCmdIsExecuted(file, fset, lit) {
			return true
		}
	}
	return false
}

// tableCmdIsExecuted reports whether the bareSedIn literal lit lives in a
// command table that reaches a shell at loop time: the table's command field
// is selected by the loop's value variable and passed to a shell-executing
// call in the loop body. The field name is taken from that call
// (`tc.<field>`), not hardcoded, so a table whose command field is called
// `command` is covered the same way. Detector tables (whose command field is
// only parsed) never satisfy the last condition, so they stay clean.
func tableCmdIsExecuted(file *ast.File, fset *token.FileSet, lit *ast.BasicLit) bool {
	ranges := []*ast.RangeStmt{}
	ast.Inspect(file, func(n ast.Node) bool {
		if r, ok := n.(*ast.RangeStmt); ok {
			ranges = append(ranges, r)
		}
		return true
	})
	for _, r := range ranges {
		st := tableStructType(file, r)
		if st == nil {
			continue
		}
		for _, call := range execCallsIn(r.Body, file, fset) {
			if field, ok := passedTableField(call, r); ok {
				if !structHasStringField(st, field) {
					continue
				}
				if litInTable(lit, file, r, st, field, fset) {
					return true
				}
			}
		}
	}
	return false
}

// litInTable reports whether lit sits in the table range loop r iterates and
// is the value of the field named field — the command field the loop passes
// to a shell-executing call. That field may live in the loop's range
// expression (an inline `range []struct{...}`) or in the composite literal
// that initialises the named variable it ranges over.
func litInTable(lit *ast.BasicLit, file *ast.File, r *ast.RangeStmt, st *ast.StructType, field string, fset *token.FileSet) bool {
	// Inline form: `for _, tc := range []struct{...}{...}` — the range
	// expression IS the table, and lit must be in the `field` column of a row
	// (not merely anywhere in the expression).
	if c, ok := r.X.(*ast.CompositeLit); ok {
		if litInCmdField(lit, c, st, field, fset) {
			return true
		}
	}
	// Named-variable form: `for _, tc := range cases` where
	// `cases := []struct{...}{...}`. The lit must be in the `field` field of
	// a row in a composite literal assigned to the range variable in the SAME
	// function as the loop.
	if id, ok := r.X.(*ast.Ident); ok {
		fn := enclosingFunc(r, file, fset)
		for _, c := range allComposites(file) {
			if !inside(lit, c, fset) {
				continue
			}
			if fn == nil || !compositeAssignedTo(c, id.Name, fn) {
				continue
			}
			if litInCmdField(lit, c, st, field, fset) {
				return true
			}
		}
	}
	return false
}

// litInCmdField reports whether lit is the value of the field named field in
// some row of the table composite c (whose struct element type is st). For
// keyed rows the field name must match; for unkeyed rows the lit's position
// in the row must match field's index in st.
func litInCmdField(lit *ast.BasicLit, c *ast.CompositeLit, st *ast.StructType, field string, fset *token.FileSet) bool {
	cmdIdx := structFieldIndex(st, field)
	for i, el := range c.Elts {
		if !inside(lit, el, fset) {
			continue
		}
		// Keyed row: &ast.KeyValueExpr{Key: Ident(field), Value: lit}.
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == field {
				return true
			}
			continue
		}
		// Unkeyed row: each row is itself a composite literal. The lit must
		// be the cmdIdx-th field of the row.
		if row, ok := el.(*ast.CompositeLit); ok {
			for j, f := range row.Elts {
				if !inside(lit, f, fset) {
					continue
				}
				if cmdIdx >= 0 && j == cmdIdx {
					return true
				}
			}
			continue
		}
		// Direct unkeyed value (not a row composite): the lit IS the field.
		if cmdIdx >= 0 && i == cmdIdx {
			return true
		}
	}
	return false
}

// structFields expands struct type st's fields into a flat list, one entry
// per named field: a `struct{ a, b string }` declaration is a single AST
// Field whose Names list carries both `a` and `b`, so it expands to two
// entries. The position of an entry is the field's index in the struct (the
// order used by unkeyed composite-literal rows). Anonymous (embedded) fields
// are skipped — they have no position a table row could fill.
func structFields(st *ast.StructType) []string {
	out := []string{}
	if st == nil || st.Fields == nil {
		return out
	}
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			out = append(out, n.Name)
		}
	}
	return out
}

// structFieldIndex returns the index of the `string` field named field in
// struct type st — counting the expanded field list, so a grouped
// `struct{ a, cmd string }` places cmd at index 1 — or -1 if st has no such
// field (or it is not a string field).
func structFieldIndex(st *ast.StructType, field string) int {
	for i, name := range structFields(st) {
		if name != field {
			continue
		}
		if f := structStringField(st, field); f {
			return i
		}
	}
	return -1
}

// structHasStringField reports whether struct type st has a `string` field
// named field.
func structHasStringField(st *ast.StructType, field string) bool {
	return structFieldIndex(st, field) >= 0
}

// structStringField reports whether the field named field in st is a string
// field.
func structStringField(st *ast.StructType, field string) bool {
	if st == nil || st.Fields == nil {
		return false
	}
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.Name != field {
				continue
			}
			stype, ok := f.Type.(*ast.Ident)
			return ok && stype.Name == "string"
		}
	}
	return false
}

// enclosingFunc returns the *ast.FuncDecl (or *ast.FuncLit) that encloses
// node n, or nil if n is at file scope.
func enclosingFunc(n ast.Node, file *ast.File, fset *token.FileSet) ast.Node {
	var result ast.Node
	ast.Inspect(file, func(node ast.Node) bool {
		if result != nil {
			return false
		}
		switch f := node.(type) {
		case *ast.FuncDecl:
			if inside(n, f.Body, fset) {
				result = f
				return false
			}
		case *ast.FuncLit:
			if inside(n, f.Body, fset) {
				result = f
				return false
			}
		}
		return true
	})
	return result
}

// allComposites returns every composite literal in file.
func allComposites(file *ast.File) []*ast.CompositeLit {
	out := []*ast.CompositeLit{}
	ast.Inspect(file, func(n ast.Node) bool {
		if c, ok := n.(*ast.CompositeLit); ok {
			out = append(out, c)
		}
		return true
	})
	return out
}

// compositeAssignedTo reports whether composite literal c is the value of an
// assignment whose left-hand side is the ident name, within the given scope
// node (a function body). scope may be nil (matches anywhere in the file).
func compositeAssignedTo(c *ast.CompositeLit, name string, scope ast.Node) bool {
	if scope == nil {
		return false
	}
	found := false
	ast.Inspect(scope, func(n ast.Node) bool {
		if found {
			return false
		}
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) == 0 || len(as.Rhs) == 0 {
			return true
		}
		id, ok := as.Lhs[0].(*ast.Ident)
		if !ok || id.Name != name {
			return true
		}
		if as.Rhs[0] == c {
			found = true
		}
		return true
	})
	return found
}

// tableStructType returns the struct element type of the table the range
// loop iterates — for an inline `range []struct{...}{...}` (the range
// expression's composite has an *ast.ArrayType whose Elt is the struct) or a
// named variable whose declaration initialises it with such a composite.
// Returns nil if the loop does not range over a struct table.
func tableStructType(file *ast.File, r *ast.RangeStmt) *ast.StructType {
	if c, ok := r.X.(*ast.CompositeLit); ok {
		if arr, ok := c.Type.(*ast.ArrayType); ok {
			if st, ok := arr.Elt.(*ast.StructType); ok {
				return st
			}
		}
	}
	if id, ok := r.X.(*ast.Ident); ok {
		if st, ok := declaredType(id.Name, file).(*ast.StructType); ok {
			return st
		}
	}
	return nil
}

// declaredType returns the declared type of variable name in file (the
// element type of the slice it was initialised with). Returns nil if the
// variable is not declared in this file as a composite literal.
func declaredType(name string, file *ast.File) ast.Expr {
	var result ast.Expr
	ast.Inspect(file, func(n ast.Node) bool {
		if result != nil {
			return false
		}
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) == 0 || len(as.Rhs) == 0 {
			return true
		}
		id, ok := as.Lhs[0].(*ast.Ident)
		if !ok || id.Name != name {
			return true
		}
		lit, ok := as.Rhs[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		if arr, ok := lit.Type.(*ast.ArrayType); ok {
			result = arr.Elt
		} else {
			result = lit.Type
		}
		return false
	})
	return result
}

// execCallsIn returns the shell-executing calls anywhere inside body —
// directly in its statements and nested inside any function literals (the
// common `t.Run(tc.name, func(t *testing.T) { bashCall(t, tc.cmd) })` shape).
func execCallsIn(body ast.Node, file *ast.File, fset *token.FileSet) []*ast.CallExpr {
	return execCallsInNode(body, fset)
}

// execCallsInNode returns the shell-executing calls inside node n, recursing
// into function literals so calls in `t.Run(_, func() { bashCall(...) })`
// closures are found.
func execCallsInNode(n ast.Node, fset *token.FileSet) []*ast.CallExpr {
	calls := []*ast.CallExpr{}
	ast.Inspect(n, func(x ast.Node) bool {
		switch v := x.(type) {
		case *ast.CallExpr:
			if id, ok := identOf(v.Fun); ok && shellExecHelpers[id.Name] {
				calls = append(calls, v)
			}
		case *ast.FuncLit:
			calls = append(calls, execCallsInNode(v.Body, fset)...)
			return false // don't double-count via the walk above
		}
		return true
	})
	return calls
}

// passedTableField reports the name of the table's command field that call
// selects on the loop's value variable and passes to its helper — `tc.<field>`
// for some argument of call — or ok=false if call passes no such field. The
// field name comes from the call, not a hardcoded "cmd", so a table whose
// command field is named `command` (passed as `tc.command`) is covered too.
func passedTableField(call *ast.CallExpr, r *ast.RangeStmt) (string, bool) {
	valName := ""
	if id, ok := r.Value.(*ast.Ident); ok {
		valName = id.Name
	}
	if valName == "" {
		return "", false
	}
	for _, arg := range call.Args {
		sel, ok := arg.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == valName {
			return sel.Sel.Name, true
		}
	}
	return "", false
}

// inside reports whether node m's position span is inside node n's span.
func inside(m, n ast.Node, fset *token.FileSet) bool {
	return fset.Position(m.Pos()).Offset >= fset.Position(n.Pos()).Offset &&
		fset.Position(m.End()).Offset <= fset.Position(n.End()).Offset
}

// identOf returns the identifier a call expression is invoked through —
// either `bashCall(...)` (an Ident) or `tools.Execute(...)` (a SelectorExpr,
// whose Sel is the ident).
func identOf(fun ast.Expr) (*ast.Ident, bool) {
	switch f := fun.(type) {
	case *ast.Ident:
		return f, true
	case *ast.SelectorExpr:
		return f.Sel, true
	}
	return nil, false
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
