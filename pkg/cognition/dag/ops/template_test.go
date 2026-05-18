package ops

import (
	"strings"
	"testing"
)

func TestLoadTemplate_extractInsight(t *testing.T) {
	resetTemplateCache()
	pt, err := LoadTemplate("maintain_extract_insight")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if pt.Meta.Version != 1 {
		t.Errorf("expected version=1, got %d", pt.Meta.Version)
	}
	if pt.Meta.Op != "maintain.extract_insight" {
		t.Errorf("unexpected op: %q", pt.Meta.Op)
	}
	if pt.Meta.MaxOutputTokens > MaxOutputBudget {
		t.Errorf("max_output_tokens=%d exceeds cap %d", pt.Meta.MaxOutputTokens, MaxOutputBudget)
	}
	if len(pt.Meta.Vars) == 0 {
		t.Error("expected vars to be declared")
	}
}

func TestLoadTemplate_renderWithVars(t *testing.T) {
	resetTemplateCache()
	pt, err := LoadTemplate("maintain_extract_insight")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out, err := pt.Render(map[string]any{
		"content": "decided to use pgx instead of database/sql",
		"source":  "decision-note",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "pgx instead of database/sql") {
		t.Errorf("rendered prompt should contain the content; got:\n%s", out)
	}
	if !strings.Contains(out, "decision-note") {
		t.Errorf("rendered prompt should contain the source; got:\n%s", out)
	}
}

func TestLoadTemplate_missingVar(t *testing.T) {
	resetTemplateCache()
	pt, err := LoadTemplate("maintain_extract_insight")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	_, err = pt.Render(map[string]any{"content": "x"}) // source missing
	if err == nil {
		t.Fatal("expected error for missing var")
	}
	if !strings.Contains(err.Error(), "source") {
		t.Errorf("error should name the missing var; got: %v", err)
	}
}

func TestLoadTemplate_cachesParse(t *testing.T) {
	resetTemplateCache()
	a, err := LoadTemplate("maintain_extract_insight")
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadTemplate("maintain_extract_insight")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("expected LoadTemplate to return cached pointer")
	}
}

func TestLoadTemplate_missingFile(t *testing.T) {
	resetTemplateCache()
	_, err := LoadTemplate("does_not_exist")
	if err == nil {
		t.Fatal("expected error for missing template")
	}
}

func TestSplitFrontmatter_malformed(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"no opening delim", "version: 1\nop: x\n---\nbody"},
		{"no closing delim", "---\nversion: 1\nop: x\nbody"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := splitFrontmatter([]byte(tc.raw))
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
