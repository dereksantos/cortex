package llm

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBuildAnalysisPrompt(t *testing.T) {
	tests := []struct {
		name       string
		toolName   string
		filePath   string
		toolResult string
		wantParts  []string
	}{
		{
			name:       "includes all fields",
			toolName:   "Edit",
			filePath:   "/src/main.go",
			toolResult: "modified auth module",
			wantParts:  []string{"Tool: Edit", "File: /src/main.go", "Result: modified auth module"},
		},
		{
			name:       "handles empty file path",
			toolName:   "Bash",
			filePath:   "",
			toolResult: "ran tests",
			wantParts:  []string{"Tool: Bash", "File: ", "Result: ran tests"},
		},
		{
			name:       "includes JSON format instructions",
			toolName:   "Write",
			filePath:   "/test.go",
			toolResult: "created file",
			wantParts:  []string{"JSON format", "summary", "category", "importance", "tags", "reasoning"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildAnalysisPrompt(tt.toolName, tt.filePath, tt.toolResult)

			for _, part := range tt.wantParts {
				if !strings.Contains(result, part) {
					t.Errorf("prompt missing expected part %q", part)
				}
			}
		})
	}
}

func TestParseAnalysisJSON(t *testing.T) {
	tests := []struct {
		name           string
		response       string
		wantSummary    string
		wantCategory   string
		wantImportance int
		wantTags       int
		wantNil        bool
	}{
		{
			name: "parses valid JSON",
			response: `{
				"summary": "Added JWT authentication",
				"category": "decision",
				"importance": 8,
				"tags": ["auth", "security"],
				"reasoning": "Chose JWT for stateless auth"
			}`,
			wantSummary:    "Added JWT authentication",
			wantCategory:   "decision",
			wantImportance: 8,
			wantTags:       2,
		},
		{
			name: "parses JSON with surrounding text",
			response: `Here's my analysis:
			{
				"summary": "Refactored error handling",
				"category": "pattern",
				"importance": 6,
				"tags": ["errors"],
				"reasoning": "Better error context"
			}
			Hope this helps!`,
			wantSummary:    "Refactored error handling",
			wantCategory:   "pattern",
			wantImportance: 6,
			wantTags:       1,
		},
		{
			name:     "returns nil for no JSON",
			response: "This response has no JSON at all",
			wantNil:  true,
		},
		{
			name:     "returns nil for empty response",
			response: "",
			wantNil:  true,
		},
		{
			name:     "returns nil for malformed JSON",
			response: `{"summary": "broken JSON, "missing": quote}`,
			wantNil:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := parseAnalysisJSON(tt.response)

			if tt.wantNil {
				if result != nil {
					t.Error("expected nil result")
				}
				return
			}

			if result == nil {
				t.Fatal("unexpected nil result")
			}

			if result.Summary != tt.wantSummary {
				t.Errorf("expected summary %q, got %q", tt.wantSummary, result.Summary)
			}
			if result.Category != tt.wantCategory {
				t.Errorf("expected category %q, got %q", tt.wantCategory, result.Category)
			}
			if result.Importance != tt.wantImportance {
				t.Errorf("expected importance %d, got %d", tt.wantImportance, result.Importance)
			}
			if len(result.Tags) != tt.wantTags {
				t.Errorf("expected %d tags, got %d", tt.wantTags, len(result.Tags))
			}
		})
	}
}

func TestParseAnalysisWithFallback(t *testing.T) {
	t.Run("returns parsed result for valid JSON", func(t *testing.T) {
		response := `{"summary": "Valid", "category": "decision", "importance": 7, "tags": [], "reasoning": "test"}`
		result := ParseAnalysisWithFallback(response)

		if result.Summary != "Valid" {
			t.Errorf("expected summary 'Valid', got %q", result.Summary)
		}
		if result.Category != "decision" {
			t.Errorf("expected category 'decision', got %q", result.Category)
		}
	})

	t.Run("returns fallback for invalid JSON", func(t *testing.T) {
		response := "This is not JSON at all"
		result := ParseAnalysisWithFallback(response)

		if result.Summary != response {
			t.Errorf("expected summary to be original response")
		}
		if result.Category != "insight" {
			t.Errorf("expected fallback category 'insight', got %q", result.Category)
		}
		if result.Importance != 5 {
			t.Errorf("expected fallback importance 5, got %d", result.Importance)
		}
		if result.Reasoning != "Could not parse structured response" {
			t.Errorf("expected fallback reasoning, got %q", result.Reasoning)
		}
	})

	t.Run("returns fallback for empty response", func(t *testing.T) {
		result := ParseAnalysisWithFallback("")

		if result.Category != "insight" {
			t.Errorf("expected fallback category")
		}
	})
}

func TestMockProvider(t *testing.T) {
	t.Run("Name returns mock", func(t *testing.T) {
		provider := NewMockProvider(0)
		if provider.Name() != "mock" {
			t.Errorf("expected name 'mock', got %q", provider.Name())
		}
	})

	t.Run("IsAvailable returns true", func(t *testing.T) {
		provider := NewMockProvider(0)
		if !provider.IsAvailable() {
			t.Error("expected IsAvailable to return true")
		}
	})

	t.Run("Generate returns response", func(t *testing.T) {
		provider := NewMockProvider(0)
		response, err := provider.Generate(context.Background(), "How do I handle JWT auth?")

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if response == "" {
			t.Error("expected non-empty response")
		}
		if !strings.Contains(strings.ToLower(response), "jwt") {
			t.Error("expected response to mention JWT")
		}
	})

	t.Run("respects latency setting", func(t *testing.T) {
		provider := NewMockProvider(50) // 50ms latency

		start := time.Now()
		_, _ = provider.Generate(context.Background(), "test")
		elapsed := time.Since(start)

		if elapsed < 50*time.Millisecond {
			t.Errorf("expected at least 50ms latency, got %v", elapsed)
		}
	})

	t.Run("GenerateWithSystem uses context", func(t *testing.T) {
		provider := NewMockProvider(0)

		// Without context - should give generic error response
		responseWithoutContext, _ := provider.Generate(context.Background(), "How to handle errors?")

		// With context mentioning pkg/errors
		responseWithContext, _ := provider.GenerateWithSystem(
			context.Background(),
			"How to handle errors?",
			"We use pkg/errors for wrapping errors",
		)

		// The response with context should mention wrapping
		if !strings.Contains(responseWithContext, "Wrap") {
			t.Error("expected context-aware response to mention error wrapping")
		}

		// Responses should be different (context matters)
		if responseWithoutContext == responseWithContext {
			t.Error("expected different responses with and without context")
		}
	})
}

func TestMockProviderResponses(t *testing.T) {
	provider := NewMockProvider(0)
	ctx := context.Background()

	tests := []struct {
		name         string
		prompt       string
		context      string
		wantContains string
	}{
		{
			name:         "JWT auth response",
			prompt:       "How do I implement JWT authentication?",
			wantContains: "JWT",
		},
		{
			name:         "database migration response",
			prompt:       "How do I run database migrations?",
			wantContains: "migration",
		},
		{
			name:         "testing response",
			prompt:       "How should I write tests?",
			wantContains: "table-driven",
		},
		{
			name:         "logging with slog context",
			prompt:       "How should I do logging?",
			context:      "We use slog for structured logging",
			wantContains: "slog",
		},
		{
			name:         "default response for unknown topic",
			prompt:       "What color should buttons be?",
			wantContains: "pattern",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var response string
			var err error

			if tt.context != "" {
				response, err = provider.GenerateWithSystem(ctx, tt.prompt, tt.context)
			} else {
				response, err = provider.Generate(ctx, tt.prompt)
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !strings.Contains(strings.ToLower(response), strings.ToLower(tt.wantContains)) {
				t.Errorf("expected response to contain %q, got %q", tt.wantContains, response)
			}
		})
	}
}

func TestAnalysisSystemPrompt(t *testing.T) {
	t.Run("contains key guidance", func(t *testing.T) {
		expectedParts := []string{
			"ACTIONABLE",
			"DURABLE",
			"TEACHABLE",
			"decisions",
			"conventions",
			"constraints",
		}

		for _, part := range expectedParts {
			if !strings.Contains(AnalysisSystemPrompt, part) {
				t.Errorf("AnalysisSystemPrompt missing expected content: %q", part)
			}
		}
	})
}
