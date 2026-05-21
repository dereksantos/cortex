package llm

import (
	"sync"
	"testing"
)

func TestSwapTrackerEmpty(t *testing.T) {
	tr := NewSwapTracker()
	if got := tr.Loaded("any"); got != "" {
		t.Errorf("empty tracker should return \"\", got %q", got)
	}
	if tr.WouldSwap("any", "model") {
		t.Errorf("WouldSwap on unseen endpoint should be false")
	}
}

func TestSwapTrackerNoteAndLoaded(t *testing.T) {
	tr := NewSwapTracker()
	tr.Note("chatterbox", "Qwen3-Coder-30B")
	if got := tr.Loaded("chatterbox"); got != "Qwen3-Coder-30B" {
		t.Errorf("Loaded after Note: got %q", got)
	}
	if got := tr.Loaded("ollama"); got != "" {
		t.Errorf("Loaded on untouched endpoint should be \"\"")
	}
}

func TestSwapTrackerWouldSwap(t *testing.T) {
	tr := NewSwapTracker()
	tr.Note("chatterbox", "Qwen3-Coder-30B")
	if tr.WouldSwap("chatterbox", "Qwen3-Coder-30B") {
		t.Errorf("same model should not be a swap")
	}
	if !tr.WouldSwap("chatterbox", "Qwen3-Embedding") {
		t.Errorf("different model on same endpoint should be a swap")
	}
	if tr.WouldSwap("ollama", "anything") {
		t.Errorf("never-used endpoint should not register as swap")
	}
}

func TestSwapTrackerNilSafe(t *testing.T) {
	var tr *SwapTracker
	tr.Note("any", "any") // shouldn't panic
	if got := tr.Loaded("any"); got != "" {
		t.Errorf("nil tracker Loaded: got %q", got)
	}
	if tr.WouldSwap("any", "any") {
		t.Errorf("nil tracker WouldSwap should be false")
	}
	if snap := tr.Snapshot(); snap != nil {
		t.Errorf("nil tracker Snapshot should be nil, got %v", snap)
	}
}

func TestSwapTrackerConcurrent(t *testing.T) {
	tr := NewSwapTracker()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tr.Note("ep", "m")
			_ = tr.Loaded("ep")
		}(i)
	}
	wg.Wait()
	if got := tr.Loaded("ep"); got != "m" {
		t.Errorf("Loaded after concurrent updates: got %q", got)
	}
}

func TestSwapTrackerSnapshot(t *testing.T) {
	tr := NewSwapTracker()
	tr.Note("a", "m1")
	tr.Note("b", "m2")
	snap := tr.Snapshot()
	if len(snap) != 2 || snap["a"] != "m1" || snap["b"] != "m2" {
		t.Errorf("Snapshot: got %v", snap)
	}
	// Mutating the snapshot must not affect the tracker.
	snap["a"] = "tampered"
	if tr.Loaded("a") != "m1" {
		t.Errorf("Snapshot should be a copy, not a live view")
	}
}
