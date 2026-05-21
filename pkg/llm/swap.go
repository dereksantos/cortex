// Package llm — model-swap tracker.
//
// Lemonade / single-residency endpoints (chatterbox) enforce one
// loaded model per type; switching costs 7-15s. The tracker keeps an
// in-memory record of which model each endpoint last served so the
// runtime can prefer no-swap routing when quality is comparable.
// This is a substrate for future "batch by model" decisions; the
// recommender doesn't yet consult it, but the data is captured so
// future work can ride on it.
//
// Phase 4 Slice E.

package llm

import "sync"

// SwapTracker records the last model loaded on each endpoint. Safe
// for concurrent reads + writes. Zero value is a usable empty tracker.
type SwapTracker struct {
	mu   sync.RWMutex
	last map[string]string // endpoint name → last model id used
}

// NewSwapTracker returns a fresh tracker. Convenience over the zero
// value when callers want a non-nil pointer to pass around.
func NewSwapTracker() *SwapTracker {
	return &SwapTracker{last: map[string]string{}}
}

// Note records that endpoint just served a call against model.
// Subsequent calls with a different model would trigger a swap.
// Concurrent-safe. Nil receiver is a no-op (the field passing this
// can be left nil to disable tracking entirely).
func (t *SwapTracker) Note(endpoint, model string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last == nil {
		t.last = map[string]string{}
	}
	t.last[endpoint] = model
}

// Loaded returns the last model served by endpoint, or "" if the
// tracker has never seen a call to that endpoint. Concurrent-safe.
// Nil receiver returns "".
func (t *SwapTracker) Loaded(endpoint string) string {
	if t == nil {
		return ""
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.last[endpoint]
}

// WouldSwap returns true when running model on endpoint would trigger
// a model swap on a single-residency backend (a different model is
// currently loaded). Returns false when endpoint has never been used,
// or when the same model is already loaded.
//
// Callers can use this to choose between two functionally-equivalent
// models — prefer the one that doesn't require a swap.
func (t *SwapTracker) WouldSwap(endpoint, model string) bool {
	loaded := t.Loaded(endpoint)
	return loaded != "" && loaded != model
}

// Snapshot returns a copy of the current endpoint→model map. Useful
// for debugging / telemetry. Concurrent-safe.
func (t *SwapTracker) Snapshot() map[string]string {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]string, len(t.last))
	for k, v := range t.last {
		out[k] = v
	}
	return out
}
