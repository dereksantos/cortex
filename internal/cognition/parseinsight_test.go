package cognition

import "testing"

func TestParseInsight(t *testing.T) {
	t.Run("parses a clean JSON object", func(t *testing.T) {
		f, err := ParseInsight(`{"content":"use pgx","category":"decision","importance":0.8,"tags":["db"]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.Content != "use pgx" || f.Category != "decision" || f.Importance != 0.8 || len(f.Tags) != 1 {
			t.Errorf("parsed = %+v", f)
		}
	})

	t.Run("tolerates prose around the object", func(t *testing.T) {
		f, err := ParseInsight("Here is the insight:\n{\"content\":\"x\",\"category\":\"pattern\",\"importance\":0.5}\nDone.")
		if err != nil || f.Content != "x" {
			t.Fatalf("got %+v, err %v", f, err)
		}
	})

	t.Run("normalizes out-of-range importance to 0.5", func(t *testing.T) {
		for _, raw := range []string{
			`{"content":"x","importance":0}`,
			`{"content":"x","importance":9}`,
			`{"content":"x"}`,
		} {
			f, err := ParseInsight(raw)
			if err != nil {
				t.Fatalf("%s: %v", raw, err)
			}
			if f.Importance != 0.5 {
				t.Errorf("%s: importance = %v, want 0.5", raw, f.Importance)
			}
		}
	})

	t.Run("NO_INSIGHT / no JSON is an error", func(t *testing.T) {
		if _, err := ParseInsight("NO_INSIGHT"); err == nil {
			t.Error("expected error when no JSON object is present")
		}
	})
}
