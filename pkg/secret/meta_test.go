package secret

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoRealSecurityBinaryInvokedByTests is the M1.6 meta-test (GOAL.md
// §6): it fails verify if any _test.go file anywhere in the repo passes
// the string literal "security" as an argument to exec.Command or
// exec.CommandContext. Real keychain access must stay behind the Store
// interface defined in this package (faked via Fake in tests) — a test
// that shells out to /usr/bin/security directly would prompt for an
// unlock on an operator's machine and always fail in CI.
func TestNoRealSecurityBinaryInvokedByTests(t *testing.T) {
	root := repoRoot(t)

	var violations []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		found, ferr := fileInvokesSecurityBinary(path)
		if ferr != nil {
			return ferr
		}
		if found {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			violations = append(violations, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking repo for _test.go files: %v", err)
	}
	if len(violations) > 0 {
		t.Errorf("test file(s) pass the literal \"security\" as an exec.Command/exec.CommandContext "+
			"argument — real keychain access must go through pkg/secret.Store, faked via pkg/secret.Fake "+
			"in tests (GOAL.md M1.6): %v", violations)
	}
}

// fileInvokesSecurityBinary reports whether the Go source file at path
// contains a call to exec.Command or exec.CommandContext with a string
// literal argument (anywhere in the argument list) whose unquoted value
// is exactly "security". Parsing ignores build tags, so this also
// catches violations behind a //go:build constraint that wouldn't
// compile on the host platform.
func fileInvokesSecurityBinary(path string) (bool, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return false, err
	}

	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "exec" {
			return true
		}
		if sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext" {
			return true
		}
		for _, arg := range call.Args {
			lit, ok := arg.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			v, uqErr := strconv.Unquote(lit.Value)
			if uqErr != nil {
				continue
			}
			if v == "security" {
				found = true
			}
		}
		return true
	})
	return found, nil
}

// TestFileInvokesSecurityBinaryDetectsLiteral pins the detector logic
// itself against a synthetic fixture, independent of whether any real
// _test.go in the repo currently trips it.
func TestFileInvokesSecurityBinaryDetectsLiteral(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "direct literal to exec.Command",
			src: `package fixture
import "os/exec"
func f() { exec.Command("security", "find-generic-password") }
`,
			want: true,
		},
		{
			name: "literal as later argument",
			src: `package fixture
import "os/exec"
func f() { exec.Command("/bin/sh", "-c", "security") }
`,
			want: true,
		},
		{
			name: "CommandContext variant",
			src: `package fixture
import ("context"; "os/exec")
func f(ctx context.Context) { exec.CommandContext(ctx, "security") }
`,
			want: true,
		},
		{
			name: "unrelated string literal, not an exec argument",
			src: `package fixture
var tags = []string{"auth", "security"}
`,
			want: false,
		},
		{
			name: "exec call via a variable, not a literal",
			src: `package fixture
import "os/exec"
const bin = "security"
func f() { exec.Command(bin) }
`,
			want: false,
		},
		{
			name: "no exec import at all",
			src: `package fixture
func f() string { return "security" }
`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "fixture_test.go")
			if err := os.WriteFile(path, []byte(tt.src), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			got, err := fileInvokesSecurityBinary(path)
			if err != nil {
				t.Fatalf("fileInvokesSecurityBinary: %v", err)
			}
			if got != tt.want {
				t.Errorf("fileInvokesSecurityBinary(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// repoRoot walks up from the current package directory to the nearest
// ancestor containing go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found walking up from %s", dir)
		}
		dir = parent
	}
}
