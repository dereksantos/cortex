//go:build !windows

package longmemeval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
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

	binary, err := benchmarks.ResolveCortexBinary()
	if err != nil {
		return nil, fmt.Errorf("longmemeval: %w", err)
	}

	axis := NormalizeAxis(pl.Q.QuestionType)
	if axis == "" {
		axis = "unknown"
	}

	// Hydrate only for cortex strategy. For baseline we leave the
	// store empty so cortex_search returns "empty" regardless of
	// what the model asks — that's the comparison apples-to-apples.
	if pl.Strategy == StrategyCortex {
		if err := hydrateHaystack(ctx, binary, workdir, pl.Q); err != nil {
			return nil, fmt.Errorf("hydrate %s: %w", pl.Q.QuestionID, err)
		}
	}

	// Drive the agent through `cortex code --json` so this benchmark
	// measures the same harness shipped to users. The system prompt
	// declares the QA framing but does NOT teach tool usage — per
	// eval-principles #2, that's coaching and would launder the score.
	out, err := benchmarks.RunCode(ctx, binary, benchmarks.CodeOpts{
		Workdir:      workdir,
		Model:        b.model,
		Prompt:       pl.Q.Question,
		SystemPrompt: longmemevalSystemPrompt,
	})
	if err != nil {
		// A hard failure (no key, CLI crash) emits a cell with
		// TaskSuccess=false and a Notes diagnosis rather than
		// bubbling — that way one bad instance doesn't tank an
		// entire run, and the cell stays in the rollup as a
		// signal of fragility.
		return makeCellResult(b, pl, axis, "", nil, false, "harness error: "+err.Error()), nil
	}
	finalText := out.Final

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

	return makeCellResult(b, pl, axis, finalText, out, verdictCorrect, notes), nil
}

// hydrateHaystack marshals each turn of each haystack session into an
// events.Event and ships them all to `cortex capture --bulk --workdir`
// in a single subprocess call, then drains the journal via
// `cortex ingest --workdir`. The result lands in <workdir>/.cortex/
// where the subsequent `cortex code` call's cortex_search tool reads
// from the same store.
func hydrateHaystack(ctx context.Context, binary, workdir string, q Question) error {
	evs := buildHaystackEvents(q)
	if err := benchmarks.RunBulkCapture(ctx, binary, workdir, evs); err != nil {
		return fmt.Errorf("bulk capture: %w", err)
	}
	if err := benchmarks.RunIngest(ctx, binary, workdir); err != nil {
		return fmt.Errorf("ingest: %w", err)
	}
	return nil
}

// buildHaystackEvents flattens a Question's haystack sessions into a
// list of events.Event values ready for bulk capture. Pure function:
// no I/O, no time-of-day dependence beyond the event timestamps
// derived from haystack_dates. Empty turns are dropped (a blank turn
// is metadata, not content worth indexing).
func buildHaystackEvents(q Question) []*events.Event {
	var evs []*events.Event
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
			evs = append(evs, &events.Event{
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
			})
		}
	}
	return evs
}

// longmemevalSystemPrompt is FRAMING ONLY (per eval-principles #2). It
// declares the task role and the expected answer format. It does NOT:
//   - Mention cortex_search by name.
//   - Prescribe a search workflow ("call cortex_search aggressively").
//   - Instruct the model to ignore "I don't have access" reflexes.
//
// The previous version coached all three explicitly, which laundered
// the benchmark's score — measuring "what we taught the model to do"
// instead of "what the production harness does naturally". Keep this
// prompt narrow; if a future change requires re-adding any of the
// stripped lines, that requires a principle-2 reconsideration first,
// not a silent edit.
//
// (Background on why a per-benchmark override exists at all: the
// default `cortex code` system prompt frames the model as a Go
// programmer, which makes personal-knowledge questions feel out-of-
// scope. Generalizing the default is tracked separately; this prompt
// is the minimum framing needed to keep the QA semantics legible.)
const longmemevalSystemPrompt = `You are a helpful assistant answering questions about prior conversations with the user.

If you genuinely cannot find the answer in your available context, say "I don't know" rather than inventing a fact. Keep answers short: one to three sentences usually suffices.`

// makeCellResult builds a fully-populated, validation-ready CellResult.
// out may be nil when the upstream CLI call failed before producing
// telemetry; in that case the token/cost/turn counters stay at zero,
// which the rollup layer interprets as "instance attempted but no
// agent activity happened".
func makeCellResult(b *Benchmark, pl InstancePayload, axis, finalText string, out *benchmarks.CodeOutput, success bool, notes string) *evalv2.CellResult {
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
		TaskSuccess:          success,
		TaskSuccessCriterion: evalv2.CriterionJudgeLLM,
		Notes:                notes,
	}
	if out != nil {
		cell.TokensIn = out.TokensIn
		cell.TokensOut = out.TokensOut
		cell.CostUSD = out.CostUSD
		cell.LatencyMs = out.LatencyMs
		cell.AgentTurnsTotal = out.Turns
	}
	if strategy == evalv2.StrategyCortex {
		cell.CortexVersion = evalv2.CortexVersion
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
