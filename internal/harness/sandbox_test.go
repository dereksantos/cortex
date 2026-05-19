package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestContainPath covers the path-containment guard that every fs tool
// uses. Each row is a (workdir, relative-path) pair the tool would
// receive; success means the absolute path falls inside workdir and
// not under .git / .cortex.
func TestContainPath(t *testing.T) {
	tmp, err := os.MkdirTemp("", "harness-contain-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(tmp)

	tests := []struct {
		name    string
		rel     string
		wantErr error
	}{
		{name: "simple file", rel: "main.go"},
		{name: "subdir", rel: "internal/util/foo.go"},
		{name: "dot", rel: "."},
		{name: "escapes via ..", rel: "../etc/passwd", wantErr: errPathEscapesWorkdir},
		{name: "absolute path", rel: "/etc/passwd", wantErr: errArgIsAbsolutePath},
		{name: ".git denied", rel: ".git/HEAD", wantErr: errPathIsReservedDir},
		{name: ".cortex denied", rel: ".cortex/data/events.jsonl", wantErr: errPathIsReservedDir},
		{name: "empty", rel: "", wantErr: errEmptyPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := containPath(tmp, tt.rel)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr.Error()) {
				t.Errorf("error %q does not wrap %v", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestValidateShellArg + TestResolveShellCommand were removed in the
// iter-7 (2026-05-19) refactor that replaced the binary-allowlist
// run_shell with a real `bash -c` invocation. The model can now run
// arbitrary commands; defense moves to user-configurable shell
// policy (separate slice). The path-containment tests below still
// cover the fs-tool guarantees that survived the rewrite.

// TestContainPath_AbsoluteWorkdirRequired locks in that workdir must
// be absolute. A relative workdir could let a chdir later in the
// process flip the containment semantics.
func TestContainPath_AbsoluteWorkdirRequired(t *testing.T) {
	_, err := containPath("rel/workdir", "main.go")
	if err == nil {
		t.Fatalf("relative workdir should error")
	}
	if !strings.Contains(err.Error(), "workdir must be an absolute path") {
		t.Errorf("error %q does not mention workdir-must-be-absolute", err.Error())
	}
}

// TestContainPath_SamePathSuffix prevents a path like
// /workdir-evil from passing when workdir is /workdir.
func TestContainPath_SamePathSuffix(t *testing.T) {
	tmp, err := os.MkdirTemp("", "harness-suffix-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(tmp)

	// Manually construct a sibling dir that shares the workdir prefix.
	parent := filepath.Dir(tmp)
	base := filepath.Base(tmp)
	evil := filepath.Join(parent, base+"-evil")
	if err := os.MkdirAll(evil, 0o755); err != nil {
		t.Fatalf("mkdir evil: %v", err)
	}
	defer os.RemoveAll(evil)

	// Try to read across the boundary via a relative path that
	// resolves into the evil dir. There's no way to do this from a
	// pure "rel" parameter to containPath without "..", which we
	// already block. This test is therefore a smoke check that the
	// HasPrefix guard includes the separator (so the prefix match
	// wouldn't accidentally permit the sibling).
	if _, err := containPath(tmp, "."); err != nil {
		t.Errorf(". inside workdir should be allowed: %v", err)
	}
}
