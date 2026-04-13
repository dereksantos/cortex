package cognition

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
)

func TestParseNuanceResponse(t *testing.T) {
	tests := []struct {
		name          string
		response      string
		expectNuances int
		expectError   bool
	}{
		{
			name: "valid JSON response",
			response: `{
				"nuances": [
					{"detail": "log before return", "why": "ensures error is captured"},
					{"detail": "include error attribute", "why": "helps debugging"}
				]
			}`,
			expectNuances: 2,
			expectError:   false,
		},
		{
			name: "JSON with surrounding text",
			response: `Here are the nuances:
			{
				"nuances": [
					{"detail": "always check nil", "why": "prevents panic"}
				]
			}
			Hope this helps!`,
			expectNuances: 1,
			expectError:   false,
		},
		{
			name:          "no JSON in response",
			response:      "This pattern has no notable gotchas.",
			expectNuances: 0,
			expectError:   true,
		},
		{
			name:          "empty nuances array",
			response:      `{"nuances": []}`,
			expectNuances: 0,
			expectError:   false,
		},
		{
			name:          "malformed JSON",
			response:      `{"nuances": [{"detail": "incomplete`,
			expectNuances: 0,
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nuances, err := parseNuanceResponse(tt.response)

			if tt.expectError && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if len(nuances) != tt.expectNuances {
				t.Errorf("got %d nuances, want %d", len(nuances), tt.expectNuances)
			}
		})
	}
}

func TestExtractNuances_NilProvider(t *testing.T) {
	nuances, err := ExtractNuances(context.Background(), nil, "some pattern")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if nuances != nil {
		t.Error("expected nil nuances for nil provider")
	}
}

func TestExtractNuances_EmptyContent(t *testing.T) {
	provider := llm.NewMockProvider(0)
	nuances, err := ExtractNuances(context.Background(), provider, "")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if nuances != nil {
		t.Error("expected nil nuances for empty content")
	}
}

func TestExtractNuances_ContentTruncation(t *testing.T) {
	// Verify that very long content gets truncated (doesn't cause errors)
	provider := &nuanceMockProvider{
		response: `{"nuances": [{"detail": "test", "why": "reason"}]}`,
	}

	longContent := strings.Repeat("x", 2000)
	nuances, err := ExtractNuances(context.Background(), provider, longContent)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(nuances) != 1 {
		t.Errorf("got %d nuances, want 1", len(nuances))
	}

	// Verify the prompt was truncated
	if !strings.Contains(provider.receivedPrompt, "...") {
		t.Error("expected truncated content to end with ...")
	}
}

// nuanceMockProvider records the prompt for inspection
type nuanceMockProvider struct {
	response       string
	receivedPrompt string
}

func (m *nuanceMockProvider) Name() string      { return "nuance-mock" }
func (m *nuanceMockProvider) IsAvailable() bool { return true }
func (m *nuanceMockProvider) Generate(ctx context.Context, prompt string) (string, error) {
	m.receivedPrompt = prompt
	return m.response, nil
}
func (m *nuanceMockProvider) GenerateWithSystem(ctx context.Context, prompt, system string) (string, error) {
	m.receivedPrompt = prompt
	return m.response, nil
}
func (m *nuanceMockProvider) GenerateWithStats(ctx context.Context, prompt string) (string, llm.GenerationStats, error) {
	m.receivedPrompt = prompt
	return m.response, llm.GenerationStats{InputTokens: len(prompt) / 4, OutputTokens: len(m.response) / 4}, nil
}

// TestExtractNuances_RealLLM tests with a real LLM to verify the prompt works.
// Skip if no LLM is available.
func TestExtractNuances_RealLLM(t *testing.T) {
	// Check for Anthropic API key
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping real LLM test")
	}

	cfg := config.Default()
	cfg.AnthropicModel = "claude-3-haiku-20240307"
	provider := llm.NewAnthropicClient(cfg)
	if !provider.IsAvailable() {
		t.Skip("Anthropic provider not available")
	}

	// Test with the actual logging pattern from our journey
	loggingPattern := "Use slog.Info() for successful operations, slog.Error() for failures with err attribute, slog.Debug() for verbose/troubleshooting output"

	nuances, err := ExtractNuances(context.Background(), provider, loggingPattern)
	if err != nil {
		t.Fatalf("ExtractNuances failed: %v", err)
	}

	t.Logf("Extracted %d nuances from logging pattern:", len(nuances))
	for i, n := range nuances {
		t.Logf("  %d. %s (why: %s)", i+1, n.Detail, n.Why)
	}

	// We expect at least one nuance extracted
	if len(nuances) == 0 {
		t.Error("expected at least one nuance from logging pattern")
	}

	// Check if any nuance mentions something about logging placement/timing
	foundRelevant := false
	for _, n := range nuances {
		lower := strings.ToLower(n.Detail + " " + n.Why)
		// Look for keywords suggesting logging placement, order, or timing
		if strings.Contains(lower, "before") ||
			strings.Contains(lower, "return") ||
			strings.Contains(lower, "order") ||
			strings.Contains(lower, "first") ||
			strings.Contains(lower, "capture") ||
			strings.Contains(lower, "miss") {
			foundRelevant = true
			t.Logf("Found relevant nuance about ordering/placement: %s", n.Detail)
		}
	}

	if !foundRelevant {
		t.Log("WARNING: No nuance about log placement/order found. May need prompt iteration.")
	}
}

// TestExtractNuances_ErrorHandlingPattern tests nuance extraction on error handling patterns
func TestExtractNuances_ErrorHandlingPattern(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping real LLM test")
	}

	cfg := config.Default()
	cfg.AnthropicModel = "claude-3-haiku-20240307"
	provider := llm.NewAnthropicClient(cfg)
	if !provider.IsAvailable() {
		t.Skip("Anthropic provider not available")
	}

	errorPattern := "Always wrap errors with context using fmt.Errorf(\"failed to X: %w\", err). Return errors up the call stack, don't log and return."

	nuances, err := ExtractNuances(context.Background(), provider, errorPattern)
	if err != nil {
		t.Fatalf("ExtractNuances failed: %v", err)
	}

	t.Logf("Extracted %d nuances from error handling pattern:", len(nuances))
	for i, n := range nuances {
		t.Logf("  %d. %s (why: %s)", i+1, n.Detail, n.Why)
	}

	if len(nuances) == 0 {
		t.Error("expected at least one nuance from error handling pattern")
	}
}
