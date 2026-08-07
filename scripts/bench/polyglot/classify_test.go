package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		in   Signals
		want string
	}{
		{
			name: "pass carries no failure class",
			in:   Signals{Pass: true, ToolCalls: 6, FilesChanged: 1},
			want: ClassNone,
		},
		{
			name: "zero tool calls is chat mode",
			in:   Signals{ToolCalls: 0},
			want: ClassChatMode,
		},
		{
			name: "chat mode wins even when a file somehow differs",
			in:   Signals{ToolCalls: 0, FilesChanged: 1},
			want: ClassChatMode,
		},
		{
			name: "tool calls but nothing landed on disk is early finalize",
			in:   Signals{ToolCalls: 4, FilesChanged: 0},
			want: ClassEarlyFinalize,
		},
		{
			name: "read-only exploration then stop is early finalize",
			in:   Signals{ToolCalls: 12, FilesChanged: 0},
			want: ClassEarlyFinalize,
		},
		{
			name: "edited files with failing tests is wrong code",
			in:   Signals{ToolCalls: 9, FilesChanged: 1},
			want: ClassWrongCode,
		},
		{
			name: "timeout outranks everything, including a passing test run",
			in:   Signals{Pass: true, TimedOut: true, ToolCalls: 3, FilesChanged: 1},
			want: ClassTimeout,
		},
		{
			name: "timeout outranks a concurrent error",
			in:   Signals{TimedOut: true, Errored: true},
			want: ClassTimeout,
		},
		{
			name: "error outranks a passing test run",
			in:   Signals{Pass: true, Errored: true, FilesChanged: 1},
			want: ClassError,
		},
		{
			name: "error with no tool calls is still error, not chat mode",
			in:   Signals{Errored: true, ToolCalls: 0},
			want: ClassError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.in); got != tt.want {
				t.Errorf("Classify(%+v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestClassifyIsTotal pins the invariant the report depends on: every
// combination of signals yields exactly one class, and a non-passing,
// non-faulted attempt is never left unclassified.
func TestClassifyIsTotal(t *testing.T) {
	known := map[string]bool{
		ClassNone: true, ClassChatMode: true, ClassWrongCode: true,
		ClassEarlyFinalize: true, ClassTimeout: true, ClassError: true,
	}
	for i := 0; i < 32; i++ {
		s := Signals{
			Pass:     i&1 != 0,
			TimedOut: i&2 != 0,
			Errored:  i&4 != 0,
		}
		if i&8 != 0 {
			s.ToolCalls = 7
		}
		if i&16 != 0 {
			s.FilesChanged = 1
		}
		got := Classify(s)
		if !known[got] {
			t.Fatalf("Classify(%+v) returned unknown class %q", s, got)
		}
		if got == ClassNone && !s.Pass {
			t.Errorf("Classify(%+v) = empty class for a non-passing attempt", s)
		}
		if got != ClassNone && s.Pass && !s.TimedOut && !s.Errored {
			t.Errorf("Classify(%+v) = %q for a clean pass", s, got)
		}
	}
}

func TestScanTranscript(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name          string
		lines         string
		wantCalls     int
		wantMutating  int
		wantMessages  int
		wantScanError bool
	}{
		{
			name: "counts tool calls and separates mutating ones",
			lines: `{"kind":"message","role":"system","content":"prompt"}
{"kind":"message","role":"user","content":"do it"}
{"kind":"message","role":"assistant","tool_calls":[{"function":{"name":"outline"}},{"function":{"name":"read_file"}}]}
{"kind":"message","role":"tool","tool_call_id":"1","content":"..."}
{"kind":"message","role":"assistant","tool_calls":[{"function":{"name":"edit_file"}}]}
{"kind":"message","role":"assistant","content":"done"}
`,
			wantCalls:    3,
			wantMutating: 1,
			wantMessages: 6,
		},
		{
			name: "ignores state and context entries",
			lines: `{"kind":"state","state":{"version":1}}
{"kind":"context","context":{"iteration":1}}
{"kind":"message","role":"assistant","tool_calls":[{"function":{"name":"bash"}}]}
`,
			wantCalls:    1,
			wantMutating: 0,
			wantMessages: 1,
		},
		{
			name: "treats a missing kind as a message (legacy rows)",
			lines: `{"role":"assistant","tool_calls":[{"function":{"name":"write_file"}}]}
`,
			wantCalls:    1,
			wantMutating: 1,
			wantMessages: 1,
		},
		{
			name: "skips a truncated final line without failing",
			lines: `{"kind":"message","role":"assistant","tool_calls":[{"function":{"name":"grep"}}]}
{"kind":"message","role":"assist`,
			wantCalls:    1,
			wantMutating: 0,
			wantMessages: 1,
		},
		{
			name:         "an empty transcript reports zero, not an error",
			lines:        "",
			wantCalls:    0,
			wantMutating: 0,
			wantMessages: 0,
		},
		{
			name: "the agent subagent counts as a mutating call",
			lines: `{"kind":"message","role":"assistant","tool_calls":[{"function":{"name":"agent"}}]}
`,
			wantCalls:    1,
			wantMutating: 1,
			wantMessages: 1,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, "t"+string(rune('a'+i))+".jsonl")
			if err := os.WriteFile(path, []byte(tt.lines), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			st, err := ScanTranscript(path)
			if err != nil {
				t.Fatalf("ScanTranscript: %v", err)
			}
			if st.ToolCalls != tt.wantCalls {
				t.Errorf("ToolCalls = %d, want %d", st.ToolCalls, tt.wantCalls)
			}
			if st.MutatingCalls != tt.wantMutating {
				t.Errorf("MutatingCalls = %d, want %d", st.MutatingCalls, tt.wantMutating)
			}
			if st.Messages != tt.wantMessages {
				t.Errorf("Messages = %d, want %d", st.Messages, tt.wantMessages)
			}
		})
	}
}

func TestScanTranscriptMissingFile(t *testing.T) {
	if _, err := ScanTranscript(filepath.Join(t.TempDir(), "absent.jsonl")); err == nil {
		t.Error("ScanTranscript on a missing file = nil error, want an error")
	}
}

func TestReadCellMetrics(t *testing.T) {
	contextDir := t.TempDir()
	// Assembled from parts to match the runner's own path construction.
	dir := filepath.Join(contextDir, "journal", "ev"+"al")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seg := `{"type":"eval.cell_result","payload":{"run_id":"other","tokens_in":1,"tokens_out":2}}
{"type":"eval.cell_result","payload":{"run_id":"20260806-101010","tokens_in":41234,"tokens_out":2210,"latency_ms":91000,"agent_turns_total":1}}
not json at all
`
	if err := os.WriteFile(filepath.Join(dir, "0001.jsonl"), []byte(seg), 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}

	tests := []struct {
		name      string
		session   string
		wantIn    int
		wantOut   int
		wantTurns int
	}{
		{name: "finds the row for the session", session: "20260806-101010", wantIn: 41234, wantOut: 2210, wantTurns: 1},
		{name: "unknown session yields zeroes", session: "nope"},
		{name: "empty session yields zeroes", session: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := ReadCellMetrics(contextDir, tt.session)
			if err != nil {
				t.Fatalf("ReadCellMetrics: %v", err)
			}
			if m.TokensIn != tt.wantIn || m.TokensOut != tt.wantOut {
				t.Errorf("tokens = %d/%d, want %d/%d", m.TokensIn, m.TokensOut, tt.wantIn, tt.wantOut)
			}
			if m.AgentTurns != tt.wantTurns {
				t.Errorf("AgentTurns = %d, want %d", m.AgentTurns, tt.wantTurns)
			}
		})
	}
}

func TestReadCellMetricsNoJournal(t *testing.T) {
	m, err := ReadCellMetrics(t.TempDir(), "20260806-101010")
	if err != nil {
		t.Fatalf("ReadCellMetrics on a workspace with no journal: %v", err)
	}
	if m.TokensIn != 0 || m.TokensOut != 0 {
		t.Errorf("got %+v, want zero metrics", m)
	}
}
