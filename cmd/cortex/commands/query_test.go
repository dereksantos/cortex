package commands

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
)

// TestOpenWorkdirContext verifies the --workdir resolver wires a config
// rooted at <workdir>/.cortex and returns a usable storage handle. The
// invariant: benchmarks calling search/ingest with --workdir get a
// store that never touches the user's global ~/.cortex.
func TestOpenWorkdirContext(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		workdir := t.TempDir()
		cfg, store, err := openWorkdirContext(workdir)
		if err != nil {
			t.Fatalf("openWorkdirContext: %v", err)
		}
		defer store.Close()

		wantCtxDir := filepath.Join(workdir, ".cortex")
		if cfg.ContextDir != wantCtxDir {
			t.Errorf("ContextDir = %q, want %q", cfg.ContextDir, wantCtxDir)
		}
		if cfg.ProjectRoot != workdir {
			t.Errorf("ProjectRoot = %q, want %q", cfg.ProjectRoot, workdir)
		}
		if store == nil {
			t.Fatal("storage is nil")
		}
	})

	t.Run("empty workdir errors", func(t *testing.T) {
		if _, _, err := openWorkdirContext(""); err == nil {
			t.Fatal("expected error for empty workdir")
		}
		if _, _, err := openWorkdirContext("   "); err == nil {
			t.Fatal("expected error for whitespace-only workdir")
		}
	})
}

// TestEmitSearchJSON verifies the structured-output contract: stable
// keys, full content (no truncation), nil result -> empty array.
// Benchmarks parse this; the schema is part of the public CLI contract.
func TestEmitSearchJSON(t *testing.T) {
	t.Run("nil result emits empty array", func(t *testing.T) {
		var buf bytes.Buffer
		if err := emitSearchJSON(&buf, "fast", 12*time.Millisecond, nil); err != nil {
			t.Fatalf("emitSearchJSON: %v", err)
		}
		var got searchJSONOutput
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Mode != "fast" {
			t.Errorf("Mode = %q, want %q", got.Mode, "fast")
		}
		if got.ElapsedMs != 12 {
			t.Errorf("ElapsedMs = %d, want 12", got.ElapsedMs)
		}
		if got.Results == nil {
			t.Error("Results is nil, want empty slice")
		}
		if len(got.Results) != 0 {
			t.Errorf("len(Results) = %d, want 0", len(got.Results))
		}
	})

	t.Run("full content preserved (no truncation)", func(t *testing.T) {
		// A chunk larger than the human-readable mode's 500-char preview.
		// Benchmarks score against the full string; truncation would
		// silently break substring assertions.
		longContent := strings.Repeat("haystack ", 200) // 1800 chars
		result := &cognition.ResolveResult{
			Results: []cognition.Result{
				{Score: 0.95, Content: longContent + " The secret recipe code is 4F-9X-2B."},
				{Score: 0.42, Content: "runner-up"},
			},
		}
		var buf bytes.Buffer
		if err := emitSearchJSON(&buf, "full", 200*time.Millisecond, result); err != nil {
			t.Fatalf("emitSearchJSON: %v", err)
		}
		var got searchJSONOutput
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(got.Results) != 2 {
			t.Fatalf("len(Results) = %d, want 2", len(got.Results))
		}
		if got.Results[0].Score != 0.95 {
			t.Errorf("Results[0].Score = %v, want 0.95", got.Results[0].Score)
		}
		if !strings.Contains(got.Results[0].Content, "The secret recipe code is 4F-9X-2B.") {
			t.Error("Results[0].Content lost the needle — truncation regression")
		}
		if len(got.Results[0].Content) != len(longContent)+len(" The secret recipe code is 4F-9X-2B.") {
			t.Errorf("Results[0].Content length = %d, want %d (full content)",
				len(got.Results[0].Content),
				len(longContent)+len(" The secret recipe code is 4F-9X-2B."))
		}
	})
}
