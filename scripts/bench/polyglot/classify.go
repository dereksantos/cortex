package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// classify.go turns the raw evidence of one exercise attempt — the session
// transcript, the on-disk diff, the process outcome, the `go test` verdict —
// into exactly one failure_class.
//
// Every input is deterministic. No model is consulted anywhere in this file,
// and none should ever be: the exercises ship unit tests, so the verdict is
// `go test`'s exit code and nothing else.

// Failure classes. A passing exercise carries the empty class.
const (
	ClassNone          = ""
	ClassChatMode      = "chat_mode"
	ClassWrongCode     = "wrong_code"
	ClassEarlyFinalize = "early_finalize"
	ClassTimeout       = "timeout"
	ClassError         = "error"
)

// Signals is the deterministic evidence Classify decides on.
type Signals struct {
	// Pass is `go test ./...`'s exit code == 0 in the exercise workdir.
	Pass bool
	// TimedOut: the cortex turn hit --timeout and was killed.
	TimedOut bool
	// Errored: the cortex turn failed for a non-timeout reason — non-zero
	// exit, an "error" field in its --json output, or a harness fault
	// (workdir staging, transcript missing).
	Errored bool
	// ToolCalls is every tool call the model emitted across the turn.
	ToolCalls int
	// FilesChanged is how many of the exercise's declared solution files
	// differ (sha256) from the pristine stub after the turn.
	FilesChanged int
}

// Classify returns the single failure class for one attempt.
//
// The order encodes precedence: an attempt we cut short or that crashed is
// classified by how it ended, not by what it managed to write first. Only a
// turn that ran to completion is judged on its output.
//
//	timeout        the turn was killed at --timeout
//	error          the turn exited non-zero / reported an error
//	""             go test passed
//	chat_mode      zero tool calls — the model replied in prose, touching nothing
//	early_finalize tool calls but zero net change to the solution files
//	wrong_code     the solution files changed and the tests still fail
//
// early_finalize's threshold is "zero solution files differ from the pristine
// stub", NOT "zero write_file/edit_file calls". That is the defensible line:
// an edit_file whose match failed, a write_file that rewrote the stub
// byte-for-byte, and never attempting an edit at all are indistinguishable
// from the exercise's point of view — in all three the agent finalized with
// no work on disk. Counting attempted-but-landed-nothing calls as work would
// mis-file those as wrong_code and hide the real failure.
func Classify(s Signals) string {
	switch {
	case s.TimedOut:
		return ClassTimeout
	case s.Errored:
		return ClassError
	case s.Pass:
		return ClassNone
	case s.ToolCalls == 0:
		return ClassChatMode
	case s.FilesChanged == 0:
		return ClassEarlyFinalize
	default:
		return ClassWrongCode
	}
}

// TranscriptStats is what the session transcript tells us about the turn.
type TranscriptStats struct {
	// ToolCalls is every tool call emitted, of any kind.
	ToolCalls int
	// MutatingCalls is the subset that tried to change a file.
	MutatingCalls int
	// Messages is the number of transcript message rows (a liveness check:
	// zero means the transcript never got written).
	Messages int
}

// mutatingTools are the tool calls that attempt to change the workspace.
// Counted for reporting; the classifier itself trusts the on-disk diff.
var mutatingTools = map[string]bool{
	"write_file":  true,
	"edit_file":   true,
	"remove_path": true,
	// `agent` is the implementation subagent — it carries write_file /
	// edit_file inside it, so a call to it is an attempt to change files.
	"agent": true,
}

// transcriptRow is the subset of cmd/cortex's sessionEntry this runner reads.
// The transcript is append-only JSONL; unknown fields are ignored on purpose
// so a new entry kind upstream can't break the classifier.
type transcriptRow struct {
	Kind      string `json:"kind"`
	Role      string `json:"role"`
	ToolCalls []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// ScanTranscript counts tool calls in a cortex session transcript.
func ScanTranscript(path string) (TranscriptStats, error) {
	var st TranscriptStats
	f, err := os.Open(path)
	if err != nil {
		return st, fmt.Errorf("failed to open transcript %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Transcript lines carry whole tool results; the 64KiB default is far
	// too small. 8MiB matches the largest tool output cortex will emit.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row transcriptRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			// A truncated final line (killed mid-write) is expected on a
			// timeout — skip it rather than failing the whole exercise.
			continue
		}
		if row.Kind != "" && row.Kind != "message" {
			continue
		}
		st.Messages++
		for _, tc := range row.ToolCalls {
			st.ToolCalls++
			if mutatingTools[tc.Function.Name] {
				st.MutatingCalls++
			}
		}
	}
	if err := sc.Err(); err != nil {
		return st, fmt.Errorf("failed to read transcript %s: %w", path, err)
	}
	return st, nil
}

// CellMetrics is the token/latency accounting cortex itself writes to the
// per-workspace eval journal at the end of a headless turn
// (cmd/cortex/session_runtime.go's emitSessionMetrics). Reading it here —
// rather than re-deriving token counts — keeps the benchmark honest about
// what the harness actually billed.
type CellMetrics struct {
	TokensIn   int   `json:"tokens_in"`
	TokensOut  int   `json:"tokens_out"`
	LatencyMs  int64 `json:"latency_ms"`
	AgentTurns int   `json:"agent_turns_total"`
	RunID      string
}

type cellEnvelope struct {
	Type    string `json:"type"`
	Payload struct {
		RunID      string `json:"run_id"`
		TokensIn   int    `json:"tokens_in"`
		TokensOut  int    `json:"tokens_out"`
		LatencyMs  int64  `json:"latency_ms"`
		AgentTurns int    `json:"agent_turns_total"`
	} `json:"payload"`
}

// ReadCellMetrics finds the metrics row cortex wrote for the given session in
// the workspace's journal. A missing row is not an error — the turn may have
// been killed before it could emit one — so the caller gets zeroes.
func ReadCellMetrics(contextDir, sessionID string) (CellMetrics, error) {
	var out CellMetrics
	if sessionID == "" {
		return out, nil
	}
	// Path is assembled from parts so a plain grep for the journal class name
	// still finds this call site.
	dir := filepath.Join(contextDir, "journal", "eval")
	segments, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return out, fmt.Errorf("failed to list journal segments in %s: %w", dir, err)
	}
	for _, seg := range segments {
		f, err := os.Open(seg)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			var env cellEnvelope
			if err := json.Unmarshal(sc.Bytes(), &env); err != nil {
				continue
			}
			if env.Payload.RunID != sessionID {
				continue
			}
			out = CellMetrics{
				TokensIn:   env.Payload.TokensIn,
				TokensOut:  env.Payload.TokensOut,
				LatencyMs:  env.Payload.LatencyMs,
				AgentTurns: env.Payload.AgentTurns,
				RunID:      env.Payload.RunID,
			}
		}
		f.Close()
	}
	return out, nil
}
