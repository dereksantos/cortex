package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestEmitEmbedJSON pins the --json contract for the default
// (text → vector) mode. Keys + types are the public contract
// downstream consumers (benchmarks, scripts) parse.
func TestEmitEmbedJSON(t *testing.T) {
	vec := []float32{0.1, -0.2, 0.3, 0.4}
	var buf bytes.Buffer
	if err := emitEmbedJSON(&buf, vec, "nomic-embed-text", "ollama"); err != nil {
		t.Fatalf("emitEmbedJSON: %v", err)
	}
	var got embedJSONOutput
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := embedJSONOutput{
		Vector:   vec,
		Dim:      4,
		Model:    "nomic-embed-text",
		Provider: "ollama",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("emitEmbedJSON output mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

// TestEmitEmbedJSON_FullVectorPreserved ensures vectors of realistic
// size (768 dims for nomic-embed-text) round-trip without truncation
// or precision loss past JSON's float64 floor.
func TestEmitEmbedJSON_FullVectorPreserved(t *testing.T) {
	vec := make([]float32, 768)
	for i := range vec {
		vec[i] = float32(i) / 1000.0
	}
	var buf bytes.Buffer
	if err := emitEmbedJSON(&buf, vec, "m", "p"); err != nil {
		t.Fatalf("emitEmbedJSON: %v", err)
	}
	var got embedJSONOutput
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Dim != 768 {
		t.Errorf("Dim = %d, want 768", got.Dim)
	}
	if len(got.Vector) != 768 {
		t.Errorf("len(Vector) = %d, want 768", len(got.Vector))
	}
	// Spot-check a midpoint value (float32 → JSON number → float32 round trip).
	if diff := got.Vector[500] - 0.5; diff > 1e-5 || diff < -1e-5 {
		t.Errorf("Vector[500] = %v, want ≈0.5", got.Vector[500])
	}
}

// TestEmitEmbedStoreJSON pins the --store mode contract.
func TestEmitEmbedStoreJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := emitEmbedStoreJSON(&buf, "doc-42", "corpus", "nomic-embed-text", "ollama", 768); err != nil {
		t.Fatalf("emitEmbedStoreJSON: %v", err)
	}
	var got embedStoreJSONOutput
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := embedStoreJSONOutput{
		Stored:      true,
		DocID:       "doc-42",
		ContentType: "corpus",
		Dim:         768,
		Model:       "nomic-embed-text",
		Provider:    "ollama",
	}
	if got != want {
		t.Errorf("emitEmbedStoreJSON output mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

// fakeEmbedder is a deterministic stand-in for tests that exercise the
// store/bulk paths without spinning up Ollama or downloading Hugot.
// It writes len(text)-sensitive vectors so different inputs produce
// different vectors (verifies the bulk path doesn't dedup or reuse).
type fakeEmbedder struct{ dim int }

func (f *fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, f.dim)
	for i := range vec {
		vec[i] = float32(len(text)+i) / 100.0
	}
	return vec, nil
}
func (f *fakeEmbedder) IsEmbeddingAvailable() bool { return true }

// TestExecuteBulkEmbed verifies the NDJSON-stdin path embeds and stores
// each request, emits a final summary, and rejects malformed input
// with a line number. The storage write itself is covered by
// internal/storage tests; here we just exercise the CLI handler.
func TestExecuteBulkEmbed(t *testing.T) {
	t.Run("happy path stores all + emits summary", func(t *testing.T) {
		workdir := t.TempDir()
		embedder := &fakeEmbedder{dim: 8}
		input := strings.Join([]string{
			`{"doc_id":"d1","text":"alpha"}`,
			`{"doc_id":"d2","text":"bravo charlie","content_type":"corpus"}`,
			`{"doc_id":"d3","text":"delta"}`,
		}, "\n") + "\n"
		var out bytes.Buffer
		err := executeBulkEmbed(workdir, "corpus", embedder, "test-model", "local", strings.NewReader(input), &out)
		if err != nil {
			t.Fatalf("executeBulkEmbed: %v", err)
		}
		var got bulkEmbedSummary
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal summary: %v", err)
		}
		want := bulkEmbedSummary{Stored: 3, Model: "test-model", Provider: "local", Dim: 8}
		if got != want {
			t.Errorf("summary = %+v, want %+v", got, want)
		}
	})

	t.Run("missing doc_id names line", func(t *testing.T) {
		workdir := t.TempDir()
		input := `{"doc_id":"d1","text":"ok"}` + "\n" + `{"text":"no id"}` + "\n"
		err := executeBulkEmbed(workdir, "corpus", &fakeEmbedder{dim: 4}, "m", "p", strings.NewReader(input), &bytes.Buffer{})
		if err == nil {
			t.Fatal("expected error for missing doc_id on line 2")
		}
		if !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "doc_id") {
			t.Errorf("error %q should mention line 2 + doc_id", err)
		}
	})

	t.Run("empty text rejected per line", func(t *testing.T) {
		workdir := t.TempDir()
		input := `{"doc_id":"d1","text":""}` + "\n"
		err := executeBulkEmbed(workdir, "corpus", &fakeEmbedder{dim: 4}, "m", "p", strings.NewReader(input), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "text") {
			t.Errorf("expected text-required error, got %v", err)
		}
	})

	t.Run("blank lines skipped", func(t *testing.T) {
		workdir := t.TempDir()
		input := `{"doc_id":"d1","text":"alpha"}` + "\n\n" + `{"doc_id":"d2","text":"bravo"}` + "\n"
		var out bytes.Buffer
		if err := executeBulkEmbed(workdir, "corpus", &fakeEmbedder{dim: 4}, "m", "p", strings.NewReader(input), &out); err != nil {
			t.Fatalf("executeBulkEmbed: %v", err)
		}
		var got bulkEmbedSummary
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Stored != 2 {
			t.Errorf("Stored = %d, want 2 (blank line must be skipped, not counted)", got.Stored)
		}
	})
}

// TestEmbedCommand_Validation covers the flag-validation paths so a
// typo in a benchmark wrapper fails with a clear error, not a panic.
func TestEmbedCommand_Validation(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		expect string // substring of expected error message
	}{
		{"missing --text", []string{}, "--text"},
		{"--store without --workdir", []string{"--text", "hello", "--store", "--doc-id", "d1"}, "--workdir"},
		{"--store without --doc-id", []string{"--text", "hello", "--store", "--workdir", "/tmp/x"}, "--doc-id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := &EmbedCommand{}
			err := cmd.Execute(&Context{Args: c.args})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), c.expect) {
				t.Errorf("error %q does not mention %q", err, c.expect)
			}
		})
	}
}
