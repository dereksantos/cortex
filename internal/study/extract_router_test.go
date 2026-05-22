package study

import "testing"

func TestChooseExtractOp_Explicit(t *testing.T) {
	cases := []struct {
		cfg, lang, want string
	}{
		{ExtractOpInsight, "go", "maintain.extract_insight"},
		{ExtractOpInsight, "md", "maintain.extract_insight"},
		{ExtractOpOverview, "md", "maintain.extract_overview"},
		{ExtractOpOverview, "py", "maintain.extract_overview"},
	}
	for _, tc := range cases {
		t.Run(tc.cfg+"/"+tc.lang, func(t *testing.T) {
			if got := ChooseExtractOp(tc.cfg, tc.lang); got != tc.want {
				t.Errorf("ChooseExtractOp(%q,%q) = %q, want %q", tc.cfg, tc.lang, got, tc.want)
			}
		})
	}
}

// TestChooseExtractOp_Auto pins the post-A/B routing decision: every
// language family routes to extract_overview. See the 2026-05-21 entry
// in docs/eval-journal.md and ChooseExtractOp's docstring for the
// scoring rationale.
func TestChooseExtractOp_Auto(t *testing.T) {
	cases := []string{
		// Source-like
		"go", "py", "js", "ts", "rs", "java", "c", "cs", "swift", "kt", "scala",
		"rb", "sh", "lua", "sql", "hs",
		// Config-like
		"toml", "yaml", "ini", "tf",
		// Prose / unknown
		"md", "txt", "rst", "unknown", "",
	}
	for _, lang := range cases {
		t.Run("auto/"+lang, func(t *testing.T) {
			got := ChooseExtractOp(ExtractOpAuto, lang)
			if got != "maintain.extract_overview" {
				t.Errorf("ChooseExtractOp(auto,%q) = %q, want maintain.extract_overview", lang, got)
			}
		})
	}
}

func TestChooseExtractOp_EmptyCfgFallsBackToAuto(t *testing.T) {
	// cfg="" should behave the same as cfg="auto" — overview for all.
	for _, lang := range []string{"go", "md", "py", "ts"} {
		t.Run(lang, func(t *testing.T) {
			got := ChooseExtractOp("", lang)
			if got != "maintain.extract_overview" {
				t.Errorf("empty cfg, lang=%q: got %q, want maintain.extract_overview", lang, got)
			}
		})
	}
}

func TestIsValidExtractOp(t *testing.T) {
	for _, ok := range []string{"", "auto", "extract_insight", "extract_overview"} {
		if !IsValidExtractOp(ok) {
			t.Errorf("IsValidExtractOp(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"insight", "overview", "extract", "INSIGHT", "garbage"} {
		if IsValidExtractOp(bad) {
			t.Errorf("IsValidExtractOp(%q) = true, want false", bad)
		}
	}
}
