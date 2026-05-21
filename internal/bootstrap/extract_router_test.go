package bootstrap

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

func TestChooseExtractOp_Auto(t *testing.T) {
	cases := []struct {
		lang, want string
	}{
		// Source-like → overview
		{"go", "maintain.extract_overview"},
		{"py", "maintain.extract_overview"},
		{"js", "maintain.extract_overview"},
		{"ts", "maintain.extract_overview"},
		{"rs", "maintain.extract_overview"},
		{"java", "maintain.extract_overview"},
		{"c", "maintain.extract_overview"},
		{"sql", "maintain.extract_overview"},

		// Config-like → overview
		{"toml", "maintain.extract_overview"},
		{"yaml", "maintain.extract_overview"},
		{"ini", "maintain.extract_overview"},

		// Prose / unknown → insight
		{"md", "maintain.extract_insight"},
		{"txt", "maintain.extract_insight"},
		{"rst", "maintain.extract_insight"},
		{"unknown", "maintain.extract_insight"},
		{"", "maintain.extract_insight"},
	}
	for _, tc := range cases {
		t.Run("auto/"+tc.lang, func(t *testing.T) {
			if got := ChooseExtractOp(ExtractOpAuto, tc.lang); got != tc.want {
				t.Errorf("ChooseExtractOp(auto,%q) = %q, want %q", tc.lang, got, tc.want)
			}
		})
	}
}

func TestChooseExtractOp_EmptyCfgFallsBackToAuto(t *testing.T) {
	// cfg="" should behave the same as cfg="auto"
	cases := []struct {
		lang, want string
	}{
		{"go", "maintain.extract_overview"},
		{"md", "maintain.extract_insight"},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			if got := ChooseExtractOp("", tc.lang); got != tc.want {
				t.Errorf("empty cfg, lang=%q: got %q, want %q", tc.lang, got, tc.want)
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
