// webui_transcript_test.go — M5.2b: the session transcript view-model
// (GOAL.md §6 M5.2, split into M5.2a/b/c/d per STATE.md). Golden-pins the
// exact JSON shape buildTranscriptViewModel produces over a real JSONL
// session fixture (writeTestSession, the same helper sessions_test.go's
// TestListSessions uses), following the "golden-pinned = literal string in
// a test" convention established at M1.5/M2.5/M5.2a.
package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestBuildTranscriptViewModelGolden(t *testing.T) {
	dir := t.TempDir()
	writeTestSession(t, dir, "20260101-000000",
		`{"kind":"message","turn":0,"role":"system","content":"you are cortex"}`,
		`{"kind":"message","turn":1,"role":"user","content":"what's the module name?"}`,
		`{"kind":"message","turn":1,"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]}`,
		`{"kind":"message","turn":1,"role":"tool","tool_call_id":"call_1","content":"module cortex"}`,
		`{"kind":"message","turn":1,"role":"assistant","content":"It's cortex."}`,
		`{"kind":"state","state":{"version":1,"base":0,"frontier":5,"total_turns":1,"high_watermark":0,"low_watermark":0,"last_turn":1}}`,
	)

	vm, err := buildTranscriptViewModel(filepath.Join(dir, "20260101-000000.jsonl"))
	if err != nil {
		t.Fatalf("buildTranscriptViewModel: %v", err)
	}
	got, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	want := `{"session_id":"20260101-000000","entries":[` +
		`{"turn":1,"role":"user","content":"what's the module name?"},` +
		`{"turn":1,"role":"assistant","content":"","tool_calls":[{"id":"call_1","name":"read_file","args":"{\"path\":\"go.mod\"}"}]},` +
		`{"turn":1,"role":"tool","content":"module cortex","tool_call_id":"call_1"},` +
		`{"turn":1,"role":"assistant","content":"It's cortex."}` +
		`]}`
	if string(got) != want {
		t.Errorf("transcript view-model JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestBuildTranscriptViewModelOmitsSeedSystemMessage(t *testing.T) {
	dir := t.TempDir()
	writeTestSession(t, dir, "20260101-000000",
		`{"kind":"message","turn":0,"role":"system","content":"you are cortex"}`,
	)

	vm, err := buildTranscriptViewModel(filepath.Join(dir, "20260101-000000.jsonl"))
	if err != nil {
		t.Fatalf("buildTranscriptViewModel: %v", err)
	}
	if vm.Entries == nil {
		t.Error("Entries = nil, want an empty (non-nil) slice")
	}
	if len(vm.Entries) != 0 {
		t.Errorf("got %d entries, want 0 (system-only session)", len(vm.Entries))
	}
}

func TestBuildTranscriptViewModelMissingFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	_, err := buildTranscriptViewModel(filepath.Join(dir, "nope.jsonl"))
	if err == nil {
		t.Fatal("buildTranscriptViewModel on a missing file: got nil error, want non-nil")
	}
}
