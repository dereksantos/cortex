// usage_test.go — `cortex --help` must list every subcommand main()
// dispatches on and exit 0, rather than falling through into the REPL.
// Same build-the-real-binary pattern as version_test.go, so it exercises
// main()'s actual os.Exit(0) path.
package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	bin := filepath.Join(t.TempDir(), "cortex")
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
