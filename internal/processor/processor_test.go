package processor

import (
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/events"
)

func TestProcessor_shouldAnalyze(t *testing.T) {
	p := &Processor{
		lastProcessed: make(map[string]time.Time),
	}

	tests := []struct {
		name  string
		event *events.Event
		want  bool
	}{
		{
			name: "should skip Read operations",
			event: &events.Event{
				ToolName: "Read",
			},
			want: false,
		},
		{
			name: "should skip Grep operations",
			event: &events.Event{
				ToolName: "Grep",
			},
			want: false,
		},
		{
			name: "should analyze Edit",
			event: &events.Event{
				ToolName:  "Edit",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "main.go",
				},
			},
			want: true,
		},
		{
			name: "should analyze Write",
			event: &events.Event{
				ToolName:  "Write",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "test.go",
				},
			},
			want: true,
		},
		{
			name: "should skip binary files (.pyc)",
			event: &events.Event{
				ToolName:  "Edit",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "script.pyc",
				},
			},
			want: false,
		},
		{
			name: "should skip image files (.png)",
			event: &events.Event{
				ToolName:  "Write",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "logo.png",
				},
			},
			want: false,
		},
		{
			name: "should skip lock files",
			event: &events.Event{
				ToolName:  "Edit",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "package-lock.json",
				},
			},
			want: false,
		},
		{
			name: "should analyze markdown files",
			event: &events.Event{
				ToolName:  "Edit",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "README.md",
				},
			},
			want: true,
		},
		{
			name: "should analyze Task events",
			event: &events.Event{
				ToolName:  "Task",
				Timestamp: time.Now(),
			},
			want: true,
		},
		{
			name: "should analyze Bash events",
			event: &events.Event{
				ToolName:  "Bash",
				Timestamp: time.Now(),
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.shouldAnalyze(tt.event)
			if got != tt.want {
				t.Errorf("shouldAnalyze() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProcessor_Deduplication(t *testing.T) {
	p := &Processor{
		lastProcessed: make(map[string]time.Time),
	}

	now := time.Now()

	// First edit to a file
	event1 := &events.Event{
		ToolName:  "Edit",
		Timestamp: now,
		ToolInput: map[string]interface{}{
			"file_path": "main.go",
		},
	}

	// Should analyze first time
	if !p.shouldAnalyze(event1) {
		t.Error("First edit should be analyzed")
	}

	// Second edit to same file within 30 seconds
	event2 := &events.Event{
		ToolName:  "Edit",
		Timestamp: now.Add(15 * time.Second),
		ToolInput: map[string]interface{}{
			"file_path": "main.go",
		},
	}

	// Should be skipped (deduplicated)
	if p.shouldAnalyze(event2) {
		t.Error("Second edit within 30s should be skipped (deduplicated)")
	}

	// Third edit to same file after 30 seconds
	event3 := &events.Event{
		ToolName:  "Edit",
		Timestamp: now.Add(35 * time.Second),
		ToolInput: map[string]interface{}{
			"file_path": "main.go",
		},
	}

	// Should analyze again
	if !p.shouldAnalyze(event3) {
		t.Error("Edit after 30s should be analyzed")
	}

	// Edit to different file
	event4 := &events.Event{
		ToolName:  "Edit",
		Timestamp: now,
		ToolInput: map[string]interface{}{
			"file_path": "other.go",
		},
	}

	// Should analyze (different file)
	if !p.shouldAnalyze(event4) {
		t.Error("Edit to different file should be analyzed")
	}
}

func TestProcessor_FileExtensionFiltering(t *testing.T) {
	p := &Processor{
		lastProcessed: make(map[string]time.Time),
	}

	binaryExtensions := []string{
		".pyc", ".o", ".class", ".lock",
		".png", ".jpg", ".jpeg", ".gif", ".svg",
		".zip", ".tar", ".gz", ".pdf",
	}

	for _, ext := range binaryExtensions {
		t.Run("should skip "+ext, func(t *testing.T) {
			event := &events.Event{
				ToolName:  "Edit",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "file" + ext,
				},
			}

			if p.shouldAnalyze(event) {
				t.Errorf("Files with extension %s should be skipped", ext)
			}
		})
	}

	// Test files that SHOULD be analyzed
	allowedExtensions := []string{
		".go", ".py", ".js", ".ts", ".md", ".txt",
		".json", ".yaml", ".yml", ".toml",
	}

	for _, ext := range allowedExtensions {
		t.Run("should analyze "+ext, func(t *testing.T) {
			event := &events.Event{
				ToolName:  "Edit",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "file" + ext,
				},
			}

			if !p.shouldAnalyze(event) {
				t.Errorf("Files with extension %s should be analyzed", ext)
			}
		})
	}
}
