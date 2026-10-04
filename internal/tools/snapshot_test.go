package tools

// snapshot_test.go — the golden snapshots for the three pure renderers: the
// tool-action line, the file diff, and the nested subagent lines. Each frame
// is pinned across the degradation matrix that path actually varies by —
// terminal width (40/80/200, plus the non-TTY width-0 that clips nothing),
// color mode (ANSI vs NO_COLOR), and — for the diff path only — the
// CORTEX_LOOP_RENDER=0 flat fallback, which suppresses the diff body. The
// tool-action and nested lines render identically rich or flat (only the diff
// body is gated on richRenderDisabled), so they sweep width × color; the diff
// sweeps width × color × {rich, flat}. A change to any clipping, indent,
// color, or glyph therefore shows up as a golden diff in review.
//
// The frames are deterministic: the wall-clock gutter and per-call elapsed
// times are the only run-to-run quantities and normalizeFrame pins both
// (golden_test.go), so the goldens record the SHAPE of each path — clipping,
// indentation, markers, colors — and nothing that depends on when the test
// ran.
//
// The goldens live in testdata/*.golden next to this file; regenerate with
// `go test ./internal/tools -update`.

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// seedPath is a fixed, non-existent path every seed read_file runs against.
// read_file on a missing path returns a stable error ("open <path>: no such
// file or directory"), so the child action lines and result summaries the
// nested renderers emit are deterministic — the alternative (a t.TempDir() seed
// file) embeds a per-run PID in its path and the golden would churn every run.
// The path is also long enough to exercise the width clip at 40 columns.
const seedPath = "/tmp/cortex-snapshot/nonexistent-seed-file.txt"

// width is a terminal-width axis value. 40 and 200 are the clip boundary on
// either side of 80; 0 is the non-TTY/piped form that clips nothing.
type width int

// matrix is the width axis every renderer sweeps.
var matrix = []width{40, 80, 200, 0}

// caseName names a matrix corner: the width (w0 for the no-clip path) plus,
// for the diff path, the render mode: flat means CORTEX_LOOP_RENDER=0 (true),
// rich means the default rendering (false).
func caseName(w width, flat bool) string {
	s := "w0"
	if w != 0 {
		s = "w" + fmt.Sprint(int(w))
	}
	if flat {
		return s + "_flat"
	}
	return s + "_rich"
}

// restoreRich pins richRenderDisabled for one test and restores it on cleanup.
func restoreRich(t *testing.T, disabled bool) {
	t.Helper()
	prev := richRenderDisabled
	richRenderDisabled = disabled
	t.Cleanup(func() { richRenderDisabled = prev })
}

// runCase captures the frame f renders under one width (in both color and
// NO_COLOR form) with the given render mode (flat = CORTEX_LOOP_RENDER=0),
// and writes/compares two goldens (…_colored.golden and …_nocolor.golden)
// under testdata. The frame is normalized before compare/write so the wall
// clock and elapsed times never leak in. base is the renderer's name; w and
// flat name the corner.
func runCase(t *testing.T, base string, w width, flat bool, f func(width int) string) {
	t.Helper()
	defer restoreWidth(t, int(w))()
	restoreRich(t, flat)

	var colored, nocolor string
	{
		defer restoreColor(t, false)()
		colored = f(int(w))
	}
	{
		defer restoreColor(t, true)()
		nocolor = f(int(w))
	}
	name := fmt.Sprintf("snap_%s_%s", base, caseName(w, flat))
	goldenFrame(t, "testdata", name+"_colored", colored)
	goldenFrame(t, "testdata", name+"_nocolor", nocolor)
}

// --- tool-action line -------------------------------------------------------

// TestToolActionLineSnapshot pins the tool-action line across the width axis,
// in both color and NO_COLOR form. It renders the action directly
// (formatToolAction), not through Execute, so the seed need not exist — the
// action line only shows the path. The flat fallback is not an axis here:
// formatToolAction renders the same line rich or flat.
func TestToolActionLineSnapshot(t *testing.T) {
	resetNesting(t) // the line must print at the top margin, not captured
	action := "read_file(" + seedPath + ")"
	for _, w := range matrix {
		w := w
		t.Run(caseName(w, false), func(t *testing.T) {
			runCase(t, "toolaction", w, false, func(w int) string {
				return formatToolAction("", action, "")
			})
		})
	}
}

// --- file diff --------------------------------------------------------------

// seedDiff returns a before/after pair: one line changed, a few lines of
// context on each side, and a line added at the end. The numbers grow the
// line-number column, the context exercises hunk collapsing, and the added
// line exercises the '+' color/NO_COLOR path.
func seedDiff() (before, after string) {
	before = "package main\n\nfunc f() int {\n\tif a {\n\t\treturn 1\n\t}\n\treturn 2\n}\n"
	after = "package main\n\nfunc f() int {\n\tif a {\n\t\treturn 1\n\t}\n\treturn 3\n}\nvar extra = 0\n"
	return
}

// TestDiffUnderEditSnapshot pins the file diff under an edit across the full
// width × color × {rich, flat} matrix. It renders through the real print path
// (printFileDiff), not renderDiff directly: the flat fallback suppresses the
// diff body in printFileDiff, so that is what makes the flat goldens differ
// from the rich ones. deps is non-quiet so the lines are actually emitted and
// captured; width and the flat flag are pinned by runCase.
func TestDiffUnderEditSnapshot(t *testing.T) {
	before, after := seedDiff()
	for _, w := range matrix {
		w := w
		for _, flat := range []bool{false, true} {
			w, flat := w, flat
			t.Run(caseName(w, flat), func(t *testing.T) {
				runCase(t, "diff", w, flat, func(w int) string {
					return captureStdout(t, func() {
						printFileDiff(headlessDeps{}, before, after)
					})
				})
			})
		}
	}
}

// --- nested subagent lines --------------------------------------------------

// readStudyNest runs one study subagent that makes nested read_file calls
// against read, and returns the captured stdout (parent line, the child lines,
// the done line) as a single string. The read is EXPECTED to fail (seedPath
// does not exist) — the failure IS the deterministic content: a stable "no
// such file" error that the child lines summarize. The call count is small so
// the display cap never trips.
func readStudyNest(t *testing.T, read string, nested int, digest string) string {
	t.Helper()
	deps := &subDeps{digest: digest}
	deps.loop = func(d ToolDeps) {
		for i := 0; i < nested; i++ {
			// The read fails by design (non-existent seedPath); the error is the
			// stable content the child line summarizes, so no t.Errorf here.
			_, _ = Execute(context.Background(), editCall(t, FunctionReadFile,
				map[string]any{"path": read}), d)
		}
	}
	out := captureStdout(t, func() {
		if _, err := Execute(context.Background(), studyCall(t, "internal/tools", "where is the gutter"), deps); err != nil {
			t.Fatalf("study: %v", err)
		}
	})
	return strings.TrimRight(out, "\n")
}

// TestNestedSubagentLinesSnapshot pins the nested subagent lines — parent
// announcement, the indented child calls (each with elapsed + a result
// summary), and the done line — across the width axis in both color modes.
// The child calls read the fixed non-existent seedPath, so their result
// summaries (the read error) are deterministic. The flat fallback is not an
// axis here: the nesting renderers emit the same lines rich or flat.
func TestNestedSubagentLinesSnapshot(t *testing.T) {
	resetNesting(t)
	for _, w := range matrix {
		w := w
		t.Run(caseName(w, false), func(t *testing.T) {
			runCase(t, "nested", w, false, func(w int) string {
				return readStudyNest(t, seedPath, 2, "a digest of the study")
			})
		})
	}
}

// TestNestedSubagentDoneLineSnapshot pins the subagent done line in its two
// terminal states — a clean finish and a failure — across the width axis in
// both color modes. The done line is the part that clips at narrow widths (it
// carries the call count, the elapsed, and the digest size / error), so it
// gets its own snapshots to catch a clip or a color regression there.
func TestNestedSubagentDoneLineSnapshot(t *testing.T) {
	resetNesting(t)
	for _, w := range matrix {
		w := w
		t.Run("clean_"+caseName(w, false), func(t *testing.T) {
			runCase(t, "nested_done", w, false, func(w int) string {
				return lastLine(readStudyNest(t, seedPath, 2, strings.Repeat("x", 100)))
			})
		})
		t.Run("error_"+caseName(w, false), func(t *testing.T) {
			runCase(t, "nested_done_err", w, false, func(w int) string {
				return lastLine(runStudyError(t))
			})
		})
	}
}

// runStudyError runs one study whose subagent loop errors and returns the
// captured stdout (parent line, no child lines, an error done line).
func runStudyError(t *testing.T) string {
	t.Helper()
	deps := &subDeps{err: fmt.Errorf("backend refused the request")}
	out := captureStdout(t, func() {
		if _, err := Execute(context.Background(), studyCall(t, "internal/tools", "where is the gutter"), deps); err == nil {
			t.Fatal("want the error to propagate")
		}
	})
	return strings.TrimRight(out, "\n")
}

// lastLine returns the final line of s (the subagent's done line), or s itself
// when it is a single line.
func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
