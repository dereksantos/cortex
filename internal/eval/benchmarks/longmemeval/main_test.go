//go:build !windows

package longmemeval

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
)

// TestMain builds the cortex binary once per package and publishes its
// path via $CORTEX_BINARY for the integration test(s). Building once
// amortizes ~1s across all tests.
//
// If $CORTEX_BINARY is already set, we honor it and skip the build.
func TestMain(m *testing.M) {
	var cleanup func()
	if os.Getenv("CORTEX_BINARY") == "" {
		bin, err := benchmarks.CompileBinary(longmemevalRepoRoot())
		if err != nil {
			fmt.Fprintf(os.Stderr, "longmemeval test setup: build cortex: %v\n", err)
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

func longmemevalRepoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	// .../internal/eval/benchmarks/longmemeval/main_test.go → 5 up
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
}
