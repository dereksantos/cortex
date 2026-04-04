// Package llm provides LLM client implementations
package llm

import (
	"context"
	"strings"
	"time"
)

// MockProvider returns predictable responses for testing eval framework
type MockProvider struct {
	latency time.Duration
}

// NewMockProvider creates a mock provider with simulated latency
func NewMockProvider(latencyMs int) *MockProvider {
	return &MockProvider{
		latency: time.Duration(latencyMs) * time.Millisecond,
	}
}

// Name returns the provider identifier
func (m *MockProvider) Name() string {
	return "mock"
}

// IsAvailable always returns true for mock
func (m *MockProvider) IsAvailable() bool {
	return true
}

// Generate produces a mock response based on prompt keywords
func (m *MockProvider) Generate(ctx context.Context, prompt string) (string, error) {
	time.Sleep(m.latency)
	return m.generateResponse(prompt, ""), nil
}

// GenerateWithSystem generates with context - produces better mock responses
func (m *MockProvider) GenerateWithSystem(ctx context.Context, prompt, system string) (string, error) {
	time.Sleep(m.latency)
	return m.generateResponse(prompt, system), nil
}

// GenerateWithStats produces a mock response with synthetic token counts.
func (m *MockProvider) GenerateWithStats(ctx context.Context, prompt string) (string, GenerationStats, error) {
	time.Sleep(m.latency)
	response := m.generateResponse(prompt, "")
	stats := GenerationStats{
		InputTokens:  len(prompt) / 4,
		OutputTokens: len(response) / 4,
	}
	return response, stats, nil
}

// generateResponse creates a mock response based on keywords in prompt and context
func (m *MockProvider) generateResponse(prompt, context string) string {
	promptLower := strings.ToLower(prompt)
	contextLower := strings.ToLower(context)
	combined := promptLower + " " + contextLower

	// LLM-as-Judge scoring - check first before other patterns
	// Detect by looking for "evaluating whether an ai response" or judge evaluation criteria
	if strings.Contains(promptLower, "evaluating whether an ai response") ||
		strings.Contains(promptLower, "response to evaluate") ||
		(strings.Contains(promptLower, "correctness") && strings.Contains(promptLower, "understanding") && strings.Contains(promptLower, "hallucination")) {
		return `{"correctness": 0.75, "understanding": 0.80, "hallucination": 0.15, "explanation": "Response appears to be partially correct based on mock evaluation."}`
	}

	// Auth/JWT related
	if strings.Contains(combined, "jwt") || strings.Contains(combined, "auth") {
		if strings.Contains(promptLower, "password reset") {
			return "For password reset, generate a short-lived JWT token with expiry of 1 hour. Send this token via email to the user. When they click the link, validate the token and allow password change."
		}
		if strings.Contains(promptLower, "refresh") {
			return "Implement refresh tokens by issuing a long-lived refresh token alongside the access token. Store refresh tokens securely and use them to obtain new access tokens."
		}
		return "Use JWT tokens for authentication. Include user_id in claims, set appropriate expiry, and use HS256 signing."
	}

	// Database related
	if strings.Contains(combined, "database") || strings.Contains(combined, "sql") || strings.Contains(combined, "postgres") {
		if strings.Contains(promptLower, "migration") {
			return "Use goose for database migrations. Create migration files in db/migrations/ directory. Run migrations with goose up command."
		}
		if strings.Contains(promptLower, "connection") {
			return "Use database/sql with connection pooling. Set max open connections and idle timeout appropriately."
		}
		return "Use PostgreSQL for the database. Follow the established patterns with sqlx for queries."
	}

	// Error handling
	if strings.Contains(combined, "error") {
		if strings.Contains(contextLower, "wrap") || strings.Contains(contextLower, "pkg/errors") {
			return "Wrap errors with context using errors.Wrapf(err, \"description\"). Never return bare errors. Always add context at boundaries."
		}
		return "Handle errors explicitly. Return errors up the call stack with appropriate context."
	}

	// Go idioms
	if strings.Contains(combined, "logging") || strings.Contains(combined, "log") {
		if strings.Contains(contextLower, "slog") {
			return "Use slog for structured logging. Include request_id in all log entries. Use slog.Info, slog.Error with key-value pairs."
		}
		return "Use structured logging with appropriate log levels."
	}

	// Testing
	if strings.Contains(promptLower, "test") {
		return "Write table-driven tests using t.Run for subtests. Use the []struct pattern for test cases."
	}

	// Code review judge - detect by looking for "acceptance criteria" or "evaluate each criterion"
	if strings.Contains(combined, "Acceptance Criteria") ||
		strings.Contains(combined, "acceptance criteria") ||
		strings.Contains(combined, "Evaluate each criterion") ||
		strings.Contains(combined, "evaluate each criterion") {
		return `{
  "evaluations": [
    {
      "criterion": "Sample acceptance criterion",
      "passed": true,
      "confidence": 0.95,
      "reasoning": "The code appears to meet this criterion based on the implementation."
    }
  ]
}`
	}

	// Default response
	return "Based on the project context, follow established patterns and conventions. Ensure consistency with existing code."
}
