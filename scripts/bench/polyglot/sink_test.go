package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSinkAppendsDuringRun pins the hard requirement that rows land on disk as
// each exercise finishes: a run killed halfway through must still leave a
// complete, parseable record of everything that did finish.
func TestSinkAppendsDuringRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "results.jsonl")
	sink, err := NewSink(path)
	if err != nil {
		t.Fatalf("NewSink: %v", err)
	}
	defer sink.Close()

	rows := []Row{
		{Exercise: "alphametics", Pass: true, ToolCalls: 9, TokensIn: 40000, TokensOut: 1800, WallMs: 62000},
		{Exercise: "beer-song", ToolCalls: 0, FailureClass: ClassChatMode, WallMs: 12000},
	}
	for i, r := range rows {
		if err := sink.Append(r); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		// Read the file back BEFORE the sink is closed — that is the property
		// under test.
		got := readRows(t, path)
		if len(got) != i+1 {
			t.Fatalf("after appending %d row(s), the file holds %d", i+1, len(got))
		}
		if got[i].Exercise != r.Exercise {
			t.Errorf("row %d exercise = %q, want %q", i, got[i].Exercise, r.Exercise)
		}
	}
}

// TestRowAlwaysCarriesFailureClass guards the schema: consumers group by
// failure_class, so the key must be present on passing rows too (as "").
func TestRowAlwaysCarriesFailureClass(t *testing.T) {
	b, err := json.Marshal(Row{Exercise: "wordy", Pass: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{
		"exercise", "pass", "tool_calls", "tokens_in", "tokens_out",
		"wall_ms", "failure_class", "transcript_path",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("row is missing the required key %q", key)
		}
	}
	if string(raw["failure_class"]) != `""` {
		t.Errorf("failure_class on a passing row = %s, want an empty string", raw["failure_class"])
	}
}

func TestSummarize(t *testing.T) {
	tests := []struct {
		name        string
		rows        []Row
		wantPassed  int
		wantRate    float64
		wantClasses map[string]int
		wantMedIn   int
		wantTotIn   int
		wantMedWall int64
	}{
		{
			name:        "no rows summarizes to zero without dividing by zero",
			wantClasses: map[string]int{},
		},
		{
			name: "counts pass@1 and each failure class",
			rows: []Row{
				{Pass: true, TokensIn: 10, WallMs: 1000},
				{FailureClass: ClassWrongCode, TokensIn: 30, WallMs: 3000},
				{FailureClass: ClassChatMode, TokensIn: 20, WallMs: 2000},
				{FailureClass: ClassWrongCode, TokensIn: 40, WallMs: 9000},
			},
			wantPassed:  1,
			wantRate:    0.25,
			wantClasses: map[string]int{ClassWrongCode: 2, ClassChatMode: 1},
			wantMedIn:   20, // lower median of 10,20,30,40
			wantTotIn:   100,
			wantMedWall: 2000,
		},
		{
			name:        "an all-pass run reports no classes",
			rows:        []Row{{Pass: true, TokensIn: 5, WallMs: 500}, {Pass: true, TokensIn: 7, WallMs: 700}},
			wantPassed:  2,
			wantRate:    1,
			wantClasses: map[string]int{},
			wantMedIn:   5,
			wantTotIn:   12,
			wantMedWall: 500,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Summarize(tt.rows)
			if s.Passed != tt.wantPassed {
				t.Errorf("Passed = %d, want %d", s.Passed, tt.wantPassed)
			}
			if s.PassRate() != tt.wantRate {
				t.Errorf("PassRate = %v, want %v", s.PassRate(), tt.wantRate)
			}
			if len(s.ClassCounts) != len(tt.wantClasses) {
				t.Errorf("ClassCounts = %v, want %v", s.ClassCounts, tt.wantClasses)
			}
			for c, n := range tt.wantClasses {
				if s.ClassCounts[c] != n {
					t.Errorf("ClassCounts[%s] = %d, want %d", c, s.ClassCounts[c], n)
				}
			}
			if s.MedTokensIn != tt.wantMedIn {
				t.Errorf("MedTokensIn = %d, want %d", s.MedTokensIn, tt.wantMedIn)
			}
			if s.TokensIn != tt.wantTotIn {
				t.Errorf("TokensIn = %d, want %d", s.TokensIn, tt.wantTotIn)
			}
			if s.WallMsMedian != tt.wantMedWall {
				t.Errorf("WallMsMedian = %d, want %d", s.WallMsMedian, tt.wantMedWall)
			}
		})
	}
}

func TestPrintSummary(t *testing.T) {
	meta := RunMeta{
		RunID:           "20260806-120000",
		Model:           "qwen3-coder-q3",
		Endpoint:        "http://chatterbox:4000",
		PolyglotCommit:  "7e0611e77b54e2dea774cdc0aa00cf9f7ed6144f",
		ResultsPath:     "/tmp/results.jsonl",
		TranscriptsPath: "/tmp/transcripts",
	}
	rows := []Row{
		{Exercise: "alphametics", Pass: true, ToolCalls: 9, TokensIn: 40000, TokensOut: 1800, WallMs: 62000},
		{Exercise: "beer-song", FailureClass: ClassChatMode, WallMs: 12000},
	}
	var buf bytes.Buffer
	PrintSummary(&buf, meta, rows)
	out := buf.String()
	for _, want := range []string{
		"pass@1        1/2 (50.0%)",
		"chat_mode      1",
		"PASS alphametics",
		"FAIL beer-song",
		"qwen3-coder-q3",
		"7e0611e77b54",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing %q\n---\n%s", want, out)
		}
	}
}

func TestWriteRunMeta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "run.json")
	in := RunMeta{
		RunID:          "20260806-120000",
		Model:          "qwen3-coder-q3",
		Temperature:    0,
		PolyglotCommit: "7e0611e77b54e2dea774cdc0aa00cf9f7ed6144f",
		CortexCommit:   "deadbeef",
		Exercises:      []string{"alphametics"},
	}
	if err := WriteRunMeta(path, in); err != nil {
		t.Fatalf("WriteRunMeta: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var out RunMeta
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.PolyglotCommit != in.PolyglotCommit || out.CortexCommit != in.CortexCommit {
		t.Errorf("round trip lost the pins: %+v", out)
	}

	t.Run("rewriting at end of run replaces the file", func(t *testing.T) {
		in.EndedAt = "2026-08-06T12:34:56Z"
		if err := WriteRunMeta(path, in); err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		var out RunMeta
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if out.EndedAt != in.EndedAt {
			t.Errorf("EndedAt = %q, want %q", out.EndedAt, in.EndedAt)
		}
	})
}

func readRows(t *testing.T, path string) []Row {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open results: %v", err)
	}
	defer f.Close()
	var out []Row
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var r Row
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("parse row %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	return out
}
