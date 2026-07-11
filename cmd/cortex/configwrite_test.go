package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// jsonEqual reports whether two JSON encodings decode to the same
// value, ignoring whitespace/formatting differences. Used to assert
// "survives byte-for-byte" in the sense GOAL.md §2 actually prescribes:
// read-modify-write over json.RawMessage never drops or mutates a field
// it didn't touch — encoding/json's own Marshal normalizes whitespace
// even for untouched json.RawMessage values (verified: it compacts
// nested raw content), so exact-byte comparison of formatting isn't a
// property the prescribed map[string]any/json.RawMessage mechanism can
// offer; value equality is the meaningful, testable invariant.
func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("jsonEqual: unmarshal a: %v", err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("jsonEqual: unmarshal b: %v", err)
	}
	return reflect.DeepEqual(va, vb)
}

// TestSetJSONPathPreservesUnknownFieldsByteForByte pins the
// read-modify-write invariant (GOAL.md §2): writing one nested path
// leaves every other top-level field's value untouched, including a
// field this package has no schema for at all.
func TestSetJSONPathPreservesUnknownFieldsByteForByte(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := `{
  "backend": {"type": "ollama", "endpoint": "http://localhost:11434"},
  "models": {"study": {"model": "reasoner-9b", "window": 32768}},
  "tools": {"allow_delete": false},
  "an_unknown_future_field": {"nested": [1, 2, 3], "flag": true}
}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	doc, err := readJSONDoc(path)
	if err != nil {
		t.Fatalf("readJSONDoc: %v", err)
	}
	if err := setJSONPath(doc, []string{"backend", "type"}, "openrouter"); err != nil {
		t.Fatalf("setJSONPath: %v", err)
	}
	if err := setJSONPath(doc, []string{"models", roleCode, "model"}, "openrouter/free"); err != nil {
		t.Fatalf("setJSONPath: %v", err)
	}
	if err := writeJSONDoc(path, doc); err != nil {
		t.Fatalf("writeJSONDoc: %v", err)
	}

	var got map[string]json.RawMessage
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// The untouched top-level field this package has no schema for at
	// all must survive with its value intact.
	wantUnknown := json.RawMessage(`{"nested": [1, 2, 3], "flag": true}`)
	if !jsonEqual(t, got["an_unknown_future_field"], wantUnknown) {
		t.Errorf("an_unknown_future_field = %s, want %s", got["an_unknown_future_field"], wantUnknown)
	}

	// tools survives untouched too.
	wantTools := json.RawMessage(`{"allow_delete": false}`)
	if !jsonEqual(t, got["tools"], wantTools) {
		t.Errorf("tools = %s, want %s", got["tools"], wantTools)
	}

	// Sibling role under "models" (study) survives untouched, while the
	// touched role (code) picks up the new value.
	var models map[string]json.RawMessage
	if err := json.Unmarshal(got["models"], &models); err != nil {
		t.Fatalf("Unmarshal models: %v", err)
	}
	wantStudy := json.RawMessage(`{"model": "reasoner-9b", "window": 32768}`)
	if !jsonEqual(t, models["study"], wantStudy) {
		t.Errorf("models.study = %s, want %s", models["study"], wantStudy)
	}
	var code struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(models[roleCode], &code); err != nil {
		t.Fatalf("Unmarshal models.code: %v", err)
	}
	if code.Model != "openrouter/free" {
		t.Errorf("models.code.model = %q, want %q", code.Model, "openrouter/free")
	}

	// Sibling field under "backend" (endpoint) survives untouched, while
	// the touched field (type) picks up the new value.
	var backend struct {
		Type     string `json:"type"`
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal(got["backend"], &backend); err != nil {
		t.Fatalf("Unmarshal backend: %v", err)
	}
	if backend.Type != "openrouter" {
		t.Errorf("backend.type = %q, want %q", backend.Type, "openrouter")
	}
	if backend.Endpoint != "http://localhost:11434" {
		t.Errorf("backend.endpoint = %q, want unchanged %q", backend.Endpoint, "http://localhost:11434")
	}
}

// TestReadJSONDocMissingFileIsEmptyDoc pins that a not-yet-created
// config file is a valid read-modify-write starting point (first
// bootstrap run, no ~/.cortex/config.json yet).
func TestReadJSONDocMissingFileIsEmptyDoc(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")

	doc, err := readJSONDoc(path)
	if err != nil {
		t.Fatalf("readJSONDoc: %v", err)
	}
	if len(doc) != 0 {
		t.Errorf("readJSONDoc(missing) = %v, want empty", doc)
	}
}
