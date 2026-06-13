package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReferencedFileFloor pins fault-tree item 6: questions that name
// large files get a token-budget floor big enough to read them, while
// questions over small files (or naming nothing real) are unaffected.
func TestReferencedFileFloor(t *testing.T) {
	dir := t.TempDir()
	// A "large" file (~40K tokens) and a "small" one (~1K tokens).
	big := filepath.Join(dir, "pkg", "huge.go")
	if err := os.MkdirAll(filepath.Dir(big), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, make([]byte, 160_000), 0o644); err != nil {
		t.Fatal(err)
	}
	small := filepath.Join(dir, "small.go")
	if err := os.WriteFile(small, make([]byte, 4_000), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		prompt      string
		wantFloorGE int
		wantFloorLT int
		wantSignal  string // substring; "" means expect empty signal
	}{
		{
			name:        "large file named → floor covers it",
			prompt:      "How does pkg/huge.go consume the config?",
			wantFloorGE: scopeFileFloorBase + 40_000,
			wantFloorLT: scopeFileFloorBase + 41_000,
			wantSignal:  "huge.go",
		},
		{
			name:        "small file named → modest floor",
			prompt:      "What does small.go return?",
			wantFloorGE: scopeFileFloorBase + 1_000,
			wantFloorLT: scopeFileFloorBase + 1_100,
			wantSignal:  "small.go",
		},
		{
			name:        "two files summed",
			prompt:      "How do pkg/huge.go and small.go interact?",
			wantFloorGE: scopeFileFloorBase + 41_000,
			wantFloorLT: scopeFileFloorBase + 42_000,
			wantSignal:  "huge.go",
		},
		{
			name:        "no real file → no floor",
			prompt:      "How does authentication work in general?",
			wantFloorGE: 0,
			wantFloorLT: 1,
		},
		{
			name:        "named file does not exist → no floor",
			prompt:      "Explain pkg/does_not_exist.go",
			wantFloorGE: 0,
			wantFloorLT: 1,
		},
		{
			name:        "dotted non-path token ignored",
			prompt:      "What does sense.estimate_scope emit?",
			wantFloorGE: 0,
			wantFloorLT: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			signal, floor := referencedFileFloor(dir, tc.prompt)
			if floor < tc.wantFloorGE || floor >= tc.wantFloorLT {
				t.Errorf("floor=%d; want [%d,%d)", floor, tc.wantFloorGE, tc.wantFloorLT)
			}
			if tc.wantSignal == "" {
				if signal != "" {
					t.Errorf("signal=%q; want empty", signal)
				}
			} else if !strings.Contains(signal, tc.wantSignal) {
				t.Errorf("signal=%q; want substring %q", signal, tc.wantSignal)
			}
		})
	}
}

// TestReferencedFileFloor_ClampedToMax ensures a pathologically large
// reference can't push the floor past the estimator's clamp.
func TestReferencedFileFloor_ClampedToMax(t *testing.T) {
	dir := t.TempDir()
	huge := filepath.Join(dir, "enormous.go")
	// 1M bytes → ~250K tokens, above maxScopeFloorTokens.
	if err := os.WriteFile(huge, make([]byte, 1_000_000), 0o644); err != nil {
		t.Fatal(err)
	}
	_, floor := referencedFileFloor(dir, "Summarize enormous.go")
	if floor != maxScopeFloorTokens {
		t.Errorf("floor=%d; want clamp at %d", floor, maxScopeFloorTokens)
	}
}

// TestReferencedFileFloor_AbsolutePath confirms absolute paths in the
// prompt resolve without being re-joined to workdir.
func TestReferencedFileFloor_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "abs.go")
	if err := os.WriteFile(f, make([]byte, 8_000), 0o644); err != nil {
		t.Fatal(err)
	}
	_, floor := referencedFileFloor(dir, "Look at "+f+" please")
	if floor < scopeFileFloorBase+1_900 || floor >= scopeFileFloorBase+2_100 {
		t.Errorf("floor=%d; want ~%d for an 8KB absolute-path file", floor, scopeFileFloorBase+2_000)
	}
}
