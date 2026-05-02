package eval

import (
	"fmt"
	"go/ast"
	"strings"
)

// testShape is the small set of axes we compare across resource test files.
// Kept intentionally coarse — finer-grained signals (mock setup, fixture
// reuse, sub-test naming) belong in a follow-up if these three don't
// discriminate well enough on real session output.
type testShape struct {
	HasSharedSetup bool
	TableDriven    bool
	AssertCalls    map[string]int
}

// testParity returns the mean per-file parity score of otherTestFiles against
// s1TestFile. Each file is compared on three axes:
//
//   - shared setup helper (e.g., setupTest, newTestServer) present in both?
//   - table-driven structure (range loop containing t.Run) present in both?
//   - same dominant assertion call (the call appearing most often in the file)?
//
// Per-file score = matched_axes / 3. Result = mean across otherTestFiles.
func testParity(s1TestFile string, otherTestFiles []string) (float64, error) {
	if len(otherTestFiles) == 0 {
		return 1.0, nil
	}
	s1Shape, err := extractTestShape(s1TestFile)
	if err != nil {
		return 0, fmt.Errorf("extract s1 test shape: %w", err)
	}
	var sum float64
	for _, f := range otherTestFiles {
		shape, err := extractTestShape(f)
		if err != nil {
			return 0, fmt.Errorf("extract test shape %s: %w", f, err)
		}
		sum += compareTestShape(s1Shape, shape)
	}
	return sum / float64(len(otherTestFiles)), nil
}

func extractTestShape(path string) (*testShape, error) {
	file, _, err := parseGoFile(path)
	if err != nil {
		return nil, err
	}
	shape := &testShape{AssertCalls: make(map[string]int)}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if !strings.HasPrefix(fn.Name.Name, "Test") && isSetupName(fn.Name.Name) {
			shape.HasSharedSetup = true
		}
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			qual := qualifiedCallName(x.Fun)
			if isAssertionCall(qual) {
				shape.AssertCalls[qual]++
			}
		case *ast.RangeStmt:
			ast.Inspect(x.Body, func(c ast.Node) bool {
				call, ok := c.(*ast.CallExpr)
				if !ok {
					return true
				}
				if qualifiedCallName(call.Fun) == "t.Run" {
					shape.TableDriven = true
				}
				return true
			})
		}
		return true
	})
	return shape, nil
}

func compareTestShape(a, b *testShape) float64 {
	matched := 0
	if a.HasSharedSetup == b.HasSharedSetup {
		matched++
	}
	if a.TableDriven == b.TableDriven {
		matched++
	}
	if dominantAssertion(a.AssertCalls) == dominantAssertion(b.AssertCalls) {
		matched++
	}
	return float64(matched) / 3.0
}

func dominantAssertion(calls map[string]int) string {
	var best string
	maxCount := 0
	for k, v := range calls {
		if v > maxCount {
			maxCount = v
			best = k
		}
	}
	return best
}

func isSetupName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "setup") ||
		strings.Contains(lower, "teardown") ||
		strings.HasPrefix(lower, "newtest") ||
		strings.HasPrefix(lower, "maketest") ||
		strings.HasPrefix(lower, "newserver") ||
		strings.HasPrefix(lower, "newdb")
}

func isAssertionCall(qual string) bool {
	switch qual {
	case "t.Errorf", "t.Fatalf", "t.Error", "t.Fatal":
		return true
	}
	if strings.HasPrefix(qual, "require.") || strings.HasPrefix(qual, "assert.") {
		return true
	}
	return false
}
