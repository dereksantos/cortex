package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
)

// TestEmitSearchVectorJSON pins the public CLI contract: stable keys,
// content-type filtering, top-k truncation, and ensures Results is
// always a JSON array (never null) so downstream parsers don't need to
// handle both shapes.
func TestEmitSearchVectorJSON(t *testing.T) {
	t.Run("happy path with filter", func(t *testing.T) {
		results := []storage.VectorSearchResult{
			{ContentID: "doc-1", ContentType: "corpus", Similarity: 0.95},
			{ContentID: "ev-9", ContentType: "event", Similarity: 0.91, Content: "event text"},
			{ContentID: "doc-2", ContentType: "corpus", Similarity: 0.88},
		}
		var buf bytes.Buffer
		if err := emitSearchVectorJSON(&buf, results, "corpus", 10, 17*time.Millisecond, "nomic-embed-text", "ollama"); err != nil {
			t.Fatalf("emit: %v", err)
		}
		var got searchVectorOutput
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.K != 10 {
			t.Errorf("K=%d want 10", got.K)
		}
		if got.ElapsedMs != 17 {
			t.Errorf("ElapsedMs=%d want 17", got.ElapsedMs)
		}
		if got.Model != "nomic-embed-text" || got.Provider != "ollama" {
			t.Errorf("model/provider=%q/%q want nomic-embed-text/ollama", got.Model, got.Provider)
		}
		if len(got.Results) != 2 {
			t.Fatalf("got %d results after corpus filter, want 2", len(got.Results))
		}
		if got.Results[0].ContentID != "doc-1" {
			t.Errorf("Results[0].ContentID=%q want doc-1", got.Results[0].ContentID)
		}
		if got.Results[1].ContentID != "doc-2" {
			t.Errorf("Results[1].ContentID=%q want doc-2", got.Results[1].ContentID)
		}
	})

	t.Run("top-k truncates", func(t *testing.T) {
		var results []storage.VectorSearchResult
		for i := 0; i < 20; i++ {
			results = append(results, storage.VectorSearchResult{
				ContentID:   "d-" + string(rune('a'+i)),
				ContentType: "corpus",
				Similarity:  1.0 - float64(i)*0.01,
			})
		}
		var buf bytes.Buffer
		if err := emitSearchVectorJSON(&buf, results, "", 5, 0, "", ""); err != nil {
			t.Fatalf("emit: %v", err)
		}
		var got searchVectorOutput
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(got.Results) != 5 {
			t.Errorf("len(Results)=%d want 5 (top-k)", len(got.Results))
		}
	})

	t.Run("empty results emits empty array not null", func(t *testing.T) {
		var buf bytes.Buffer
		if err := emitSearchVectorJSON(&buf, nil, "", 10, 0, "", ""); err != nil {
			t.Fatalf("emit: %v", err)
		}
		// Parse as a raw map so we see how the field landed.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
			t.Fatalf("unmarshal raw: %v", err)
		}
		gotResults := string(raw["results"])
		if gotResults != "[]" {
			t.Errorf("results = %q, want \"[]\" (downstream parsers shouldn't need to handle null)", gotResults)
		}
	})

	t.Run("model/provider omitted when vector-mode", func(t *testing.T) {
		// --vector mode passes empty model/provider — they must omit
		// from JSON (omitempty) so the schema cleanly distinguishes
		// "embedded by CLI" from "caller supplied vector".
		var buf bytes.Buffer
		results := []storage.VectorSearchResult{{ContentID: "d1", ContentType: "corpus", Similarity: 0.5}}
		if err := emitSearchVectorJSON(&buf, results, "", 5, 0, "", ""); err != nil {
			t.Fatalf("emit: %v", err)
		}
		s := buf.String()
		if strings.Contains(s, "\"model\"") {
			t.Errorf("model key should be omitted when empty; got %s", s)
		}
		if strings.Contains(s, "\"provider\"") {
			t.Errorf("provider key should be omitted when empty; got %s", s)
		}
	})
}

// TestSearchVectorCommand_Validation covers the flag-validation paths.
func TestSearchVectorCommand_Validation(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		expect string
	}{
		{"missing --workdir", []string{"--text", "x"}, "--workdir"},
		{"missing query", []string{"--workdir", "/tmp"}, "--text or --vector"},
		{"both --text and --vector", []string{"--workdir", "/tmp", "--text", "x", "--vector", "[0.1]"}, "--text or --vector"},
		{"top-k zero", []string{"--workdir", "/tmp", "--text", "x", "--top-k", "0"}, "top-k"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := &SearchVectorCommand{}
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
