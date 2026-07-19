// version_test.go — E4 (docs/completion-roadmap.md): `cortex --version` /
// `cortex version` must print the same display version the REPL banner
// shows and exit 0. TestVersionFlag builds the real binary (same pattern
// as memory_e2e_live_test.go) rather than exec'ing the test binary, so it
// exercises main()'s actual os.Exit(0) path.
package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionString(t *testing.T) {
	v := version()
	if v == "" {
		t.Fatal("version() returned empty string")
	}
	if !strings.HasPrefix(v, Version) {
		t.Errorf("version() = %q, want prefix %q", v, Version)
	}
}

func TestVersionFlag(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "cortex")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build cortex: %v\n%s", err, out)
	}

	for _, flag := range []string{"--version", "-v", "version"} {
		t.Run(flag, func(t *testing.T) {
			out, err := exec.Command(bin, flag).CombinedOutput()
			if err != nil {
				t.Fatalf("cortex %s: %v\n%s", flag, err, out)
			}
			got := strings.TrimSpace(string(out))
			if !strings.HasPrefix(got, "cortex ") {
				t.Errorf("cortex %s output = %q, want prefix \"cortex \"", flag, got)
			}
		})
	}
}
