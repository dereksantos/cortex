package tools

import (
	"context"
	"testing"
)

// TestMaybeAddAttributionTrailer tests the maybeAddAttributionTrailer function.
func TestMaybeAddAttributionTrailer(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		enabled     bool
		trailer     string
		want        string
		wantApplied bool
	}{
		{
			name:        "git commit without trailer - adds trailer",
			command:     "git commit -m \"fix: login bug\"",
			enabled:     true,
			trailer:     "Co-Authored-By: Cortex (qwen3-coder)",
			want:        "git commit -m \"fix: login bug\" --trailer=\"Co-Authored-By: Cortex (qwen3-coder)\"",
			wantApplied: true,
		},
		{
			name:        "git commit with trailer - no change",
			command:     "git commit -m \"fix: login bug\" --trailer=\"Co-Authored-By: Cortex (qwen3-coder)\"",
			enabled:     true,
			trailer:     "Co-Authored-By: Cortex (qwen3-coder)",
			want:        "git commit -m \"fix: login bug\" --trailer=\"Co-Authored-By: Cortex (qwen3-coder)\"",
			wantApplied: false,
		},
		{
			name:        "git log --grep commit - not modified",
			command:     "git log --grep commit",
			enabled:     true,
			trailer:     "Co-Authored-By: Cortex (qwen3-coder)",
			want:        "git log --grep commit",
			wantApplied: false,
		},
		{
			name:        "chained command - not modified",
			command:     "git commit -m \"fix\" && git push",
			enabled:     true,
			trailer:     "Co-Authored-By: Cortex (qwen3-coder)",
			want:        "git commit -m \"fix\" && git push",
			wantApplied: false,
		},
		{
			name:        "attribution disabled - no change",
			command:     "git commit -m \"fix: login bug\"",
			enabled:     false,
			trailer:     "",
			want:        "git commit -m \"fix: login bug\"",
			wantApplied: false,
		},
		{
			name:        "not a git commit command - no change",
			command:     "go test ./...",
			enabled:     true,
			trailer:     "Co-Authored-By: Cortex (qwen3-coder)",
			want:        "go test ./...",
			wantApplied: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &mockDeps{
				attribution: &mockAttributionProvider{
					enabled: tt.enabled,
					trailer: tt.trailer,
				},
			}

			// Call maybeAddAttributionTrailer and check if it was applied
			result, applied := maybeAddAttributionTrailer(tt.command, deps)
			if result != tt.want {
				t.Errorf("maybeAddAttributionTrailer(%q) = %q, want %q", tt.command, result, tt.want)
			}
			if applied != tt.wantApplied {
				t.Errorf("maybeAddAttributionTrailer(%q) applied=%v, want %v", tt.command, applied, tt.wantApplied)
			}
		})
	}
}

// Mock types for testing

type mockAttributionProvider struct {
	enabled bool
	trailer string
}

func (m *mockAttributionProvider) AttributionEnabled() bool {
	return m.enabled
}

func (m *mockAttributionProvider) AttributionCommit(model string) string {
	return m.trailer
}

type mockDeps struct {
	attribution AttributionProvider
}

func (m *mockDeps) AttributionEnabled() bool {
	if m.attribution != nil {
		return m.attribution.AttributionEnabled()
	}
	return false
}

func (m *mockDeps) AttributionCommit(model string) string {
	if m.attribution != nil {
		return m.attribution.AttributionCommit(model)
	}
	return ""
}

func (m *mockDeps) MemoryWrite(name, content, scope string) (string, error) { return "", nil }
func (m *mockDeps) MemoryRead(name, scope string) (string, error)           { return "", nil }
func (m *mockDeps) MemorySearch(query, scope string) (string, error)        { return "", nil }
func (m *mockDeps) MemoryForget(name, scope string) (string, error)         { return "", nil }
func (m *mockDeps) Recall(citation string) (string, error)                  { return "", nil }
func (m *mockDeps) Outline(path string, budget int) (string, error)         { return "", nil }
func (m *mockDeps) SeedBudget() int                                         { return 0 }
func (m *mockDeps) RunSubagent(ctx context.Context, sa Subagent, seed string) (string, error) {
	return "", nil
}
func (m *mockDeps) Summarize(ctx context.Context, text string, role string, max int) (string, bool, error) {
	return "", false, nil
}
func (m *mockDeps) SummarizeText(ctx context.Context, text string, role string, max int) (string, bool, error) {
	return "", false, nil
}
func (m *mockDeps) GateShell(ctx context.Context, command string) (string, bool) { return "", true }
func (m *mockDeps) AllowDelete() (string, bool)                                  { return "", false }
func (m *mockDeps) Quiet() bool                                                  { return false }
func (m *mockDeps) ValidateToolCall(tc ToolCall) (bool, string)                  { return true, "" }
func (m *mockDeps) RemoveOutlineEntry(citation string) bool                      { return false }
func (m *mockDeps) MergeOutlineEntries(startCitation, endCitation string) (string, error) {
	return "", nil
}
func (m *mockDeps) OutlineLen() int { return 0 }
func (m *mockDeps) AdjustWatermarks(highDelta, lowDelta int) (int, int, int, int, error) {
	return 0, 0, 0, 0, nil
}
func (m *mockDeps) IsToolEnabled(toolName string) bool { return true }

// ConfigProvider stubs
func (m *mockDeps) MemoryIndexCap() int                 { return 0 }
func (m *mockDeps) UserMemoryIndexCap() int             { return 0 }
func (m *mockDeps) CaptureExcerptCap() int              { return 0 }
func (m *mockDeps) MaxTaskContextChars() int            { return 0 }
func (m *mockDeps) MaxToolOutput() int                  { return 0 }
func (m *mockDeps) MaxToolIterations() int              { return 0 }
func (m *mockDeps) InstructionBytesCap() int            { return 0 }
func (m *mockDeps) MaxServedModelsShown() int           { return 0 }
func (m *mockDeps) FleetDiscoveryTimeout() int          { return 0 }
func (m *mockDeps) PreflightTimeout() int               { return 0 }
func (m *mockDeps) SelfHealEnabled() bool               { return false }
func (m *mockDeps) ToolLimits() Limits                  { return Limits{} }
func (m *mockDeps) TailHighWatermark() int              { return 0 }
func (m *mockDeps) TailDrainWatermark() int             { return 0 }
func (m *mockDeps) OutlineBudget() int                  { return 0 }
func (m *mockDeps) SeedBudgetTokens() int               { return 0 }
func (m *mockDeps) MaxToolOutputBytes() int             { return 0 }
func (m *mockDeps) MaxReadBytes() int                   { return 0 }
func (m *mockDeps) DefaultRangeLines() int              { return 0 }
func (m *mockDeps) MaxRangeLines() int                  { return 0 }
func (m *mockDeps) MaxHits() int                        { return 0 }
func (m *mockDeps) LineCap() int                        { return 0 }
func (m *mockDeps) MaxOutputBytes() int                 { return 0 }
func (m *mockDeps) FetchTimeoutSec() int                { return 0 }
func (m *mockDeps) FetchMaxRedirects() int              { return 0 }
func (m *mockDeps) FetchMaxBodyBytes() int              { return 0 }
func (m *mockDeps) WebSearchDefaultMaxResults() int     { return 0 }
func (m *mockDeps) WebSearchMaximumMaxResults() int     { return 0 }
func (m *mockDeps) EnableWeb() bool                     { return false }
func (m *mockDeps) EnableAgent() bool                   { return false }
func (m *mockDeps) EnableScan() bool                    { return false }
func (m *mockDeps) EnableEffortEscalation() bool        { return false }
func (m *mockDeps) EnableContextEvict() bool            { return false }
func (m *mockDeps) EnableContextMerge() bool            { return false }
func (m *mockDeps) EnableContextAdjustWatermarks() bool { return false }
func (m *mockDeps) EnableDelete() bool                  { return false }
func (m *mockDeps) DeleteRoot() string                  { return "" }
func (m *mockDeps) CurationBudgetTokens() int           { return 0 }
func (m *mockDeps) OutlineDefaultBudget() int           { return 0 }
func (m *mockDeps) ReadDefaultRangeLines() int          { return 0 }
func (m *mockDeps) ReadMaxRangeLines() int              { return 0 }
func (m *mockDeps) ReadMaxReadBytes() int               { return 0 }
func (m *mockDeps) GrepMaxHits() int                    { return 0 }
func (m *mockDeps) GrepLineCap() int                    { return 0 }
func (m *mockDeps) GrepMaxOutputBytes() int             { return 0 }
func (m *mockDeps) FetchURLTimeoutSec() int             { return 0 }
func (m *mockDeps) FetchURLMaxRedirects() int           { return 0 }
func (m *mockDeps) FetchURLMaxBodyBytes() int           { return 0 }
