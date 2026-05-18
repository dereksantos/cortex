// Package ops — value.detect_unfamiliarity.
//
// AST-based detector for the "model imports X but doesn't call X"
// bleed pattern observed in the third-arm ABR prototype: small models
// often import the right library as a token gesture, then fall back
// to a different library's API in the function body. This op walks
// the model's Go output and reports unused imports as evidence the
// model lacks API depth and would benefit from a fetched example.
//
// Output shape feeds remember.fetch_external — each (package,
// missing-symbols) tuple becomes a fetch request.
//
// V0 scope: Go only (matches the project + the prototype scenario).
// Other languages can be wired by adding language-specific parsers
// behind the same input/output contract; the dispatch lives in
// NewDetectUnfamiliarityHandler's switch.
package ops

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"strings"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// DetectUnfamiliarityConfig is the registration shape. V0 has no
// dependencies — the detector is pure AST. Future iterations may
// add an LLM cross-check for low-confidence cases.
type DetectUnfamiliarityConfig struct{}

// UnfamiliarityFinding is one detected gap: an imported package
// whose identifiers don't appear in the code body (= the model
// imported it but isn't using it). MissingSymbols stays empty in V0
// — distinguishing "imported but not called" from "imported and
// called with the wrong symbol" needs a deeper analysis pass.
type UnfamiliarityFinding struct {
	Package        string   `json:"package"`         // import path, e.g. "github.com/jmoiron/sqlx"
	ImportedAs     string   `json:"imported_as"`     // local name (last segment or aliased)
	MissingSymbols []string `json:"missing_symbols"` // future: surface specific symbols
}

// DetectUnfamiliaritySpec returns the NodeSpec for
// value.detect_unfamiliarity.
func DetectUnfamiliaritySpec(cfg DetectUnfamiliarityConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncValue,
		Op:          "detect_unfamiliarity",
		Description: "AST: detect imports the code body never calls (bleed pattern)",
		Inputs: []dag.ParamSpec{
			{Name: "code", Type: "string", Required: true},
			{Name: "language", Type: "string"},
		},
		Outputs: []dag.ParamSpec{
			{Name: "findings", Type: "[]UnfamiliarityFinding"},
			{Name: "language", Type: "string"},
		},
		Cost:    dag.Cost{LatencyMS: 50, Tokens: 0},
		Handler: NewDetectUnfamiliarityHandler(cfg),
	}
}

// NewDetectUnfamiliarityHandler returns the dag.Handler. Mechanical
// (no LLM); cost is fixed at ~50ms.
func NewDetectUnfamiliarityHandler(cfg DetectUnfamiliarityConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
		code := readString(in, "code")
		if code == "" {
			return dag.NodeResult{
				Out:          map[string]any{"findings": []UnfamiliarityFinding{}, "language": "go"},
				CostConsumed: dag.Cost{LatencyMS: 1, Tokens: 0},
			}, nil
		}
		language := readString(in, "language")
		if language == "" {
			language = "go"
		}

		var findings []UnfamiliarityFinding
		var err error
		switch language {
		case "go":
			findings, err = detectUnfamiliarityGo(code)
		default:
			err = fmt.Errorf("value.detect_unfamiliarity: language %q not implemented (Go only in V0)", language)
		}
		if err != nil {
			return dag.NodeResult{
				Out:          map[string]any{"findings": []UnfamiliarityFinding{}, "language": language, "error": err.Error()},
				CostConsumed: dag.Cost{LatencyMS: 50, Tokens: 0},
			}, nil
		}
		return dag.NodeResult{
			Out: map[string]any{
				"findings": findings,
				"language": language,
			},
			CostConsumed: dag.Cost{LatencyMS: 50, Tokens: 0},
		}, nil
	}
}

// detectUnfamiliarityGo parses Go source and returns imports that
// have no corresponding identifier usage in the body. Uses
// parser.SkipObjectResolution so malformed code still produces
// partial findings. Tolerant: a parse error returns an empty result
// with no panic.
func detectUnfamiliarityGo(code string) ([]UnfamiliarityFinding, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "snippet.go", code, parser.ImportsOnly|parser.SkipObjectResolution)
	if err != nil {
		// First pass only needs imports; if that fails we have nothing.
		return nil, nil
	}

	// Collect (importPath, localName) for each import.
	type importEntry struct {
		path  string
		local string
	}
	var imports []importEntry
	for _, im := range f.Imports {
		p := strings.Trim(im.Path.Value, `"`)
		local := path.Base(p)
		if im.Name != nil && im.Name.Name != "" {
			local = im.Name.Name
		}
		if local == "_" || local == "." {
			// Blank/dot imports are not "the body references this"
			// candidates — skip.
			continue
		}
		imports = append(imports, importEntry{path: p, local: local})
	}
	if len(imports) == 0 {
		return nil, nil
	}

	// Second pass: full parse so we can walk identifiers in the body.
	// Tolerate errors — partial AST still gives useful identifier
	// usage info.
	full, _ := parser.ParseFile(fset, "snippet.go", code, parser.SkipObjectResolution)
	used := map[string]bool{}
	if full != nil {
		ast.Inspect(full, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); ok {
				used[ident.Name] = true
			}
			return true
		})
	}

	var findings []UnfamiliarityFinding
	for _, im := range imports {
		if used[im.local] {
			continue
		}
		findings = append(findings, UnfamiliarityFinding{
			Package:    im.path,
			ImportedAs: im.local,
		})
	}
	return findings, nil
}

