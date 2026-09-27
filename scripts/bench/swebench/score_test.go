package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseScore(t *testing.T) {
	const id = "django__django-1"
	tests := []struct {
		name     string
		report   string // "" = no report.json
		log      string
		want     string
		resolved bool
	}{
		{"resolved", `{"django__django-1":{"patch_successfully_applied":true,"resolved":true}}`, "", EvalResolved, true},
		{"unresolved", `{"django__django-1":{"patch_successfully_applied":true,"resolved":false}}`, "", EvalUnresolved, false},
		{"not applied in report", `{"django__django-1":{"patch_successfully_applied":false,"resolved":false}}`, "", EvalApplyFailed, false},
		{"apply failure in log", "", "... >>>>> Patch Apply Failed:\nerror: corrupt patch", EvalApplyFailed, false},
		{"no report no marker", "", "docker exploded", EvalError, false},
		{"report for other instance", `{"x":{"resolved":true}}`, "", EvalError, false},
		{"garbage report", `{not json`, "", EvalError, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.report != "" {
				if err := os.WriteFile(filepath.Join(dir, "report.json"), []byte(tt.report), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tt.log != "" {
				if err := os.WriteFile(filepath.Join(dir, "run_instance.log"), []byte(tt.log), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := ParseScore(dir, id)
			if got.Status != tt.want || got.Resolved != tt.resolved {
				t.Errorf("ParseScore=%+v want status %s resolved %v", got, tt.want, tt.resolved)
			}
		})
	}
}

func TestHarnessLogDirMirrorsHarness(t *testing.T) {
	got := harnessLogDir("/e", "run1", "cortex-abc__qwen--qwen3-coder", "a__b-1")
	want := filepath.Join("/e", "logs", "run_evaluation", "run1", "cortex-abc__qwen--qwen3-coder", "a__b-1")
	if got != want {
		t.Errorf("got %s want %s", got, want)
	}
	// The harness replaces "/" in model_name_or_path with "__".
	if got := harnessLogDir("/e", "r", "a/b", "i"); !strings.Contains(got, "a__b") {
		t.Errorf("slash not mapped: %s", got)
	}
}

// An empty patch is never sent to the harness (it would skip it anyway).
func TestScoreOneSkipsEmptyPatch(t *testing.T) {
	res := ScoreOne(context.Background(), "/nonexistent/python", "ds.jsonl", t.TempDir(), "r",
		Prediction{InstanceID: "i", ModelNameOrPath: "m", ModelPatch: "  \n"}, time.Minute)
	if res.Status != EvalSkippedEmpty {
		t.Errorf("status=%s want %s", res.Status, EvalSkippedEmpty)
	}
}
