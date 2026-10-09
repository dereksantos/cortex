package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/style"
)

func TestRenderHeaderGolden(t *testing.T) {
	base := headerFacts{Version: "0.3.0", Project: "~/eng/projects/cortex", Model: "qwen3-coder-q3"}
	resumed := base
	resumed.Resumed, resumed.SessionID = true, "s-0c41"
	resumed.Turns, resumed.Outlined, resumed.Notes = 14, 9, 3
	emptyResume := base
	emptyResume.Resumed, emptyResume.SessionID = true, "s-77aa"
	noModel := base
	noModel.Model = ""

	tests := []struct {
		name  string
		facts headerFacts
		color bool
		want  string
	}{
		{"fresh", base, false, "cortex 0.3.0 · ~/eng/projects/cortex · qwen3-coder-q3"},
		{"resumed", resumed, false, "cortex 0.3.0 · ~/eng/projects/cortex · qwen3-coder-q3\n" +
			"resumed s-0c41 · 14 turns · 9 outlined, 5 live · 3 notes"},
		{"resumed, nothing carried", emptyResume, false, "cortex 0.3.0 · ~/eng/projects/cortex · qwen3-coder-q3\n" +
			"resumed s-77aa · 0 turns"},
		{"missing part dropped", noModel, false, "cortex 0.3.0 · ~/eng/projects/cortex"},
		{"resumed colored", resumed, true, "cortex <90>0.3.0 · ~/eng/projects/cortex · qwen3-coder-q3</>\n" +
			"<90>resumed s-0c41 · 14 turns · 9 outlined, 5 live · 3 notes</>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer style.ForceColor(tt.color)()
			got := tagSGR(strings.Join(renderHeader(tt.facts), "\n"))
			if got != tt.want {
				t.Errorf("header changed.\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestHomeRel(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	tests := []struct {
		name, in, want string
	}{
		{"home itself", home, "~"},
		{"under home", filepath.Join(home, "eng", "cortex"), "~" + string(filepath.Separator) + filepath.Join("eng", "cortex")},
		{"outside home", "/opt/cortex", "/opt/cortex"},
		{"sibling with home as prefix", home + "x", home + "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := homeRel(tt.in); got != tt.want {
				t.Errorf("homeRel(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
