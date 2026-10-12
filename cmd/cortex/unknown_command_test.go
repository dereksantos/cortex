// unknown_command_test.go — issue #262: a first argument that names no
// subcommand must fail loudly (one-line error + usage on stderr, exit 2)
// instead of silently falling through into the REPL, which ignored the word
// and looked like a hang. Same build-the-real-binary pattern as usage_test.go,
// so it exercises main()'s actual os.Exit(2) path.
package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnknownCommandFailsLoud(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "cortex")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build cortex: %v\n%s", err, out)
	}

	// A private HOME keeps the run off the developer's real ~/.cortex; the
	// child gets /dev/null on stdin (exec's default), so a fall-through REPL
	// would read EOF and exit instead of blocking the test on its read loop.
	for _, word := range []string{"sutdy", "bogus", "hlep"} {
		t.Run(word, func(t *testing.T) {
			cmd := exec.Command(bin, word)
			cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "NO_COLOR=1")
			// Run in a scratch CWD so a fall-through REPL cannot drop a
			// .cortex/ directory into the checkout.
			cmd.Dir = t.TempDir()
			var stdout, stderr strings.Builder
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()

			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("cortex %s: want exit code 2, got err=%v\nstdout:\n%s\nstderr:\n%s",
					word, err, stdout.String(), stderr.String())
			}
			if code := exitErr.ExitCode(); code != 2 {
				t.Errorf("cortex %s: exit code = %d, want 2", word, code)
			}
			want := "cortex: unknown command \"" + word + "\""
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("cortex %s: stderr missing %q:\n%s", word, want, stderr.String())
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Errorf("cortex %s: stderr does not carry the usage:\n%s", word, stderr.String())
			}
			if strings.Contains(stdout.String(), "Usage:") {
				t.Errorf("cortex %s: usage went to stdout, want stderr:\n%s", word, stdout.String())
			}
		})
	}
}
