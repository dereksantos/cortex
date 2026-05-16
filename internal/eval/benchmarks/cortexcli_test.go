package benchmarks

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/events"
)

// repoRoot walks up from this test file (under internal/eval/benchmarks/)
// to the module root. Used by tests that build the cortex binary on
// demand.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// .../internal/eval/benchmarks/cortexcli_test.go → repo root is 4 up
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

// Build the cortex binary once per test binary, not once per test:
// even a cached `go build` adds latency that dominates fast unit tests.
var (
	sharedBinaryOnce sync.Once
	sharedBinary     string
	sharedBinaryErr  error
)

func sharedCortexBinary(t *testing.T) string {
	t.Helper()
	sharedBinaryOnce.Do(func() {
		sharedBinary, sharedBinaryErr = CompileBinary(repoRoot(t))
	})
	if sharedBinaryErr != nil {
		t.Fatalf("CompileBinary: %v", sharedBinaryErr)
	}
	return sharedBinary
}

func TestResolveCortexBinary(t *testing.T) {
	t.Run("env var must be absolute", func(t *testing.T) {
		t.Setenv("CORTEX_BINARY", "relative/path")
		if _, err := ResolveCortexBinary(); err == nil {
			t.Fatal("expected error for relative CORTEX_BINARY")
		}
	})

	t.Run("env var must exist", func(t *testing.T) {
		t.Setenv("CORTEX_BINARY", "/no/such/cortex/binary/exists")
		if _, err := ResolveCortexBinary(); err == nil {
			t.Fatal("expected error for missing CORTEX_BINARY")
		}
	})

	t.Run("env var honored when valid", func(t *testing.T) {
		bin := sharedCortexBinary(t)
		t.Setenv("CORTEX_BINARY", bin)
		got, err := ResolveCortexBinary()
		if err != nil {
			t.Fatalf("ResolveCortexBinary: %v", err)
		}
		if got != bin {
			t.Errorf("ResolveCortexBinary = %q, want %q", got, bin)
		}
	})
}

func TestRunBulkCapture_Validation(t *testing.T) {
	ctx := context.Background()
	if err := RunBulkCapture(ctx, "", "/tmp/workdir", nil); err == nil {
		t.Error("expected error for empty binary")
	}
	if err := RunBulkCapture(ctx, "/bin/true", "", nil); err == nil {
		t.Error("expected error for empty workdir")
	}
	// Empty events list is a no-op — must not error.
	if err := RunBulkCapture(ctx, "/bin/true", "/tmp", nil); err != nil {
		t.Errorf("RunBulkCapture with no events = %v, want nil", err)
	}
}

// TestCortexCLI_EndToEnd is the integration test that proves
// bulk-capture → ingest → search composes through real subprocesses.
// This is the contract benchmarks rely on; if it breaks, all CLI
// conversions break with it.
func TestCortexCLI_EndToEnd(t *testing.T) {
	bin := sharedCortexBinary(t)
	workdir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Three events with distinctive content; we'll search for one of them.
	evs := []*events.Event{
		{
			Source:     events.SourceGeneric,
			EventType:  events.EventToolUse,
			Timestamp:  time.Now(),
			ToolName:   "chunk",
			ToolResult: "alpha bravo charlie delta",
			Context:    events.EventContext{SessionID: "test"},
		},
		{
			Source:     events.SourceGeneric,
			EventType:  events.EventToolUse,
			Timestamp:  time.Now(),
			ToolName:   "chunk",
			ToolResult: "The secret recipe code is 4F-9X-2B. It hides among lorem ipsum.",
			Context:    events.EventContext{SessionID: "test"},
		},
		{
			Source:     events.SourceGeneric,
			EventType:  events.EventToolUse,
			Timestamp:  time.Now(),
			ToolName:   "chunk",
			ToolResult: "echo foxtrot golf hotel",
			Context:    events.EventContext{SessionID: "test"},
		},
	}

	if err := RunBulkCapture(ctx, bin, workdir, evs); err != nil {
		t.Fatalf("RunBulkCapture: %v", err)
	}
	if err := RunIngest(ctx, bin, workdir); err != nil {
		t.Fatalf("RunIngest: %v", err)
	}

	out, err := RunSearch(ctx, bin, workdir, SearchFast, 5, "secret recipe code")
	if err != nil {
		t.Fatalf("RunSearch: %v", err)
	}
	if out == nil {
		t.Fatal("RunSearch returned nil output")
	}
	if len(out.Results) == 0 {
		t.Fatal("RunSearch returned 0 results; expected at least 1")
	}
	// The needle event should rank above the filler events. The exact
	// score depends on the Reflex implementation; we only assert that
	// the matching content appears somewhere in the top results, and
	// that we got the full text (no 500-char truncation).
	var found bool
	for _, r := range out.Results {
		if strings.Contains(r.Content, "4F-9X-2B") {
			found = true
			if !strings.Contains(r.Content, "hides among lorem ipsum") {
				t.Errorf("matched chunk lost trailing content (truncation?): %q", r.Content)
			}
			break
		}
	}
	if !found {
		var preview []string
		for _, r := range out.Results {
			preview = append(preview, r.Content)
		}
		t.Errorf("needle '4F-9X-2B' not found in results: %v", preview)
	}
}

// TestRunCode_Validation covers the input-checking paths that don't
// require a binary or an OpenRouter API key (a full integration would
// burn real tokens; we trust cmd/cortex/commands/code_test.go for the
// JSON-shape contract).
func TestRunCode_Validation(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		opts CodeOpts
	}{
		{"empty workdir", CodeOpts{Model: "m", Prompt: "p"}},
		{"empty model", CodeOpts{Workdir: "/w", Prompt: "p"}},
		{"empty prompt", CodeOpts{Workdir: "/w", Model: "m"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := RunCode(ctx, "/bin/true", c.opts); err == nil {
				t.Error("expected error")
			}
		})
	}
	t.Run("empty binary", func(t *testing.T) {
		if _, err := RunCode(ctx, "", CodeOpts{Workdir: "/w", Model: "m", Prompt: "p"}); err == nil {
			t.Error("expected error")
		}
	})
}

// TestRunSearch_Validation covers the input-checking paths that don't
// require a binary.
func TestRunSearch_Validation(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, binary, workdir, query string
	}{
		{"empty binary", "", "/tmp", "q"},
		{"empty workdir", "/bin/true", "", "q"},
		{"empty query", "/bin/true", "/tmp", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := RunSearch(ctx, c.binary, c.workdir, SearchFast, 5, c.query); err == nil {
				t.Error("expected error")
			}
		})
	}
}

// TestMain doesn't bother building eagerly — sharedCortexBinary handles
// the lazy build only when an integration test actually needs it.
func TestMain(m *testing.M) {
	code := m.Run()
	if sharedBinary != "" {
		os.RemoveAll(filepath.Dir(sharedBinary))
	}
	os.Exit(code)
}
