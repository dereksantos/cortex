package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// sink.go is the structured record of a run. Rows are appended and fsynced as
// each exercise finishes — never reconstructed at the end from logs. A run
// killed halfway through therefore still leaves a complete, valid record of
// everything that did finish.

// Row is one exercise's result. Written to results.jsonl.
type Row struct {
	Exercise       string `json:"exercise"`
	Pass           bool   `json:"pass"`
	ToolCalls      int    `json:"tool_calls"`
	TokensIn       int    `json:"tokens_in"`
	TokensOut      int    `json:"tokens_out"`
	WallMs         int64  `json:"wall_ms"`
	FailureClass   string `json:"failure_class"`
	TranscriptPath string `json:"transcript_path"`

	// Beyond the required set: the evidence behind failure_class, so a row
	// can be audited without re-reading the transcript.
	SessionID     string `json:"session_id,omitempty"`
	MutatingCalls int    `json:"mutating_calls"`
	FilesChanged  int    `json:"files_changed"`
	AgentTurns    int    `json:"agent_turns"`
	VerifyMs      int64  `json:"verify_ms"`
	WorkDir       string `json:"work_dir"`
	Error         string `json:"error,omitempty"`
}

// Sink appends rows to results.jsonl, flushing each one to disk immediately.
type Sink struct {
	f *os.File
}

// NewSink opens (creating or truncating) the results file at path.
func NewSink(path string) (*Sink, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create results dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open results file %s: %w", path, err)
	}
	return &Sink{f: f}, nil
}

// Append writes one row and fsyncs it.
func (s *Sink) Append(r Row) error {
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("failed to encode row for %s: %w", r.Exercise, err)
	}
	if _, err := s.f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("failed to append row for %s: %w", r.Exercise, err)
	}
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("failed to flush row for %s: %w", r.Exercise, err)
	}
	return nil
}

func (s *Sink) Close() error { return s.f.Close() }

// RunMeta is run.json: everything needed to say what produced these numbers.
type RunMeta struct {
	RunID     string `json:"run_id"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`

	Model       string   `json:"model"`
	StudyModel  string   `json:"study_model"`
	Window      int      `json:"window"`
	Temperature float64  `json:"temperature"`
	BackendType string   `json:"backend_type"`
	Endpoint    string   `json:"endpoint"`
	ToolGates   []string `json:"tool_gates,omitempty"`

	CortexCommit string `json:"cortex_commit"`
	CortexDirty  bool   `json:"cortex_dirty"`
	CortexBin    string `json:"cortex_bin"`

	PolyglotRepo   string `json:"polyglot_repo"`
	PolyglotCommit string `json:"polyglot_commit"`
	Language       string `json:"language"`

	Host      string `json:"host"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	GoVersion string `json:"go_version"`

	TurnTimeout string   `json:"turn_timeout"`
	TestTimeout string   `json:"test_timeout"`
	Exercises   []string `json:"exercises"`

	ResultsPath     string `json:"results_path"`
	TranscriptsPath string `json:"transcripts_path"`
}

// WriteRunMeta writes (or rewrites) run.json.
func WriteRunMeta(path string, m RunMeta) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create run dir: %w", err)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode run metadata: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

// Summary is the end-of-run report computed from the rows.
type Summary struct {
	Total        int
	Passed       int
	ClassCounts  map[string]int
	TokensIn     int
	TokensOut    int
	MedTokensIn  int
	MedTokensOut int
	WallMsTotal  int64
	WallMsMedian int64
}

// Summarize computes pass@1 and the token/wall-clock distribution.
func Summarize(rows []Row) Summary {
	s := Summary{Total: len(rows), ClassCounts: map[string]int{}}
	in := make([]int, 0, len(rows))
	out := make([]int, 0, len(rows))
	wall := make([]int64, 0, len(rows))
	for _, r := range rows {
		if r.Pass {
			s.Passed++
		} else {
			s.ClassCounts[r.FailureClass]++
		}
		s.TokensIn += r.TokensIn
		s.TokensOut += r.TokensOut
		s.WallMsTotal += r.WallMs
		in = append(in, r.TokensIn)
		out = append(out, r.TokensOut)
		wall = append(wall, r.WallMs)
	}
	s.MedTokensIn = medianInt(in)
	s.MedTokensOut = medianInt(out)
	s.WallMsMedian = medianInt64(wall)
	return s
}

// PassRate is pass@1 in [0,1]; zero exercises reports 0.
func (s Summary) PassRate() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Passed) / float64(s.Total)
}

// medianInt returns the lower median of xs (xs is copied, not reordered).
func medianInt(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	c := append([]int(nil), xs...)
	sort.Ints(c)
	return c[(len(c)-1)/2]
}

func medianInt64(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	c := append([]int64(nil), xs...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return c[(len(c)-1)/2]
}

// classOrder fixes the report's failure-class ordering so two runs are
// diffable line-for-line.
var classOrder = []string{ClassWrongCode, ClassEarlyFinalize, ClassChatMode, ClassTimeout, ClassError}

// PrintSummary renders the end-of-run report.
func PrintSummary(w io.Writer, m RunMeta, rows []Row) {
	s := Summarize(rows)
	fmt.Fprintf(w, "\n%s\n", strings.Repeat("=", 68))
	fmt.Fprintf(w, "polyglot go slice — run %s\n", m.RunID)
	fmt.Fprintf(w, "model %s @ %s (temp %.2f) | polyglot %s\n",
		m.Model, m.Endpoint, m.Temperature, shortSHA(m.PolyglotCommit))
	fmt.Fprintf(w, "%s\n", strings.Repeat("=", 68))

	for _, r := range rows {
		verdict := "FAIL"
		if r.Pass {
			verdict = "PASS"
		}
		class := r.FailureClass
		if class == "" {
			class = "-"
		}
		fmt.Fprintf(w, "  %-4s %-26s %-15s %5d tools %7d/%-7d tok %6.1fs\n",
			verdict, r.Exercise, class, r.ToolCalls, r.TokensIn, r.TokensOut,
			float64(r.WallMs)/1000)
	}

	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 68))
	fmt.Fprintf(w, "pass@1        %d/%d (%.1f%%)\n", s.Passed, s.Total, s.PassRate()*100)
	for _, c := range classOrder {
		if n := s.ClassCounts[c]; n > 0 {
			fmt.Fprintf(w, "  %-14s %d\n", c, n)
		}
	}
	fmt.Fprintf(w, "tokens in     %d total, %d median\n", s.TokensIn, s.MedTokensIn)
	fmt.Fprintf(w, "tokens out    %d total, %d median\n", s.TokensOut, s.MedTokensOut)
	fmt.Fprintf(w, "wall clock    %s total, %s median\n",
		roundDur(time.Duration(s.WallMsTotal)*time.Millisecond),
		roundDur(time.Duration(s.WallMsMedian)*time.Millisecond))
	fmt.Fprintf(w, "results       %s\n", m.ResultsPath)
	fmt.Fprintf(w, "transcripts   %s\n", m.TranscriptsPath)
}

func roundDur(d time.Duration) string {
	if d < time.Minute {
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
