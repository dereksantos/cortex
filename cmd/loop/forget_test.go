package main

import (
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

// TestForget_RetractsMatchingMemories checks the /forget path: it matches
// captures by substring, retracts exactly those (hiding them from future
// retrieval), and leaves non-matching memories intact.
func TestForget_RetractsMatchingMemories(t *testing.T) {
	t.Chdir(t.TempDir()) // hermetic: contextDir() resolves under the temp cwd

	dir := contextDir()
	store, err := storage.New(&config.Config{ContextDir: dir, ProjectRoot: "."})
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	now := time.Now()
	poison := &events.Event{
		ID: "e-poison", Source: events.SourceGeneric, EventType: events.EventToolUse,
		Timestamp: now, ToolName: "loop",
		ToolInput:  map[string]any{"type": "turn", "user_prompt": "hi"},
		ToolResult: "there are 936 documentation files in this project",
	}
	keep := &events.Event{
		ID: "e-keep", Source: events.SourceGeneric, EventType: events.EventToolUse,
		Timestamp: now, ToolName: "loop",
		ToolInput:  map[string]any{"type": "turn", "user_prompt": "how do we auth"},
		ToolResult: "we use JWT bearer tokens",
	}
	for _, ev := range []*events.Event{poison, keep} {
		if err := store.StoreEvent(ev); err != nil {
			t.Fatalf("StoreEvent %s: %v", ev.ID, err)
		}
	}

	cs := &CortexSession{store: store, SessionID: "t"}
	// Compound natural-language input (the real failing case): the target phrase
	// is mixed with an instruction. Term-overlap must still find the 936 capture
	// and leave the unrelated one.
	matches, err := cs.forget("936 documentation files, treat the project as a clean slate")
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if len(matches) != 1 || matches[0].ID != "e-poison" {
		t.Fatalf("matches = %v, want exactly [e-poison]", matches)
	}
	if !store.IsRetracted("event-e-poison") {
		t.Error("poison memory not retracted after /forget")
	}
	if store.IsRetracted("event-e-keep") {
		t.Error("unrelated memory was retracted — over-matched")
	}
}

// TestForget_All_KeepsRememberNotes checks the clean-slate sentinel retracts
// auto-captured turns but preserves explicit /remember notes.
func TestForget_All_KeepsRememberNotes(t *testing.T) {
	t.Chdir(t.TempDir())
	store, err := storage.New(&config.Config{ContextDir: contextDir(), ProjectRoot: "."})
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	now := time.Now()
	turn := &events.Event{
		ID: "e-turn", EventType: events.EventToolUse, Timestamp: now, ToolName: "loop",
		ToolInput: map[string]any{"type": "turn", "user_prompt": "hi"}, ToolResult: "936 docs",
	}
	note := &events.Event{
		ID: "e-note", EventType: events.EventToolUse, Timestamp: now, ToolName: "loop",
		ToolInput: map[string]any{"type": "memory"}, ToolResult: "we deploy on Fridays",
	}
	for _, ev := range []*events.Event{turn, note} {
		if err := store.StoreEvent(ev); err != nil {
			t.Fatalf("StoreEvent: %v", err)
		}
	}

	cs := &CortexSession{store: store, SessionID: "t"}
	matches, err := cs.forget("--all")
	if err != nil {
		t.Fatalf("forget --all: %v", err)
	}
	if len(matches) != 1 || matches[0].ID != "e-turn" {
		t.Fatalf("clean slate matched %v, want only the turn capture", matches)
	}
	if !store.IsRetracted("event-e-turn") {
		t.Error("turn capture not retracted by clean slate")
	}
	if store.IsRetracted("event-e-note") {
		t.Error("clean slate retracted an explicit /remember note — should keep it")
	}
}

// TestForget_NoMatches is a no-op that retracts nothing.
func TestForget_NoMatches(t *testing.T) {
	t.Chdir(t.TempDir())
	store, err := storage.New(&config.Config{ContextDir: contextDir(), ProjectRoot: "."})
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	cs := &CortexSession{store: store, SessionID: "t"}
	matches, err := cs.forget("nonexistent phrase xyzzy")
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("matches = %v, want none", matches)
	}
}
