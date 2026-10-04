package loopui

// spinner_golden_test.go — golden snapshot for the plain spinner frame
// (issue #112). The spinner has no width axis (it renders a single status line
// to stdout with no clipping). Its color comes from the label, which the
// caller pre-colors via tools.Color — in the REPL the caller is cmd/cortex's
// defaultLabel ("thinking..." in Cyan), so the colored golden records that
// exact SGR; the NO_COLOR golden records the same frame with tools.Color
// forced off, the shape a NO_COLOR terminal receives.
//
// The frame is captured deterministically: the spinner's repaint interval is
// an unexported field (tickInterval) that this test pins to 1ms, and the
// capture POLLS for the first repaint with a generous deadline before
// calling Stop. The first repaint can land before or after the test's stdout
// swap depending on goroutine scheduling — tick timing was the source of
// nondeterminism (NOT the swap itself: Start runs inside the capture, so the
// swap is in place before any tick) — and polling absorbs it. If no repaint
// arrives by the deadline the test fails (t.Fatalf) rather than silently
// returning a clear-only frame.
//
// The goldens live in testdata/*.golden next to this file; regenerate with
// `go test ./internal/loopui -update`.

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/tools"
)

// updateSpinnerGoldens is the -update flag (issue #112).
var updateSpinnerGoldens = flag.Bool("update", false, "write testdata/*.golden files instead of comparing against them")

// spinnerGolden compares got against testdata/name+".golden" and fails with
// the full diff on mismatch; with -update it (re)writes the file.
func spinnerGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *updateSpinnerGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("golden %s: mkdir: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("golden %s: write: %v", path, err)
		}
		t.Logf("golden %s: updated (%d bytes)", path, len(got))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s: read: %v — run with -update to create it", path, err)
	}
	if string(want) != got {
		t.Errorf("golden %s mismatch:\n--- want ---\n%s\n--- got ---\n%s\n(re-run with -update to accept the new frame)",
			path, want, got)
	}
}

// spinnerFrame runs the spinner with label from Start() (so the first repaint
// is captured), polls the live capture until that repaint lands, then stops
// the spinner (which clears the line), and returns the deterministic frame:
// the FIRST repaint plus the Stop() clear — the two-part shape the golden
// pins.
//
// Determinism: the repaint interval is an unexported field (tickInterval,
// set here because this test lives in the spinner's own package) pinned to
// 10ms, so the first tick fires ~10ms after Start; the poll then absorbs the
// scheduling race (the tick firing before the pipe reader goroutine has
// caught up) with a generous 2s deadline, and fails hard (t.Fatalf) if no
// repaint arrives rather than recording a clear-only frame. The 10ms period
// also guarantees no second repaint lands inside the poll window (a 1ms
// period leaked one under a stalled machine), so the capture is exactly
// first repaint + Stop clear.
func spinnerFrame(t *testing.T, label string) string {
	t.Helper()
	repaint := "\r" + label + "\033[K"
	clear := "\r\033[K"
	s := NewSpinner()
	s.SetLabel(label)
	s.tickInterval = 10 * time.Millisecond // test-only: first tick fast and deterministic

	var mu sync.Mutex
	var out strings.Builder
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				mu.Lock()
				out.Write(buf[:n])
				mu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}()
	snapshot := func() string {
		mu.Lock()
		defer mu.Unlock()
		return out.String()
	}

	prev := os.Stdout
	os.Stdout = w
	s.Start()

	// Poll the live capture until the first repaint has landed.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(snapshot(), repaint) {
		if time.Now().After(deadline) {
			os.Stdout = prev
			w.Close()
			t.Fatalf("spinner produced no repaint within 2s: %q", snapshot())
		}
		time.Sleep(time.Millisecond)
	}
	s.Stop()
	os.Stdout = prev
	w.Close()
	<-done // the reader drains the rest of the pipe before close(done)
	r.Close()

	frames := snapshot()
	i := strings.Index(frames, repaint)
	if i < 0 {
		t.Fatalf("spinner frame missing the repaint: %q", frames)
	}
	tail := frames[i:]
	if !strings.HasSuffix(tail, clear) {
		t.Fatalf("spinner frame does not end with the Stop clear: %q", frames)
	}
	return tail
}

// TestSpinnerGolden pins the plain spinner frame — one repaint plus the
// Stop() clear — in both color forms: the label colored the way
// cmd/cortex's defaultLabel colors it (thinking... in Cyan), and the same
// label with tools.Color forced off (the NO_COLOR terminal's frame). Color is
// pinned via tools.SetColorDisabledForTest so the goldens hold regardless of
// the developer's NO_COLOR environment.
func TestSpinnerGolden(t *testing.T) {
	const label = "thinking... 3s"

	t.Run("colored", func(t *testing.T) {
		defer tools.SetColorDisabledForTest(false)()
		spinnerGolden(t, "spinner_colored", spinnerFrame(t, tools.Color(label, tools.Cyan)))
	})

	t.Run("no_color", func(t *testing.T) {
		defer tools.SetColorDisabledForTest(true)()
		spinnerGolden(t, "spinner_nocolor", spinnerFrame(t, tools.Color(label, tools.Cyan)))
	})
}
