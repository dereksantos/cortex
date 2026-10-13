// usage_test.go — `cortex --help` must list every subcommand main()
// dispatches on and exit 0, rather than falling through into the REPL.
// Same build-the-real-binary pattern as version_test.go, so it exercises
// main()'s actual os.Exit(0) path.
package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/style"
)

func TestUsageLinesCoverSubcommands(t *testing.T) {
	body := strings.Join(usageLines, "\n")
	for _, cmd := range []string{
		"resume", "turn", "study", "learn", "change", "serve",
		"scan", "project", "discord", "model", "study-eval", "version",
	} {
		if !strings.Contains(body, "cortex "+cmd) {
			t.Errorf("usageLines does not document subcommand %q", cmd)
		}
	}
}

func TestUsageFlag(t *testing.T) {
	bin := filepath.Join(t.TempDir(), exeName("cortex"))
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build cortex: %v\n%s", err, out)
	}

	for _, flag := range []string{"--help", "-h", "help"} {
		t.Run(flag, func(t *testing.T) {
			out, err := exec.Command(bin, flag).CombinedOutput()
			if err != nil {
				t.Fatalf("cortex %s: %v\n%s", flag, err, out)
			}
			got := string(out)
			if !strings.Contains(got, "cortex study") {
				t.Errorf("cortex %s output does not list subcommands:\n%s", flag, got)
			}
			if !strings.Contains(got, "/help") {
				t.Errorf("cortex %s output does not point at /help:\n%s", flag, got)
			}
		})
	}
}

// TestUsagePlainWhenStdoutNotATerminal covers issue #264: usage text that
// goes to a file or a pipe must be plain text, so `cortex --help > usage.txt`
// and `cortex --help | less` carry no ANSI escapes. The color role logic in
// style.Paint is unchanged; printUsage just declines to paint when stdout is
// not a terminal.
func TestUsagePlainWhenStdoutNotATerminal(t *testing.T) {
	// Force Paint to emit color, to prove printUsage -- not the global
	// NO_COLOR switch -- is what strips the escapes on a non-terminal.
	defer style.ForceColor(true)()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	printUsage()
	os.Stdout = orig
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	var buf strings.Builder
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "\x1b[") {
		t.Errorf("printUsage() wrote ANSI escapes to a non-terminal stdout (issue #264):\n%q", out)
	}
	if !strings.Contains(out, "Usage:") {
		t.Errorf("printUsage() non-terminal output lost its headings:\n%s", out)
	}
}

// exeName adds ".exe" on Windows, which won't run a built binary without
// it (#288). Tests that `go build -o` the cortex binary name it with this.
func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}
