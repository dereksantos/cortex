package commands

import (
	"bytes"
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
