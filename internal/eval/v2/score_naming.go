package eval

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
)

// namingAdherence returns the fraction of identifiers in otherFiles whose
// "naming template" exists in s1File's set.
//
// A naming template is the identifier with its file's resource tokens replaced
// by "<X>" — so ListBooks (in books.go) and ListAuthors (in authors.go) both
// templatize to "List<X>" and count as a match. Cross-resource references
// (e.g., LookupBookByLoan in loans.go) won't templatize cleanly and will be
// counted as misses, which is the desired signal: borrowed shapes don't
// inherit the convention.
//
// Tracked categories: top-level functions (handlers + tests collapse here),
// and top-level error vars/consts (anything starting with Err/err or ending
// with Err/err).
//
// Range: 0 (no identifiers match) to 1 (every identifier matches an S1 template).
func namingAdherence(s1File string, otherFiles []string) (float64, error) {
	s1Patterns, err := extractNamePatterns(s1File)
	if err != nil {
		return 0, fmt.Errorf("extract s1 patterns: %w", err)
	}
	if len(s1Patterns) == 0 {
		return 1.0, nil
	}

	var totalMatch, totalIdent int
	for _, f := range otherFiles {
		patterns, err := extractNamePatterns(f)
		if err != nil {
			return 0, fmt.Errorf("extract patterns %s: %w", f, err)
		}
		for p := range patterns {
			totalIdent++
			if s1Patterns[p] {
				totalMatch++
			}
		}
	}
	if totalIdent == 0 {
		return 1.0, nil
	}
	return float64(totalMatch) / float64(totalIdent), nil
}

// extractNamePatterns parses path and returns the set of templatized
// identifier patterns it contributes. Patterns are prefixed with their
// category ("fn:" or "err:") so a function named "ErrFoo" and an err var
// named "ErrFoo" don't collide.
func extractNamePatterns(path string) (map[string]bool, error) {
	file, _, err := parseGoFile(path)
	if err != nil {
		return nil, err
	}
	tokens := resourceTokensFromPath(path)
	patterns := make(map[string]bool)

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			patterns["fn:"+templatize(d.Name.Name, tokens)] = true
		case *ast.GenDecl:
			if d.Tok != token.VAR && d.Tok != token.CONST {
				continue
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, n := range vs.Names {
					if isErrName(n.Name) {
						patterns["err:"+templatize(n.Name, tokens)] = true
					}
				}
			}
		}
	}
	return patterns, nil
}

func templatize(name string, resourceTokens []string) string {
	out := name
	for _, t := range resourceTokens {
		out = caseInsensitiveReplace(out, t, "<X>")
	}
	return out
}

func isErrName(name string) bool {
	return strings.HasPrefix(name, "Err") ||
		strings.HasPrefix(name, "err") ||
		strings.HasSuffix(name, "Err") ||
		strings.HasSuffix(name, "Error")
}
