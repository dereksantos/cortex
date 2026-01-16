// Package llm provides LLM client implementations
package llm

import (
	"context"
	"log"
)

// FallbackEmbedder implements Embedder with fallback behavior.
// It tries the primary embedder first, falling back to secondary on failure.
type FallbackEmbedder struct {
	primary   Embedder
	secondary Embedder
	active    Embedder // tracks which embedder is currently being used
}

// NewFallbackEmbedder creates a new FallbackEmbedder that tries primary first,
// then falls back to secondary if primary is unavailable.
func NewFallbackEmbedder(primary, secondary Embedder) *FallbackEmbedder {
	return &FallbackEmbedder{
		primary:   primary,
		secondary: secondary,
	}
}

// Embed converts text to a vector embedding, trying primary first then secondary.
func (f *FallbackEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	// Try primary first if available
	if f.primary != nil && f.primary.IsEmbeddingAvailable() {
		vec, err := f.primary.Embed(ctx, text)
		if err == nil {
			if f.active != f.primary {
				f.active = f.primary
				log.Printf("[embedder] Using primary embedder")
			}
			return vec, nil
		}
		// Primary failed, log and fall through to secondary
		log.Printf("[embedder] Primary embed failed: %v, trying fallback", err)
	}

	// Fall back to secondary
	if f.secondary != nil && f.secondary.IsEmbeddingAvailable() {
		if f.active != f.secondary {
			f.active = f.secondary
			log.Printf("[embedder] Using fallback embedder")
		}
		return f.secondary.Embed(ctx, text)
	}

	// Neither available
	if f.primary != nil {
		// Return primary's embed result (which will error)
		return f.primary.Embed(ctx, text)
	}
	if f.secondary != nil {
		return f.secondary.Embed(ctx, text)
	}

	return nil, ErrNoEmbedderAvailable
}

// IsEmbeddingAvailable returns true if either primary or secondary is available.
func (f *FallbackEmbedder) IsEmbeddingAvailable() bool {
	return (f.primary != nil && f.primary.IsEmbeddingAvailable()) ||
		(f.secondary != nil && f.secondary.IsEmbeddingAvailable())
}

// ActiveEmbedder returns which embedder is currently being used.
// Returns nil if no embedding has been performed yet.
func (f *FallbackEmbedder) ActiveEmbedder() Embedder {
	return f.active
}

// ErrNoEmbedderAvailable is returned when no embedder is available.
var ErrNoEmbedderAvailable = errNoEmbedderAvailable{}

type errNoEmbedderAvailable struct{}

func (errNoEmbedderAvailable) Error() string {
	return "no embedder available"
}
