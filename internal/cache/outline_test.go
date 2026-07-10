package cache

import (
	"testing"
)

func TestOutlineEntryRender(t *testing.T) {
	tests := []struct {
		name  string
		entry OutlineEntry
		want  string
	}{
		{
			name: "full entry with all fields",
			entry: OutlineEntry{
				Turn:      5,
				User:      "What does this code do?",
				Actions:   []string{"edit cmd/cortex/study_eval.go [ok]", "read internal/cache/outline.go [ok]"},
				ReplyHead: "This function processes the input and returns a formatted string.",
				Citation:  "@session/20260701-143210:L120-134",
			},
			want: `t5 · user: What does this code do?
      edit cmd/cortex/study_eval.go [ok] · read internal/cache/outline.go [ok]
      ⤷ This function processes the input and returns a formatted string.
      [@session/20260701-143210:L120-134]`,
		},
		{
			name: "entry with no Actions",
			entry: OutlineEntry{
				Turn:      1,
				User:      "Hello",
				Actions:   []string{},
				ReplyHead: "Hi there!",
				Citation:  "@session/20260701-143210:L10-20",
			},
			want: `t1 · user: Hello
      ⤷ Hi there!
      [@session/20260701-143210:L10-20]`,
		},
		{
			name: "entry with empty ReplyHead and Citation",
			entry: OutlineEntry{
				Turn:      2,
				User:      "Test message",
				Actions:   []string{"edit file.go [ok]"},
				ReplyHead: "",
				Citation:  "",
			},
			want: `t2 · user: Test message
      edit file.go [ok]`,
		},
		{
			name: "multi-line User text",
			entry: OutlineEntry{
				Turn:      3,
				User:      "First line\nSecond line\nThird line",
				Actions:   []string{},
				ReplyHead: "",
				Citation:  "",
			},
			want: `t3 · user: First line
      Second line
      Third line`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.entry.Render()
			if got != tt.want {
				t.Errorf("Render() mismatch:\nGot:\n%s\n\nWant:\n%s", got, tt.want)
			}
		})
	}
}

func TestRenderOutline(t *testing.T) {
	tests := []struct {
		name    string
		entries []OutlineEntry
		want    string
	}{
		{
			name: "two entries joined by blank line",
			entries: []OutlineEntry{
				{
					Turn:      1,
					User:      "First message",
					Actions:   []string{"action1 [ok]"},
					ReplyHead: "First reply",
					Citation:  "@session/20260701-143210:L10-20",
				},
				{
					Turn:      2,
					User:      "Second message",
					Actions:   []string{"action2 [ok]"},
					ReplyHead: "Second reply",
					Citation:  "@session/20260701-143210:L30-40",
				},
			},
			want: `t1 · user: First message
      action1 [ok]
      ⤷ First reply
      [@session/20260701-143210:L10-20]

t2 · user: Second message
      action2 [ok]
      ⤷ Second reply
      [@session/20260701-143210:L30-40]`,
		},
		{
			name:    "empty slice returns empty string",
			entries: []OutlineEntry{},
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderOutline(tt.entries)
			if got != tt.want {
				t.Errorf("RenderOutline() mismatch:\nGot:\n%s\n\nWant:\n%s", got, tt.want)
			}
		})
	}
}
