package loopui

// spinner_golden_test.go — golden snapshot for the plain spinner frame
// (issue #112). The spinner has no width axis (it renders a single status line
// to stdout with no clipping). Its color comes from the label, which the
// caller pre-colors via tools.Color — in the REPL the caller is cmd/cortex's
// defaultLabel ("thinking..." in Cyan), so the golden records that colored
// form, the exact SGR the terminal receives.
//
// The NO_COLOR form is not pinned here: tools.colorDisabled is unexported and
// read once at startup, so a test in another package can't flip it (the same
// reason cmd/cortex's grid NO_COLOR subtest strips ANSI instead of pinning the
// flag). The NO_COLOR contract — that Color strips its wrap — is tested in
// internal/tools.
//
// The spinner repaints on a 90ms tick; the golden records the shape of ONE
// repaint plus the Stop() clear (the only two things the spinner writes):
// "\r<colored label>\033[K" for the repaint, then "\r\033[K" for the clear.
//
// The goldens live in testdata/*.golden next to this file; regenerate with
// `go test ./internal/loopui -update`.

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
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
// is captured) for just over one tick, then stops it (which clears the line),
// and returns the deterministic tail of the frame: the LAST repaint + the
// Stop() clear. The first repaint can land before or after the test's stdout
// swap depending on goroutine scheduling, so the head of the capture is
// nondeterministic — but the tail (one repaint + one clear) is the same
// either way, and it is the shape the terminal ends up showing. The frame is
// otherwise deterministic: no wall clock or elapsed time (the label's "N s"
// is the caller's own text).
func spinnerFrame(t *testing.T, label string) string {
	t.Helper()
	repaint := "\r" + label + "\033[K"
	s := NewSpinner()
	s.SetLabel(label)
	out := captureStdout(t, func() {
		s.Start()
		// Wait just over one 90ms tick so at least one repaint lands, then stop.
		time.Sleep(100 * time.Millisecond)
		s.Stop()
	})
	// The capture is repaint* followed by the Stop clear "\r\033[K". The
	// deterministic tail is the last repaint (if directly present) + the clear.
	clear := "\r\033[K"
	i := strings.LastIndex(out, clear)
	if i < 0 {
		t.Fatalf("spinner frame missing the Stop clear: %q", out)
	}
	if i >= len(repaint) && out[i-len(repaint):i] == repaint {
		return out[i-len(repaint):]
	}
	return out[i:]
}

// TestSpinnerGolden pins the plain spinner frame — one repaint plus the
// Stop() clear — as it renders in the REPL: the label colored the way
// cmd/cortex's defaultLabel colors it (thinking... in Cyan).
func TestSpinnerGolden(t *testing.T) {
	const label = "thinking... 3s"
	spinnerGolden(t, "spinner", spinnerFrame(t, tools.Color(label, tools.Cyan)))
}

// captureStdout runs f with os.Stdout redirected and returns what it printed.
// The spinner prints via fmt.Print/Printf to os.Stdout, so capturing it (and
// only it) requires swapping the global handle around the run.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	f()
	os.Stdout = prev
	w.Close()
	out := <-done
	r.Close()
	return out
}
