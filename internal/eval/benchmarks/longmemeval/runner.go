//go:build !windows

package longmemeval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/capture"
	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/internal/processor"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

// DefaultModel is the OpenRouter model under test when --model is
// omitted. Claude Haiku 4.5 is a budget-friendly default that the
// repo's spend ceiling tolerates for Oracle-split smoke runs.
const DefaultModel = "anthropic/claude-haiku-4.5"

// Benchmark is the longmemeval Benchmark implementation. Configuration
// gathered at Load() is retained on the struct so Run() can read it
// per-instance without re-parsing flags.
type Benchmark struct {
	model      string
	useJudge   bool
	judgeModel string
}

// New constructs an unconfigured benchmark. Defaults are applied at
// Load() if the caller didn't pass them through opts.Filter.
func New() *Benchmark { return &Benchmark{} }

// Name returns the registry key.
func (b *Benchmark) Name() string { return "longmemeval" }

// Load delegates JSON parsing to the package-level Load function and
// captures runtime flags (model, judge, judge-model) from opts.Filter.
// The default model is anthropic/claude-haiku-4.5; the default judge
// model matches DefaultJudgeModel (also Haiku 4.5, see judge.go).
func (b *Benchmark) Load(ctx context.Context, opts benchmarks.LoadOpts) ([]benchmarks.Instance, error) {
	b.model = strings.TrimSpace(opts.Filter[FilterModel])
	if b.model == "" {
		b.model = DefaultModel
	}
	b.useJudge = strings.EqualFold(opts.Filter[FilterJudge], "true")
	b.judgeModel = strings.TrimSpace(opts.Filter[FilterJudgeModel])
	if b.judgeModel == "" {
		b.judgeModel = DefaultJudgeModel
	}
	return Load(ctx, opts)
}

// Run executes one instance and returns a fully-validated CellResult.
// Branches on InstancePayload.Strategy:
//   - cortex: hydrate the haystack into <workdir>/.cortex, register
//     cortex_search, drive the harness with the question.
//   - baseline: skip hydration; cortex_search returns "empty" since the
//     store is empty. ContextStrategy is set to baseline so the
//     emitted cell explicitly does NOT count toward Cortex's
//     attributable token budget.
//
// Judge: when --judge is wired, the env.JudgeProvider is called with
// the package's reference prompt. Without a judge, TaskSuccess is set
// to false and a Notes line records "no judge".
func (b *Benchmark) Run(ctx context.Context, inst benchmarks.Instance, env benchmarks.Env) (*evalv2.CellResult, error) {
	pl, ok := inst.Payload.(InstancePayload)
	if !ok {
		return nil, fmt.Errorf("longmemeval: payload type=%T want InstancePayload", inst.Payload)
	}
	if env.Workdir == "" {
		return nil, fmt.Errorf("longmemeval: env.Workdir is required")
	}
	workdir, err := filepath.Abs(env.Workdir)
	if err != nil {
		return nil, fmt.Errorf("abs workdir: %w", err)
	}
	storeDir := filepath.Join(workdir, ".cortex")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir .cortex: %w", err)
	}

	axis := NormalizeAxis(pl.Q.QuestionType)
	if axis == "" {
		axis = "unknown"
	}

	// Hydrate only for cortex strategy. For baseline we leave the
	// store empty so cortex_search returns "empty" regardless of
	// what the model asks — that's the comparison apples-to-apples.
	if pl.Strategy == StrategyCortex {
		if err := hydrateHaystack(ctx, storeDir, pl.Q); err != nil {
			return nil, fmt.Errorf("hydrate %s: %w", pl.Q.QuestionID, err)
		}
	}

	// Drive the harness. We use a thin local wrapper around the
	// existing CortexHarness so the package stays decoupled from
	// the harness construction details — switch one symbol if the
	// constructor signature changes upstream.
	hr, finalText, err := runCortexHarness(ctx, b.model, pl.Q.Question, workdir, env.Verbose)
	if err != nil {
		// A hard failure (no key, harness crash) emits a cell with
		// TaskSuccess=false and a Notes diagnosis rather than
		// bubbling — that way one bad instance doesn't tank an
		// entire run, and the cell stays in the rollup as a
		// signal of fragility.
		return makeCellResult(b, pl, axis, "", evalv2.HarnessResult{}, false, "harness error: "+err.Error()), nil
	}

	// Score. With --judge we have a real verdict; without it we
	// emit the cell but flag it.
	verdictCorrect := false
	notes := fmt.Sprintf("question_type=%s axis=%s", pl.Q.QuestionType, axis)
	if b.useJudge {
		if env.JudgeProvider == nil {
			notes += " judge=missing-provider"
		} else {
			verdict, jerr := Judge(ctx, env.JudgeProvider, pl.Q, finalText)
			if jerr != nil {
				notes += " judge_err=" + sanitizeNote(jerr.Error())
			} else {
				verdictCorrect = verdict.Correct
				notes += " judge_reason=" + sanitizeNote(verdict.Reason)
			}
		}
	} else {
		notes += " judge=disabled"
	}

	return makeCellResult(b, pl, axis, finalText, hr, verdictCorrect, notes), nil
}

// hydrateHaystack writes each turn of each haystack session into the
// per-eval Cortex journal, then drains the journal into storage so the
// harness's cortex_search tool sees the events when it opens its own
// view of the same store.
func hydrateHaystack(_ context.Context, storeDir string, q Question) error {
	// Build a config that points capture at <workdir>/.cortex. We do
	// NOT use config.DefaultConfig() — that resolves a project root
	// from cwd, which is the wrong path here.
	cfg := &config.Config{
		ContextDir: storeDir,
	}
	cap := capture.New(cfg)

	for i, session := range q.HaystackSessions {
		date := ""
		if i < len(q.HaystackDates) {
			date = q.HaystackDates[i]
		}
		sessionID := fmt.Sprintf("haystack-%d", i)
		if i < len(q.HaystackSessionIDs) {
			sessionID = q.HaystackSessionIDs[i]
		}
		ts := parseHaystackDate(date)

		for j, turn := range session {
			content := strings.TrimSpace(turn.Content)
			if content == "" {
				continue
			}
			// Use ToolUse + a synthetic "Capture" toolname so the
			// router stores via the existing capture path (the same
			// shape `cortex capture --type=observation` produces).
			// Category is "observation" — the haystack is
			// conversation history we want recallable, not a
			// "decision" the user made.
			ev := &events.Event{
				Source:    events.SourceClaude,
				EventType: events.EventToolUse,
				Timestamp: ts.Add(time.Duration(j) * time.Second),
				ToolName:  "Capture",
				ToolInput: map[string]interface{}{
					"type":       "observation",
					"content":    content,
					"role":       turn.Role,
					"session_id": sessionID,
					"has_answer": turn.HasAnswer,
				},
				ToolResult: content,
				Context: events.EventContext{
					SessionID: sessionID,
				},
				Metadata: map[string]interface{}{
					"capture_type":        "observation",
					"longmemeval_role":    turn.Role,
					"longmemeval_session": sessionID,
					"longmemeval_date":    date,
				},
			}
			if err := cap.CaptureEvent(ev); err != nil {
				return fmt.Errorf("capture session=%s turn=%d: %w", sessionID, j, err)
			}
		}
	}

	// Drain the journal into storage so cortex_search sees it on
	// the next call.
	store, err := storage.New(cfg)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()
	proc := processor.New(cfg, store)
	if _, err := proc.RunBatch(); err != nil {
		return fmt.Errorf("drain journal: %w", err)
	}
	return nil
}

// runCortexHarness wraps the v2 CortexHarness so this package does not
// import v2 in package scope (avoid a heavier graph). Returns the
// harness result and the model's final text reply for judging.
func runCortexHarness(ctx context.Context, model, question, workdir string, verbose bool) (evalv2.HarnessResult, string, error) {
	h, err := evalv2.NewCortexHarness(model)
	if err != nil {
		return evalv2.HarnessResult{}, "", err
	}
	if verbose {
		h.SetNotify(func(kind string, payload any) {
			fmt.Fprintf(os.Stderr, "  [harness %s] %v\n", kind, payload)
		})
	}
	hr, err := h.RunSessionWithResult(ctx, question, workdir)
	if err != nil {
		return hr, "", err
	}
	final := h.LastLoopResult().Final
	return hr, final, nil
}

// makeCellResult builds a fully-populated, validation-ready CellResult.
func makeCellResult(b *Benchmark, pl InstancePayload, axis, finalText string, hr evalv2.HarnessResult, success bool, notes string) *evalv2.CellResult {
	strategy := evalv2.StrategyCortex
	if pl.Strategy == StrategyBaseline {
		strategy = evalv2.StrategyBaseline
	}

	cell := &evalv2.CellResult{
		SchemaVersion:        evalv2.CellResultSchemaVersion,
		RunID:                newRunID(pl.Q.QuestionID, pl.Strategy),
		Timestamp:            time.Now().UTC().Format(time.RFC3339),
		ScenarioID:           "longmemeval/" + pl.Q.QuestionID,
		Benchmark:            "longmemeval",
		Harness:              evalv2.HarnessCortex,
		Provider:             evalv2.ProviderOpenRouter,
		Model:                b.model,
		ContextStrategy:      strategy,
		TokensIn:             hr.TokensIn,
		TokensOut:            hr.TokensOut,
		CostUSD:              hr.CostUSD,
		LatencyMs:            hr.LatencyMs,
		AgentTurnsTotal:      hr.AgentTurnsTotal,
		TaskSuccess:          success,
		TaskSuccessCriterion: evalv2.CriterionJudgeLLM,
		Notes:                notes,
	}
	if strategy == evalv2.StrategyCortex {
		cell.CortexVersion = evalv2.CortexVersion
		// InjectedContextTokens reflects what cortex_search injected
		// into the prompt. The CortexHarness surfaces this via
		// LastLoopResult; we only stamp it on cortex-strategy cells
		// because Validate refuses non-zero on baseline.
		// hr already carries the value through HarnessResult fields
		// the runner sees; right now harness/loop.LoopResult exposes
		// InjectedContextTokens but evalv2.HarnessResult does not
		// surface it directly. Leaving 0 here keeps the cell valid
		// while we lobby the harness layer to expose it.
	}
	// Suppress the assistant message from cell-level fields — we
	// don't have a Notes-safe place to log it; the journal entry
	// keeps the full transcript via the harness's own transcript
	// writer.
	_ = finalText
	return cell
}

// parseHaystackDate is tolerant of common upstream date formats.
// LongMemEval's haystack_dates uses formats like
// "2021/06/01 (Tue) 21:10"; question_date is usually "YYYY-MM-DD".
// Falls back to time.Now if parsing fails so capture writes still
// land; downstream rollups read the date from metadata, not from the
// event timestamp itself.
func parseHaystackDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().UTC()
	}
	// Strip the "(Tue)" weekday tag if present — leaves
	// "2021/06/01 21:10" which time.Parse can handle.
	if i := strings.Index(s, " ("); i >= 0 {
		if j := strings.Index(s[i:], ") "); j >= 0 {
			s = s[:i] + s[i+j+1:]
		}
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05Z",
		time.RFC3339,
		"2006/01/02 15:04",
		"2006-01-02 15:04",
		"2006/01/02",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

// sanitizeNote strips characters that would break the CellResult Notes
// JSON field on round-trip through SQLite + JSONL. Whitespace and
// quotes are the practical hazards; everything else is fine.
func sanitizeNote(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\"", "'")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return strings.TrimSpace(s)
}

// newRunID generates a stable-prefix, unique id usable as a CellResult.RunID.
func newRunID(questionID, strategy string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("longmemeval-%s-%s-%s", questionID, strategy, hex.EncodeToString(b[:]))
}
