package commands

import (
	"strings"
	"testing"
	"unicode/utf8"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/tui"
)

const (
	dashboardWidth = 61
	col1Width      = 28
	col2Width      = 28
)

// TestWatchOutputFormat verifies the watch dashboard output has correct formatting.
func TestWatchOutputFormat(t *testing.T) {
	// Create test data
	data := &WatchData{
		TotalEvents:   100,
		TotalInsights: 50,
		Retrieval: &intcognition.RetrievalStats{
			TotalRetrievals: 10,
			LastMode:        "fast",
			LastReflexMs:    15,
			LastResults:     5,
			LastDecision:    "inject",
		},
		TopicWeights: map[string]float64{
			"auth": 0.8,
			"api":  0.5,
		},
	}

	t.Run("header panel has consistent width", func(t *testing.T) {
		icon, name, desc := data.ModeStatus(0, false)
		lines := tui.HeaderPanel(icon, name, desc, dashboardWidth)

		for i, line := range lines {
			width := utf8.RuneCountInString(line)
			if width != dashboardWidth {
				t.Errorf("header line %d: width=%d, want %d\nline: %q", i, width, dashboardWidth, line)
			}
		}
	})

	t.Run("split panel has consistent width", func(t *testing.T) {
		leftLines := []string{"Queries: 10", "Last: 15ms"}
		rightLines := []string{"Events: 100", "Insights: 50"}

		lines := tui.SplitPanel("Retrieval", "Background", leftLines, rightLines, col1Width, col2Width)
		// Width = leftWidth + rightWidth + 5 (outer borders + middle divider + padding)
		expectedWidth := col1Width + col2Width + 5

		for i, line := range lines {
			width := utf8.RuneCountInString(line)
			if width != expectedWidth {
				t.Errorf("split panel line %d: width=%d, want %d\nline: %q", i, width, expectedWidth, line)
			}
		}
	})

	t.Run("box characters are properly paired", func(t *testing.T) {
		leftLines := []string{"Test content"}
		rightLines := []string{"More content"}
		lines := tui.SplitPanel("Left", "Right", leftLines, rightLines, col1Width, col2Width)

		// Verify structure of each line type
		for i, line := range lines {
			switch {
			case i == 0: // Top border: ┌───┬───┐
				if !strings.HasPrefix(line, "┌") || !strings.HasSuffix(line, "┐") {
					t.Errorf("line %d: invalid top border: %q", i, line)
				}
				if strings.Count(line, "┬") != 1 {
					t.Errorf("line %d: expected 1 top-T junction: %q", i, line)
				}
			case i == len(lines)-1: // Bottom border: └───┴───┘
				if !strings.HasPrefix(line, "└") || !strings.HasSuffix(line, "┘") {
					t.Errorf("line %d: invalid bottom border: %q", i, line)
				}
				if strings.Count(line, "┴") != 1 {
					t.Errorf("line %d: expected 1 bottom-T junction: %q", i, line)
				}
			case strings.Contains(line, "─"): // Divider row: ├───┼───┤
				if !strings.HasPrefix(line, "├") || !strings.HasSuffix(line, "┤") {
					t.Errorf("line %d: invalid divider: %q", i, line)
				}
				if strings.Count(line, "┼") != 1 {
					t.Errorf("line %d: expected 1 cross junction: %q", i, line)
				}
			default: // Content row: │ content │ content │
				verticalBars := strings.Count(line, "│")
				if verticalBars != 3 {
					t.Errorf("line %d: expected 3 vertical bars, got %d\nline: %q", i, verticalBars, line)
				}
				if !strings.HasPrefix(line, "│") || !strings.HasSuffix(line, "│") {
					t.Errorf("line %d: should start and end with │: %q", i, line)
				}
			}
		}
	})

	t.Run("no embedded newlines in output lines", func(t *testing.T) {
		lines := tui.Panel("Test", []string{"Line 1", "Line 2"}, 40)

		for i, line := range lines {
			if strings.Contains(line, "\n") {
				t.Errorf("line %d contains embedded newline: %q", i, line)
			}
			if strings.Contains(line, "\r") {
				t.Errorf("line %d contains embedded carriage return: %q", i, line)
			}
		}
	})

	t.Run("activity row formatting", func(t *testing.T) {
		row := tui.ActivityRow("15:04:05", "dream", "Processing insights", 50)

		width := utf8.RuneCountInString(row)
		if width != 50 {
			t.Errorf("activity row width=%d, want 50\nrow: %q", width, row)
		}

		if strings.Contains(row, "\n") {
			t.Errorf("activity row contains newline: %q", row)
		}
	})

	t.Run("session row formatting", func(t *testing.T) {
		row := tui.SessionRow("> ", "15:04", "implement auth flow", 25, 50)

		width := utf8.RuneCountInString(row)
		if width != 50 {
			t.Errorf("session row width=%d, want 50\nrow: %q", width, row)
		}
	})
}

// TestWatchDataMethods tests the WatchData helper methods.
func TestWatchDataMethods(t *testing.T) {
	t.Run("ModeStatus returns correct values", func(t *testing.T) {
		data := &WatchData{}

		icon, name, desc := data.ModeStatus(0, false)
		if icon != "○" {
			t.Errorf("idle icon=%q, want ○", icon)
		}
		if name != "IDLE" {
			t.Errorf("idle name=%q, want IDLE", name)
		}
		if desc != "" {
			t.Errorf("idle desc=%q, want empty", desc)
		}
	})

	t.Run("ModeStatus with daemon state", func(t *testing.T) {
		data := &WatchData{
			Daemon: &intcognition.DaemonState{
				Mode:        "dream",
				Description: "exploring code",
			},
		}

		icon, name, desc := data.ModeStatus(0, true)
		if name != "DREAMING" {
			t.Errorf("dream name=%q, want DREAMING", name)
		}
		if desc != "exploring code" {
			t.Errorf("dream desc=%q, want 'exploring code'", desc)
		}
		_ = icon
	})

	t.Run("TopTopics returns sorted by weight", func(t *testing.T) {
		data := &WatchData{
			TopicWeights: map[string]float64{
				"low":    0.2,
				"medium": 0.5,
				"high":   0.9,
			},
		}

		topics := data.TopTopics(3, 0.3)
		if len(topics) != 2 { // low is below threshold
			t.Errorf("got %d topics, want 2", len(topics))
		}
		if len(topics) >= 1 && topics[0].Topic != "high" {
			t.Errorf("first topic=%q, want 'high'", topics[0].Topic)
		}
	})

	t.Run("ABR returns cache hit rate", func(t *testing.T) {
		data := &WatchData{
			Background: &intcognition.BackgroundMetrics{
				CacheHitRate: 0.75,
			},
		}

		abr := data.ABR()
		if abr != 0.75 {
			t.Errorf("ABR=%f, want 0.75", abr)
		}
	})

	t.Run("ThinkStatus based on activity level", func(t *testing.T) {
		tests := []struct {
			level float64
			want  string
		}{
			{0.0, "idle"},
			{0.5, "active"},
			{0.9, "active (busy)"},
		}

		for _, tt := range tests {
			data := &WatchData{
				Background: &intcognition.BackgroundMetrics{
					ActivityLevel: tt.level,
				},
			}
			got := data.ThinkStatus()
			if got != tt.want {
				t.Errorf("ThinkStatus(level=%f)=%q, want %q", tt.level, got, tt.want)
			}
		}
	})
}

// TestPanelWidthConsistency ensures all panel functions produce consistent widths.
func TestPanelWidthConsistency(t *testing.T) {
	testWidth := 60

	t.Run("Panel with various content", func(t *testing.T) {
		lines := tui.Panel("Title", []string{
			"Short",
			"A much longer line that might need truncation if it exceeds the width",
			"",
			"Unicode: 日本語テスト",
		}, testWidth)

		for i, line := range lines {
			width := utf8.RuneCountInString(line)
			if width != testWidth {
				t.Errorf("Panel line %d: width=%d, want %d\nline: %q", i, width, testWidth, line)
			}
		}
	})

	t.Run("HeaderPanel variants", func(t *testing.T) {
		// Short description
		lines1 := tui.HeaderPanel("●", "THINKING", "short", testWidth)
		for i, line := range lines1 {
			width := utf8.RuneCountInString(line)
			if width != testWidth {
				t.Errorf("HeaderPanel(short) line %d: width=%d, want %d", i, width, testWidth)
			}
		}

		// Long description that wraps
		lines2 := tui.HeaderPanel("●", "THINKING", "a very long description that should be truncated properly", testWidth)
		for i, line := range lines2 {
			width := utf8.RuneCountInString(line)
			if width != testWidth {
				t.Errorf("HeaderPanel(long) line %d: width=%d, want %d", i, width, testWidth)
			}
		}
	})
}
