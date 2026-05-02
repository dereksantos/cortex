package eval

import (
	"fmt"
	"go/ast"
	"go/token"
)

// smellDensity walks the given .go files and returns a per-100-LOC weighted
// smell score. Lower is better. Smells counted per function:
//
//   - cyclomatic complexity above 10  — each excess point = 1 smell
//   - function length above 50 lines  — each excess line  = 0.1 smell
//   - max nesting depth above 4       — each excess level = 1 smell
//   - magic-number/string count       — literals not in a const block, excluding
//     trivial values (0, 1, -1, "", 0.0). Integer/float = 1.0 each; strings
//     weighted 0.1 because handler code is dense in non-smelly literals (header
//     names, error messages, format strings)
//
// LOC is taken from each file's last-line position (a coarse but consistent
// proxy that doesn't require re-tokenizing for blank-line stripping).
func smellDensity(files []string) (float64, error) {
	if len(files) == 0 {
		return 0, nil
	}
	var totalSmells float64
	var totalLOC int
	for _, path := range files {
		file, fset, err := parseGoFile(path)
		if err != nil {
			return 0, fmt.Errorf("parse %s: %w", path, err)
		}
		totalLOC += fset.Position(file.End()).Line
		constLits := collectConstLiterals(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if c := cyclomatic(fn.Body); c > 10 {
				totalSmells += float64(c - 10)
			}
			length := fset.Position(fn.Body.End()).Line - fset.Position(fn.Body.Pos()).Line
			if length > 50 {
				totalSmells += float64(length-50) * 0.1
			}
			if d := maxNesting(fn.Body); d > 4 {
				totalSmells += float64(d - 4)
			}
			totalSmells += magicLiterals(fn.Body, constLits)
		}
	}
	if totalLOC == 0 {
		return 0, nil
	}
	return totalSmells / (float64(totalLOC) / 100.0), nil
}

func cyclomatic(body *ast.BlockStmt) int {
	c := 1
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.CaseClause, *ast.CommClause:
			c++
		case *ast.BinaryExpr:
			if x.Op == token.LAND || x.Op == token.LOR {
				c++
			}
		}
		return true
	})
	return c
}

// maxNesting walks the AST and returns the deepest count of nested control
// structures (if/for/range/switch/select). Switch and select count once at
// the switch level — case clauses don't add another level on top of that.
func maxNesting(n ast.Node) int {
	var walk func(n ast.Node, depth int) int
	walk = func(n ast.Node, depth int) int {
		if n == nil {
			return depth
		}
		max := depth
		try := func(child ast.Node, d int) {
			if c := walk(child, d); c > max {
				max = c
			}
		}
		switch s := n.(type) {
		case *ast.IfStmt:
			d := depth + 1
			if d > max {
				max = d
			}
			try(s.Body, d)
			try(s.Else, d)
		case *ast.ForStmt:
			d := depth + 1
			if d > max {
				max = d
			}
			try(s.Body, d)
		case *ast.RangeStmt:
			d := depth + 1
			if d > max {
				max = d
			}
			try(s.Body, d)
		case *ast.SwitchStmt:
			d := depth + 1
			if d > max {
				max = d
			}
			try(s.Body, d)
		case *ast.TypeSwitchStmt:
			d := depth + 1
			if d > max {
				max = d
			}
			try(s.Body, d)
		case *ast.SelectStmt:
			d := depth + 1
			if d > max {
				max = d
			}
			try(s.Body, d)
		case *ast.BlockStmt:
			for _, st := range s.List {
				try(st, depth)
			}
		case *ast.CaseClause:
			for _, st := range s.Body {
				try(st, depth)
			}
		case *ast.CommClause:
			for _, st := range s.Body {
				try(st, depth)
			}
		}
		return max
	}
	return walk(n, 0)
}

func collectConstLiterals(file *ast.File) map[string]bool {
	lits := make(map[string]bool)
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, val := range vs.Values {
				if lit, ok := val.(*ast.BasicLit); ok {
					lits[lit.Value] = true
				}
			}
		}
	}
	return lits
}

func magicLiterals(body *ast.BlockStmt, constLits map[string]bool) float64 {
	var count float64
	ast.Inspect(body, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok {
			return true
		}
		var weight float64
		switch lit.Kind {
		case token.INT, token.FLOAT:
			weight = 1.0
		case token.STRING:
			weight = 0.1
		default:
			return true
		}
		switch lit.Value {
		case "0", "1", "-1", `""`, "0.0":
			return true
		}
		if constLits[lit.Value] {
			return true
		}
		count += weight
		return true
	})
	return count
}
