package study

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// A full-project study on a small (32K) window must keep each inference call's
// assembled sample inside the model's input budget — the regression that
// produced "Max length reached!" on the reasoner-npu (32K) path. Auto-density
// used to size k from the raw window, packing ~100% of the context into a single
// call; it now honors SampleTokenBudget, and a cumulative cap guards against
// chunk-size variance across files in a directory study.
func TestStudySampleStaysUnderBudget(t *testing.T) {
	const window = 32768

	// A corpus far larger than the window (read threshold is total < window/2 ≈
	// 64KB) so the study/sample path triggers and auto-density would, pre-fix,
	// pack the whole window into one call.
	files := map[string]string{}
	for i := 0; i < 80; i++ {
		var b strings.Builder
		for ln := 0; ln < 120; ln++ {
			fmt.Fprintf(&b, "func handler_%d_%d(ctx context.Context) error { return process(%d, %d) }\n", i, ln, i, ln)
		}
		files[fmt.Sprintf("pkg/mod%02d/file.go", i)] = b.String()
	}
	root := writeDirFixture(t, files)

	resp, err := StudyFile(context.Background(), StudyRequest{
		Path:    root,
		Window:  window,
		Density: nil, // auto — the path that overflowed
		// Infer nil → mechanical sample-only; assert on the assembled sample.
	})
	if err != nil {
		t.Fatalf("StudyFile(dir): %v", err)
	}
	if resp.Mode != "study" {
		t.Fatalf("Mode = %q, want study (corpus not large enough to sample?)", resp.Mode)
	}
	if len(resp.Sampled) == 0 {
		t.Fatal("expected a non-empty sample")
	}

	// The assembled sample (what becomes the inference prompt) must fit the
	// per-call input budget — the same one MakePlan reserves overhead/output in.
	budgetChars := SampleTokenBudget(window, studyDefaultTargetFill) * studyCharsPerToken
	total := 0
	for _, s := range resp.Sampled {
		total += len(s.Snippet)
	}
	if total > budgetChars {
		t.Errorf("assembled sample = %d chars > input budget %d chars (~%d-token window) — would overflow",
			total, budgetChars, window)
	}
}

// The cumulative cap must hold even when chunk sizes vary, which is the
// directory case: a mid-list oversized chunk can't be allowed to blow the
// budget. Drive auto-density and assert the cap stops accumulation, while still
// admitting at least one chunk so the call is never empty.
func TestStudySampleNeverEmptyAndCapped(t *testing.T) {
	const window = 16384 // tighter window → tighter budget
	files := map[string]string{}
	for i := 0; i < 60; i++ {
		files[fmt.Sprintf("src/f%02d.go", i)] = strings.Repeat(
			fmt.Sprintf("// line in file %d %s\n", i, strings.Repeat("x", 60)), 80)
	}
	root := writeDirFixture(t, files)

	resp, err := StudyFile(context.Background(), StudyRequest{Path: root, Window: window})
	if err != nil {
		t.Fatalf("StudyFile(dir): %v", err)
	}
	if resp.Mode != "study" {
		t.Fatalf("Mode = %q, want study", resp.Mode)
	}
	if len(resp.Sampled) == 0 {
		t.Fatal("a study must sample at least one chunk")
	}
	budgetChars := SampleTokenBudget(window, studyDefaultTargetFill) * studyCharsPerToken
	total := 0
	for _, s := range resp.Sampled {
		total += len(s.Snippet)
	}
	if total > budgetChars {
		t.Errorf("sample %d chars > budget %d chars on a %d-token window", total, budgetChars, window)
	}
}
