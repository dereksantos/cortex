package commands

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dereksantos/cortex/internal/study"
)

// runSampleOnly is the mechanical checkpoint for `cortex study FILE
// --sample-only`: it runs phase-1 sampling (byte-grid → hierarchical
// sampler → ReadRegion → lazy line refinement) with NO LLM, and prints
// the sampled chunk table plus coverage. It is the inspectable proof
// that density-bound sampling works against a real large file before any
// agentic inference is wired in.
func runSampleOnly(path string, density study.Density, window int, focus *study.Focus, w io.Writer) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	resp, err := study.StudyFile(context.Background(), study.StudyRequest{
		Path:    abs,
		RelPath: path,
		Density: density,
		Window:  window,
		Focus:   focus,
		// Infer nil → mechanical sample only.
	})
	if err != nil {
		return err
	}

	if resp.Mode == "read" {
		fmt.Fprintf(w, "mode: read — file fits window/2; study degenerates to a whole-file read (%d bytes).\n", len(resp.ReadContent))
		return nil
	}

	fmt.Fprintf(w, "mode: study   path=%s   sampled=%d chunks\n", path, len(resp.Sampled))
	if focus != nil {
		fmt.Fprintf(w, "focus: lines %d-%d\n", focus.Lines[0], focus.Lines[1])
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %-28s %-18s %-7s  %s\n", "region", "bytes", "eff", "snippet")
	fmt.Fprintf(w, "  %-28s %-18s %-7s  %s\n", strings.Repeat("-", 28), strings.Repeat("-", 18), "-------", strings.Repeat("-", 40))
	for _, s := range resp.Sampled {
		region := fmt.Sprintf("%s:%d-%d", s.RelPath, s.LineStart, s.LineEnd)
		bytes := fmt.Sprintf("%d/+%d", s.ByteOffset, s.ByteLength)
		fmt.Fprintf(w, "  %-28s %-18s %-7d  %s\n", region, bytes, s.EffLines, firstLine(s.Snippet))
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "coverage: %d/%d eff lines = %.1f%%   exhausted=%t\n",
		resp.Coverage.EffLinesSeen, resp.Coverage.EffLinesTotal, 100*resp.Coverage.Pct, resp.Exhausted)
	return nil
}

// firstLine returns a single trimmed, length-capped line for the sample
// table's snippet column.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	const max = 56
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// parseFocusLines parses a "start,end" pair into a *study.Focus. Empty
// input yields nil (no focus).
func parseFocusLines(s string) (*study.Focus, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.SplitN(s, ",", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("--focus-lines: want START,END, got %q", s)
	}
	lo, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return nil, fmt.Errorf("--focus-lines start: %w", err)
	}
	hi, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return nil, fmt.Errorf("--focus-lines end: %w", err)
	}
	return &study.Focus{Lines: [2]int{lo, hi}}, nil
}
