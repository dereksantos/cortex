// Package llm provides LLM client implementations
package llm

import "context"

// Provider defines the interface for LLM backends
type Provider interface {
	// Generate produces a response for the given prompt
	Generate(ctx context.Context, prompt string) (string, error)

	// GenerateWithSystem includes system context (for context injection)
	GenerateWithSystem(ctx context.Context, prompt, system string) (string, error)

	// IsAvailable checks if the provider is ready
	IsAvailable() bool

	// Name returns the provider identifier
	Name() string
}

// GenerateRequest represents a generation request
type GenerateRequest struct {
	Prompt string
	System string // Optional system/context message
}

// GenerateResponse represents a generation response
type GenerateResponse struct {
	Output  string
	Model   string
	Latency int64 // milliseconds
}
