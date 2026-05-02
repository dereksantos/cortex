package eval

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"path/filepath"
	"strings"
)

// LibraryResources is the canonical, ordered list of resource basenames the
// library-service eval expects. Used to discover handler/test files in a
// generated workdir and as token hints for resource-aware normalization.
var LibraryResources = []string{"books", "authors", "loans", "members", "branches"}

// parseGoFile is a small convenience over go/parser. The returned FileSet is
// what callers use to translate ast positions into line numbers.
func parseGoFile(path string) (*ast.File, *token.FileSet, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, err
	}
	return file, fset, nil
}

// resourceTokensFromPath returns lowercased resource tokens to strip from
// identifiers when comparing across resources. Derived from the file's
// basename so that books.go yields ["books", "book"], authors.go yields
// ["authors", "author"], branches.go yields ["branches", "branch"], etc.
//
// Order matters: longer (plural) first, so caseInsensitiveReplace consumes
// the plural before the singular and we don't double-templatize.
func resourceTokensFromPath(path string) []string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.TrimSuffix(base, "_test")
	plural := strings.ToLower(base)
	if plural == "" {
		return nil
	}
	tokens := []string{plural}
	if sing := singularize(plural); sing != plural {
		tokens = append(tokens, sing)
	}
	return tokens
}

// singularize handles the few pluralization rules that show up in this eval's
// resource set (books, authors, loans, members, branches, libraries). Not a
// general-purpose inflector — additions should be made deliberately.
func singularize(plural string) string {
	switch {
	case strings.HasSuffix(plural, "ies"):
		return strings.TrimSuffix(plural, "ies") + "y"
	case strings.HasSuffix(plural, "ches"),
		strings.HasSuffix(plural, "shes"),
		strings.HasSuffix(plural, "xes"),
		strings.HasSuffix(plural, "ses"):
		return strings.TrimSuffix(plural, "es")
	case strings.HasSuffix(plural, "s"):
		return strings.TrimSuffix(plural, "s")
	}
	return plural
}

// caseInsensitiveReplace replaces every case-insensitive occurrence of old in s
// with replacement, preserving the surrounding characters as-is. Used to
// templatize identifiers like "ListBooks" → "List<X>".
func caseInsensitiveReplace(s, old, replacement string) string {
	if old == "" {
		return s
	}
	lower := strings.ToLower(s)
	lowOld := strings.ToLower(old)
	var b strings.Builder
	i := 0
	for i < len(s) {
		idx := strings.Index(lower[i:], lowOld)
		if idx == -1 {
			b.WriteString(s[i:])
			break
		}
		b.WriteString(s[i : i+idx])
		b.WriteString(replacement)
		i += idx + len(old)
	}
	return b.String()
}

// qualifiedCallName returns the "qualified" name of a call expression's
// callee — e.g., "fmt.Errorf", "errors.Wrap", "json.NewEncoder", or just
// "panic" for unqualified identifiers. Returns "" for callees we can't
// statically resolve (call results, type assertions, etc.).
func qualifiedCallName(fun ast.Expr) string {
	switch x := fun.(type) {
	case *ast.SelectorExpr:
		return exprToString(x.X) + "." + x.Sel.Name
	case *ast.Ident:
		return x.Name
	}
	return ""
}

// exprToString flattens the small slice of ast.Expr shapes that appear in
// type positions (params, returns, selector targets) into a stable string
// key. Anything outside that slice degrades to "?" — feature stability is
// more important than perfect fidelity here.
func exprToString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprToString(x.X) + "." + x.Sel.Name
	case *ast.StarExpr:
		return "*" + exprToString(x.X)
	case *ast.ArrayType:
		return "[]" + exprToString(x.Elt)
	case *ast.MapType:
		return "map[" + exprToString(x.Key) + "]" + exprToString(x.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.FuncType:
		return "func"
	case *ast.Ellipsis:
		return "..." + exprToString(x.Elt)
	case *ast.ChanType:
		return "chan " + exprToString(x.Value)
	}
	return "?"
}

// cosineSparse computes cosine similarity over two sparse integer vectors
// indexed by string keys. Returns 0 when either side is empty so absent
// signals don't accidentally read as "perfect match".
func cosineSparse(a, b map[string]int) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	var dot, na, nb float64
	for k, av := range a {
		na += float64(av) * float64(av)
		if bv, ok := b[k]; ok {
			dot += float64(av) * float64(bv)
		}
	}
	for _, bv := range b {
		nb += float64(bv) * float64(bv)
	}
	denom := math.Sqrt(na) * math.Sqrt(nb)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
