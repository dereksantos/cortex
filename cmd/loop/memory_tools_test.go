package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/cmd/loop/tools"
	"github.com/dereksantos/cortex/internal/projectindex"
)

// memCall builds a ToolCall for a memory tool with the given JSON args.
func memCall(name string, args map[string]any) ToolCall {
	raw, _ := json.Marshal(args)
	return ToolCall{Function: FunctionCall{Name: name, Arguments: string(raw)}}
}

// TestMemoryToolsRegistered guards the wiring: the four memory tools must be in
// the advertised tool set and dispatch through Execute against a live session.
func TestMemoryToolsRegistered(t *testing.T) {
	want := map[string]bool{
		tools.FunctionMemoryWrite:  false,
		tools.FunctionMemoryRead:   false,
		tools.FunctionMemorySearch: false,
		tools.FunctionMemoryForget: false,
	}
	for _, tool := range tools.All {
		if _, ok := want[tool.Function.Name]; ok {
			want[tool.Function.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("memory tool %q not registered in tools.All", name)
		}
	}
}

// TestMemoryTools_RoundTrip drives the full write→index→read→search→forget loop
// through the tool dispatcher against a real on-disk store — the P1 contract.
func TestMemoryTools_RoundTrip(t *testing.T) {
	t.Chdir(t.TempDir())
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.EnableMemory()
	if cs.memory == nil {
		t.Fatal("EnableMemory should wire a memory store in a writable dir")
	}
	ctx := context.Background()

	// write
	out, err := memCall(tools.FunctionMemoryWrite, map[string]any{
		"name": "Auth Decision", "content": "We use JWT, not server sessions. Why: stateless scaling.",
	}).Execute(ctx, cs)
	if err != nil {
		t.Fatalf("memory_write: %v", err)
	}
	if !strings.Contains(out, "auth-decision") {
		t.Errorf("write result should confirm the normalized name, got %q", out)
	}

	// the index injection now surfaces the note
	note := cs.memoryIndexNote()
	if !strings.Contains(note, "auth-decision") || !strings.Contains(note, "memory_read") {
		t.Errorf("index note missing the new note or the read nudge:\n%s", note)
	}

	// read
	body, err := memCall(tools.FunctionMemoryRead, map[string]any{"name": "auth-decision"}).Execute(ctx, cs)
	if err != nil {
		t.Fatalf("memory_read: %v", err)
	}
	if !strings.Contains(body, "JWT") {
		t.Errorf("read should return the note body, got %q", body)
	}

	// search (keyword)
	hits, err := memCall(tools.FunctionMemorySearch, map[string]any{"query": "jwt"}).Execute(ctx, cs)
	if err != nil {
		t.Fatalf("memory_search: %v", err)
	}
	if !strings.Contains(hits, "auth-decision") {
		t.Errorf("search for 'jwt' should find the note, got %q", hits)
	}

	// forget
	gone, err := memCall(tools.FunctionMemoryForget, map[string]any{"name": "auth-decision"}).Execute(ctx, cs)
	if err != nil {
		t.Fatalf("memory_forget: %v", err)
	}
	if !strings.Contains(gone, "forgot") {
		t.Errorf("forget should confirm removal, got %q", gone)
	}
	if cs.memoryIndexNote() != "" {
		t.Errorf("index should be empty after forgetting the only note, got %q", cs.memoryIndexNote())
	}
}

// TestMemoryReadMissingIsObservation: reading an absent note returns a friendly
// observation, not an error — the model should adapt, not abort the turn.
func TestMemoryReadMissingIsObservation(t *testing.T) {
	t.Chdir(t.TempDir())
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.EnableMemory()

	out, err := memCall(tools.FunctionMemoryRead, map[string]any{"name": "nope"}).Execute(context.Background(), cs)
	if err != nil {
		t.Fatalf("missing read should not error: %v", err)
	}
	if !strings.Contains(out, "no note named") {
		t.Errorf("missing read should explain itself, got %q", out)
	}
}

// TestMemoryUnavailableIsObservation: with no store wired, the tools report
// unavailability as a result the model can read — never a fatal error.
func TestMemoryUnavailableIsObservation(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request()} // no EnableMemory → memory == nil
	out, err := memCall(tools.FunctionMemoryWrite, map[string]any{"name": "x", "content": "y"}).Execute(context.Background(), cs)
	if err != nil {
		t.Fatalf("unavailable memory should not error: %v", err)
	}
	if !strings.Contains(out, "unavailable") {
		t.Errorf("expected an unavailability observation, got %q", out)
	}
	if cs.memoryIndexNote() != "" {
		t.Errorf("no store → no index injection, got %q", cs.memoryIndexNote())
	}
}

// TestMemoryWriteUpdatesNotDuplicates: writing an existing name updates the note
// in place (one file), the no-duplicate contract the model relies on.
func TestMemoryWriteUpdatesNotDuplicates(t *testing.T) {
	t.Chdir(t.TempDir())
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.EnableMemory()
	ctx := context.Background()

	w := func(content string) {
		if _, err := memCall(tools.FunctionMemoryWrite, map[string]any{"name": "db", "content": content}).Execute(ctx, cs); err != nil {
			t.Fatal(err)
		}
	}
	w("first")
	w("second, corrected")

	body, _ := memCall(tools.FunctionMemoryRead, map[string]any{"name": "db"}).Execute(ctx, cs)
	if !strings.Contains(body, "corrected") || strings.Contains(body, "first") {
		t.Errorf("update should replace, not append: %q", body)
	}
	// exactly one note file (plus INDEX.md) on disk
	files, _ := filepath.Glob(filepath.Join(".cortex", "memory", "*.md"))
	var notes int
	for _, f := range files {
		if filepath.Base(f) != "INDEX.md" {
			notes++
		}
	}
	if notes != 1 {
		t.Errorf("expected 1 note file after an update, got %d (%v)", notes, files)
	}
}

// TestStudyJournalPathNavigable is the P2 guarantee: study(.cortex/journal …)
// must be able to map the journal even though .cortex is gitignored. The ignore
// set keys off the SCAN ROOT, so an explicitly-given .cortex/journal root is not
// self-excluded and its files appear in the structural map.
func TestStudyJournalPathNavigable(t *testing.T) {
	root := t.TempDir()
	jdir := filepath.Join(root, ".cortex", "journal", "coding")
	if err := os.MkdirAll(jdir, 0o755); err != nil {
		t.Fatal(err)
	}
	seg := filepath.Join(jdir, "20260626T000000Z.jsonl")
	if err := os.WriteFile(seg, []byte(`{"event":"turn","user":"hello"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	ix, err := projectindex.Build(filepath.Join(".cortex", "journal"))
	if err != nil {
		t.Fatalf("project_index on .cortex/journal: %v", err)
	}
	if len(ix.Files) == 0 {
		t.Fatal("journal map is empty — .cortex/journal was self-excluded, so study(journal) can't navigate it")
	}
	if !strings.Contains(ix.Render(), "20260626T000000Z.jsonl") {
		t.Errorf("journal segment missing from the map:\n%s", ix.Render())
	}
}
