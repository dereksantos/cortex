package niah

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
)

// TestMain builds the cortex binary once for the whole package and
// publishes its path via $CORTEX_BINARY so the Run-based tests can
// exec it. Building once amortizes ~1s across all integration tests.
//
// If $CORTEX_BINARY is already set (e.g. CI pinned a release artifact),
// we honor it and skip the build.
func TestMain(m *testing.M) {
	var cleanup func()
	if os.Getenv("CORTEX_BINARY") == "" {
		bin, err := benchmarks.CompileBinary(niahRepoRoot())
		if err != nil {
			fmt.Fprintf(os.Stderr, "niah test setup: build cortex: %v\n", err)
			os.Exit(1)
		}
		os.Setenv("CORTEX_BINARY", bin)
		cleanup = func() { os.RemoveAll(filepath.Dir(bin)) }
	}
	code := m.Run()
	if cleanup != nil {
		cleanup()
	}
	os.Exit(code)
}

// niahRepoRoot walks up from this test file (under
// internal/eval/benchmarks/niah/) to the module root.
func niahRepoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	// .../internal/eval/benchmarks/niah/main_test.go → repo root is 5 up
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
}
