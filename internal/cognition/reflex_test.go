package cognition

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
)

// slowEmbedder mimics a saturated Ollama: every Embed call blocks until the
// passed context is cancelled or the configured delay elapses, whichever
// comes first. This reproduces the 60s stall observed when an active eval
// holds Ollama and Cortex's hot path keeps waiting on it.
type slowEmbedder struct {
	delay    time.Duration
	calls    atomic.Int32
	respects bool // if true, returns early when ctx is cancelled
}

func (s *slowEmbedder) Embed(ctx context.Context, _ string) ([]float32, error) {
	s.calls.Add(1)
	if !s.respects {
		time.Sleep(s.delay)
		return nil, context.DeadlineExceeded
	}
	select {
	case <-time.After(s.delay):
		return []float32{1, 2, 3}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *slowEmbedder) IsEmbeddingAvailable() bool { return true }

func newReflexTestStorage(t *testing.T) *storage.Storage {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "cortex-reflex-test-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tempDir) })

	store, err := storage.New(&config.Config{ContextDir: tempDir})
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestReflex_SlowEmbedder_DoesNotBlockHotPath reproduces the production
// incident: a hung Ollama caused Reflex to take 60s. Reflex must return
// quickly (well under the embedder's stall) by enforcing its own timeout.
func TestReflex_SlowEmbedder_DoesNotBlockHotPath(t *testing.T) {
	store := newReflexTestStorage(t)
	embedder := &slowEmbedder{delay: 5 * time.Second, respects: true}
	r := NewReflex(store, embedder)

	start := time.Now()
	_, err := r.Reflex(context.Background(), cognition.Query{Text: "anything"})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Reflex returned error: %v", err)
	}
	// Embedder delay is 5s, our cap is 100ms. With other Reflex work, give
	// generous headroom but well under the embedder delay.
	if elapsed > 1*time.Second {
		t.Errorf("Reflex took %v; embedder timeout is not bounding the hot path", elapsed)
	}
	if embedder.calls.Load() != 1 {
		t.Errorf("expected 1 embed call, got %d", embedder.calls.Load())
	}
}

// TestReflex_CircuitBreaker_SkipsAfterRepeatedFailures verifies that after
// embedFailureThreshold consecutive failures, subsequent calls don't even
// invoke the embedder until the cooldown elapses. Without this, every
// Reflex call would pay the per-call timeout cost while Ollama is hung.
func TestReflex_CircuitBreaker_SkipsAfterRepeatedFailures(t *testing.T) {
	store := newReflexTestStorage(t)
	// Sleep long enough that the per-call timeout always fires.
	embedder := &slowEmbedder{delay: 500 * time.Millisecond, respects: true}
	r := NewReflex(store, embedder)
	ctx := context.Background()

	// Drive the breaker open with threshold failures.
	for i := 0; i < embedFailureThreshold; i++ {
		if _, err := r.Reflex(ctx, cognition.Query{Text: "q"}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := embedder.calls.Load(); got != int32(embedFailureThreshold) {
		t.Fatalf("expected %d calls to trip breaker, got %d", embedFailureThreshold, got)
	}

	// Subsequent calls during cooldown must skip the embedder entirely.
	for i := 0; i < 5; i++ {
		if _, err := r.Reflex(ctx, cognition.Query{Text: "q"}); err != nil {
			t.Fatalf("post-trip call %d: %v", i, err)
		}
	}
	if got := embedder.calls.Load(); got != int32(embedFailureThreshold) {
		t.Errorf("circuit breaker leaked %d calls during cooldown",
			got-int32(embedFailureThreshold))
	}
}

// TestReflex_CircuitBreaker_RecoversOnSuccess verifies that a successful
// embed call resets the failure count, so transient failures don't
// permanently degrade retrieval.
func TestReflex_CircuitBreaker_RecoversOnSuccess(t *testing.T) {
	store := newReflexTestStorage(t)
	r := NewReflex(store, nil)

	// Manually drive failures up to one below threshold.
	for i := 0; i < embedFailureThreshold-1; i++ {
		r.recordEmbedFailure()
	}
	if !r.shouldTryEmbedder() {
		// embedder is nil, so shouldTryEmbedder is false regardless; that's
		// fine — what we're checking is the counter reset behavior below.
	}

	r.recordEmbedSuccess()
	if got := r.embedFailures.Load(); got != 0 {
		t.Errorf("after success, expected failures=0, got %d", got)
	}
	if got := r.embedSkipUntil.Load(); got != 0 {
		t.Errorf("after success, expected skipUntil=0, got %d", got)
	}
}
