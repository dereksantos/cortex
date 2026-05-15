//go:build !windows

package eval

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
)

// captureAttemptFailures persists the structured failure signals from
// one Mode-B attempt into the per-eval Cortex store as Insight rows.
// Each insight lands in a category cortex_search can target on the
// next attempt:
//
//   - "constraint" — build failures (compile errors block all progress)
//   - "correction" — frame-diff mismatches (the model's logic was wrong)
//   - "correction" — judge failures (qualitative correctness gap)
//
// The store is opened fresh per call (cheap; JSONL-backed) and closed
// before return. Errors during capture are non-fatal for the runner —
// the next attempt simply lacks the context Mode B was supposed to
// provide, which is still a valid (and signal-bearing) result.
//
// storeDir is the absolute path to the per-eval `.cortex/` directory.
// attempt is the 1-indexed attempt number.
func captureAttemptFailures(storeDir string, attempt int, build FrameDiffResult, judge GoLJudgeResult) error {
	if !filepath.IsAbs(storeDir) {
		return fmt.Errorf("storeDir must be absolute, got %q", storeDir)
	}

	cfg := &config.Config{ContextDir: storeDir}
	store, err := storage.New(cfg)
	if err != nil {
		return fmt.Errorf("open per-eval store: %w", err)
	}

	tagBase := []string{"cortex-coding-eval", fmt.Sprintf("attempt-%d", attempt), "conways-game-of-life"}

	if !build.BuildOK {
		summary := fmt.Sprintf(
			"attempt %d: `go build` failed. The build output below is the exact compiler error. Fix the failing file before iterating on logic.\n\n%s",
			attempt, truncateGoL(build.BuildOut, 1024),
		)
		if err := store.StoreInsight(newCaptureID("build"), "constraint", summary, 9,
			append(tagBase, "build-failure"),
			"captured automatically by cortex coding runner between attempts",
		); err != nil {
			return fmt.Errorf("store build-failure insight: %w", err)
		}
		return nil
	}

	for _, c := range build.Cases {
		if c.Passed {
			continue
		}
		head := fmt.Sprintf(
			"attempt %d: fixture %q failed frame-diff against the golden output. The model's implementation evolved the grid incorrectly on this canonical pattern. Diff sample:\n\n%s",
			attempt, c.Name, c.DiffSample,
		)
		if c.Err != "" {
			head += "\n\nrun error: " + c.Err
		}
		if err := store.StoreInsight(newCaptureID("frame"), "correction", head, 8,
			append(tagBase, "frame-diff", "fixture-"+c.Name),
			"captured automatically by cortex coding runner between attempts",
		); err != nil {
			return fmt.Errorf("store frame-diff insight for %s: %w", c.Name, err)
		}
	}

	if judge.Pass {
		return nil
	}
	if judge.Verdict != "" {
		summary := fmt.Sprintf(
			"attempt %d: the Conway's Game of Life judge marked the freeform run as a failure. Verdict: %s",
			attempt, judge.Verdict,
		)
		if err := store.StoreInsight(newCaptureID("judge"), "correction", summary, 7,
			append(tagBase, "judge", "qualitative-correctness"),
			"captured automatically by cortex coding runner between attempts",
		); err != nil {
			return fmt.Errorf("store judge insight: %w", err)
		}
	}
	return nil
}

// newCaptureID returns a unique synthetic event-ID for an insight
// that has no upstream event. Format: "coding-<facet>-<8hex>". The
// facet prefix is purely diagnostic (e.g. greppable in the JSONL).
func newCaptureID(facet string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "coding-" + strings.ToLower(facet) + "-" + hex.EncodeToString(b[:])
}
