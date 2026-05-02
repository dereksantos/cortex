package eval

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
)

// shapeSimilarity computes the mean pairwise cosine similarity over AST-derived
// feature vectors of the given handler files. This is the headline metric for
// the library-service eval — it answers "do the 5 resource handlers look alike?".
//
// Feature vector keys (per file):
//
//	fn:<name>      handler function names, with the file's resource token
//	               replaced by "x" so ListBooks / ListAuthors collapse to listx
//	sig:<shape>    function signature shape (param types -> return types)
//	err:<call>     qualified error-construction call sites (fmt.Errorf, errors.Wrap, ...)
//	resp:<call>    qualified response-writer call sites (json.NewEncoder, w.Write, ...)
//	status:<const> http.StatusXxx constants used
//	valid:<idiom>  validation idioms detected (empty-string check, validator import)
//
// Resource tokens come from each file's basename via resourceTokensFromPath.
// The cosine is taken pairwise across all C(N,2) combinations and averaged.
//
// 1.0 = every handler structurally identical modulo resource names.
// 0.0 = handlers share no signal at all.
func shapeSimilarity(handlerFiles []string) (float64, error) {
	if len(handlerFiles) < 2 {
		return 0, fmt.Errorf("shapeSimilarity needs at least 2 files, got %d", len(handlerFiles))
	}

	vectors := make([]map[string]int, len(handlerFiles))
	for i, path := range handlerFiles {
		v, err := extractHandlerFeatures(path, resourceTokensFromPath(path))
		if err != nil {
			return 0, fmt.Errorf("extract features %s: %w", path, err)
		}
		vectors[i] = v
	}

	var sum float64
	var pairs int
	for i := 0; i < len(vectors); i++ {
		for j := i + 1; j < len(vectors); j++ {
			sum += cosineSparse(vectors[i], vectors[j])
			pairs++
		}
	}
	if pairs == 0 {
		return 0, nil
	}
	return sum / float64(pairs), nil
}

func extractHandlerFeatures(path string, resourceTokens []string) (map[string]int, error) {
	file, _, err := parseGoFile(path)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	feats := make(map[string]int)

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := strings.ToLower(fn.Name.Name)
		for _, tok := range resourceTokens {
			name = strings.ReplaceAll(name, tok, "x")
		}
		feats["fn:"+name]++
		feats["sig:"+signatureKey(fn.Type)]++
	}

	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(strings.ToLower(path), "validator") {
			feats["valid:validator_import"]++
		}
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			qual := qualifiedCallName(x.Fun)
			if qual == "" {
				return true
			}
			lower := strings.ToLower(qual)
			switch {
			case isErrorCall(qual):
				feats["err:"+lower]++
			case isResponseCall(qual):
				feats["resp:"+lower]++
			}
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && id.Name == "http" {
				if strings.HasPrefix(x.Sel.Name, "Status") {
					feats["status:"+x.Sel.Name]++
				}
			}
		case *ast.BinaryExpr:
			if x.Op == token.EQL || x.Op == token.NEQ {
				if isEmptyStringCheck(x) {
					feats["valid:empty_string_check"]++
				}
			}
		}
		return true
	})

	return feats, nil
}

func signatureKey(ft *ast.FuncType) string {
	var b strings.Builder
	b.WriteByte('(')
	if ft.Params != nil {
		for i, p := range ft.Params.List {
			if i > 0 {
				b.WriteByte(',')
			}
			// Each Field can declare multiple names sharing one type;
			// repeat the type per name so signature shape is comparable.
			n := len(p.Names)
			if n == 0 {
				n = 1
			}
			for j := 0; j < n; j++ {
				if j > 0 {
					b.WriteByte(',')
				}
				b.WriteString(exprToString(p.Type))
			}
		}
	}
	b.WriteByte(')')
	if ft.Results != nil && len(ft.Results.List) > 0 {
		b.WriteString("->")
		for i, r := range ft.Results.List {
			if i > 0 {
				b.WriteByte(',')
			}
			n := len(r.Names)
			if n == 0 {
				n = 1
			}
			for j := 0; j < n; j++ {
				if j > 0 {
					b.WriteByte(',')
				}
				b.WriteString(exprToString(r.Type))
			}
		}
	}
	return b.String()
}

func isErrorCall(qual string) bool {
	switch qual {
	case "fmt.Errorf",
		"errors.New",
		"errors.Wrap",
		"errors.Wrapf",
		"errors.WithMessage",
		"errors.WithStack":
		return true
	}
	return false
}

func isResponseCall(qual string) bool {
	switch qual {
	case "json.NewEncoder", "json.Marshal", "json.MarshalIndent", "http.Error":
		return true
	}
	if strings.HasSuffix(qual, ".Write") ||
		strings.HasSuffix(qual, ".WriteHeader") ||
		strings.HasSuffix(qual, ".Encode") {
		return true
	}
	return false
}

func isEmptyStringCheck(b *ast.BinaryExpr) bool {
	isEmptyStr := func(e ast.Expr) bool {
		l, ok := e.(*ast.BasicLit)
		if !ok || l.Kind != token.STRING {
			return false
		}
		return l.Value == `""`
	}
	return isEmptyStr(b.X) || isEmptyStr(b.Y)
}
