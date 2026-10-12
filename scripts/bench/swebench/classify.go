package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// classify.go turns the evidence of one instance attempt into exactly one
// failure_class. The verdict itself (resolved or not) comes only from the
// official SWE-bench harness's report.json — nothing here judges a patch.

// How the cortex turn ended. Recorded on every row as turn_end.
const (
	EndCompleted  = "completed"
	EndTimeout    = "timeout"
	EndBudgetStop = "budget_stop"
	EndError      = "error"
	EndNotStarted = "not_started"
)

// Official-harness outcome for the submitted patch. Recorded as eval_status.
const (
	EvalResolved     = "resolved"
	EvalUnresolved   = "unresolved"
	EvalApplyFailed  = "patch_apply_failed"
	EvalError        = "eval_error"
	EvalSkippedEmpty = "skipped_empty_patch"
	EvalNotRun       = "not_run"
)

// Failure classes. A resolved instance carries "".
const (
	ClassNone        = ""
	ClassEnv         = "env_error"          // image pull / container / checkout failed before the agent ran
	ClassBudget      = "budget_stop"        // the spend guard interrupted the turn
	ClassTimeout     = "timeout"            // the turn hit --timeout
	ClassError       = "error"              // the turn exited non-zero / reported an error
	ClassChatMode    = "chat_mode"          // zero tool calls, empty diff
	ClassEmptyPatch  = "empty_patch"        // tool calls, but no diff against the base commit
	ClassApplyFailed = "patch_apply_failed" // the harness could not apply the diff
	ClassEvalError   = "eval_error"         // the harness failed to produce a report
	ClassWrongPatch  = "wrong_patch"        // applied cleanly, tests did not pass
)

// Signals is the deterministic evidence Classify decides on.
type Signals struct {
	EnvFailed  bool
	TurnEnd    string
	EvalStatus string
	ToolCalls  int
	PatchBytes int
}

// Classify returns the single failure class for one attempt. A resolved
// patch is a success however the turn ended (the diff on disk is the
// system's output, and the harness judged it). Otherwise an abnormal ending
// is the most informative cause, then what the patch looked like.
func Classify(s Signals) string {
	switch {
	case s.EnvFailed:
		return ClassEnv
	case s.EvalStatus == EvalResolved:
		return ClassNone
	case s.TurnEnd == EndBudgetStop:
		return ClassBudget
	case s.TurnEnd == EndTimeout:
		return ClassTimeout
	case s.TurnEnd == EndError:
		return ClassError
	case s.PatchBytes == 0 && s.ToolCalls == 0:
		return ClassChatMode
	case s.PatchBytes == 0:
		return ClassEmptyPatch
	case s.EvalStatus == EvalApplyFailed:
		return ClassApplyFailed
	case s.EvalStatus == EvalError || s.EvalStatus == EvalNotRun:
		return ClassEvalError
	default:
		return ClassWrongPatch
	}
}

// TranscriptStats is what the cortex session transcript says about a turn.
type TranscriptStats struct {
	ToolCalls     int
	MutatingCalls int
	Messages      int
	ToolCounts    map[string]int
}

var mutatingTools = map[string]bool{
	"write_file": true, "edit_file": true, "remove_path": true, "agent": true,
}

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
	st := TranscriptStats{ToolCounts: map[string]int{}}
	f, err := os.Open(path)
	if err != nil {
		return st, fmt.Errorf("failed to open transcript %s: %w", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row transcriptRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue // truncated final line after an interrupt
		}
		if row.Kind != "" && row.Kind != "message" {
			continue
		}
		st.Messages++
		for _, tc := range row.ToolCalls {
			st.ToolCalls++
			st.ToolCounts[tc.Function.Name]++
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

// CellMetrics is the accounting row cortex writes at the end of a headless
// turn (emitSessionMetrics → .cortex/journal/eval). It reports what the
// harness billed; a turn killed before completing writes none.
type CellMetrics struct {
	TokensIn        int
	TokensOut       int
	ReasoningTokens int
	CostUSD         float64
	AgentTurns      int
	Found           bool
}

type cellEnvelope struct {
	Payload struct {
		RunID           string  `json:"run_id"`
		TokensIn        int     `json:"tokens_in"`
		TokensOut       int     `json:"tokens_out"`
		ReasoningTokens int     `json:"reasoning_tokens"`
		CostUSD         float64 `json:"cost_usd"`
		AgentTurns      int     `json:"agent_turns_total"`
	} `json:"payload"`
}

// ReadCellMetrics finds the metrics row for sessionID under a copied
// .cortex directory. Missing is not an error.
func ReadCellMetrics(contextDir, sessionID string) (CellMetrics, error) {
	var out CellMetrics
	if sessionID == "" {
		return out, nil
	}
	segments, err := filepath.Glob(filepath.Join(contextDir, "journal", "eval", "*.jsonl"))
	if err != nil {
		return out, fmt.Errorf("failed to list journal segments: %w", err)
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
			if json.Unmarshal(sc.Bytes(), &env) != nil || env.Payload.RunID != sessionID {
				continue
			}
			out = CellMetrics{
				TokensIn:        env.Payload.TokensIn,
				TokensOut:       env.Payload.TokensOut,
				ReasoningTokens: env.Payload.ReasoningTokens,
				CostUSD:         env.Payload.CostUSD,
				AgentTurns:      env.Payload.AgentTurns,
				Found:           true,
			}
		}
		f.Close()
	}
	return out, nil
}

// PatchFiles counts the files a unified diff touches.
func PatchFiles(patch string) int {
	n := 0
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			n++
		}
	}
	return n
}
