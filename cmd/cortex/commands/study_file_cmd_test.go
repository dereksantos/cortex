package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/study"
)

func TestRunFileStudy_SmallFileReadMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	// A sub-threshold file is read whole — the loop never invokes the
	// provider, so this runs fully offline (window passed → no probe).
	if err := runFileStudy(&Context{}, fileStudyOpts{path: path, window: 8192}, &buf); err != nil {
		t.Fatalf("runFileStudy: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "read whole target") {
		t.Errorf("expected read-mode output, got:\n%s", out)
	}
	if !strings.Contains(out, "stopped: read") {
		t.Errorf("expected 'stopped: read', got:\n%s", out)
	}
}

func TestDecisionStr(t *testing.T) {
	cases := []struct {
		d    study.Decision
		want string
	}{
		{study.Decision{Kind: study.DecisionDone}, "DONE"},
		{study.Decision{Kind: study.DecisionDensify, Density: "dense"}, "DENSIFY (density=dense)"},
		{study.Decision{Kind: study.DecisionTarget, Focus: &study.Focus{Lines: [2]int{10, 20}}}, "TARGET lines 10-20"},
		{study.Decision{Kind: study.DecisionTarget, Focus: &study.Focus{Path: "pkg/a.go", Lines: [2]int{10, 20}}}, "TARGET pkg/a.go lines 10-20"},
	}
	for _, c := range cases {
		if got := decisionStr(c.d); got != c.want {
			t.Errorf("decisionStr(%+v) = %q, want %q", c.d, got, c.want)
		}
	}
}
