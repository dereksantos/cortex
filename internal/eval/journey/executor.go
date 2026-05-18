//go:build !windows

// executor.go — Phase D full-execution adapter for journey scenarios.
//
// Where SeedJourney proves the journey → Cortex-context pipeline works,
// ExecuteJourney closes the loop: it copies the scaffold into a temp
// workdir, seeds prior-session events into that workdir's .cortex
// storage, drives a CortexHarness against each session's coding task,
// and scores acceptance (tests pass + pattern matches).
//
// Requires an OpenRouter API key (via macOS keychain "cortex-openrouter"
// or $OPEN_ROUTER_API_KEY). Without a key, the suite-level entrypoint
// falls back to seed-only mode and reports "execution_skipped: no LLM".
//
// Per eval-principles 4 (Reproducible) + 5 (Isolated): per-scenario
// temp dir is destroyed after the run; no shared state between
// scenarios. The model id is recorded in every emitted cell_result row
// so reruns are diff-able against the same provider/model surface.
package journey

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/secret"
	"gopkg.in/yaml.v3"
)

// ExecutionReport is the per-scenario outcome from the full-execution
// adapter. One report per scenario, but each report aggregates every
// session-with-a-task in that scenario.
type ExecutionReport struct {
	ScenarioID     string            `json:"scenario_id"`
	Model          string            `json:"model"`
	WorkdirPath    string            `json:"workdir_path,omitempty"`
	Sessions       []SessionResult   `json:"sessions"`
	OverallOK      bool              `json:"overall_ok"`
	ErrorMessage   string            `json:"error_message,omitempty"`
	LatencyMs      int64             `json:"latency_ms"`
}

// SessionResult is the per-session outcome inside an ExecutionReport.
// Only sessions with a Task or Queries block produce one of these.
type SessionResult struct {
	SessionID         string   `json:"session_id"`
	Phase             string   `json:"phase"`
	Kind              string   `json:"kind"` // "task" | "queries" | "events"
	OK                bool     `json:"ok"`
	TestsPassed       bool     `json:"tests_passed"`
	PatternsRequired  []string `json:"patterns_required_matched,omitempty"`
	PatternsForbidden []string `json:"patterns_forbidden_found,omitempty"`
	QueriesPassed     int      `json:"queries_passed,omitempty"`
	QueriesTotal      int      `json:"queries_total,omitempty"`
	HarnessTurns      int      `json:"harness_turns,omitempty"`
	HarnessTokensIn   int      `json:"harness_tokens_in,omitempty"`
	HarnessTokensOut  int      `json:"harness_tokens_out,omitempty"`
	HarnessCostUSD    float64  `json:"harness_cost_usd,omitempty"`
	LatencyMs         int64    `json:"latency_ms"`
	ErrorMessage      string   `json:"error_message,omitempty"`
}

// ExecuteJourney drives one journey end-to-end. Returns an
// ExecutionReport summarizing per-session outcomes plus an overall
// pass/fail.
//
// Flow per scenario:
//
//  1. Create temp workdir; copy scaffold tree in.
//  2. Iterate sessions in order:
//     - events: seed into <workdir>/.cortex/data/insights.jsonl
//       (cumulative across sessions — later sessions see earlier).
//     - task: prompt the harness; run `go test`; check patterns.
//     - queries: run Reflex; verify expected_recall IDs surface.
//  3. Tear down workdir.
//
// Per eval-principles 5 (Isolated): workdir is unique per call. Per
// eval-principles 6 (Structured): every session emits a row into
// cell_results.jsonl when cellSink is non-nil.
func ExecuteJourney(ctx context.Context, s *Scenario, model string, cellSink io.Writer) ExecutionReport {
	start := time.Now()
	rep := ExecutionReport{ScenarioID: s.ID, Model: model}

	tempDir, err := os.MkdirTemp("", "cortex-journey-exec-*")
	if err != nil {
		rep.ErrorMessage = fmt.Sprintf("tempdir: %v", err)
		return rep
	}
	rep.WorkdirPath = tempDir
	defer os.RemoveAll(tempDir)

	if err := copyTree(s.Project.Scaffold, tempDir); err != nil {
		rep.ErrorMessage = fmt.Sprintf("copy scaffold: %v", err)
		return rep
	}

	// Seed storage path lives under <workdir>/.cortex/data/. The
	// CortexHarness's cortex_search tool opens its store at
	// <workdir>/.cortex, so the same path serves both seed-write +
	// agent-read.
	cortexDir := filepath.Join(tempDir, ".cortex")
	dataDir := filepath.Join(cortexDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		rep.ErrorMessage = fmt.Sprintf("mkdir cortex dir: %v", err)
		return rep
	}

	harness, err := evalv2.NewCortexHarness(model)
	if err != nil {
		rep.ErrorMessage = fmt.Sprintf("new harness: %v", err)
		return rep
	}

	jsonlPath := filepath.Join(dataDir, "insights.jsonl")
	jsonlFile, err := os.Create(jsonlPath)
	if err != nil {
		rep.ErrorMessage = fmt.Sprintf("open insights jsonl: %v", err)
		return rep
	}
	defer jsonlFile.Close()
	enc := json.NewEncoder(jsonlFile)
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	var eventID int64

	overallOK := true
	for _, sess := range s.Sessions {
		switch {
		case len(sess.Events) > 0:
			// Pure events session — append to insights.jsonl. No
			// emitted SessionResult (these are setup, not scored
			// independently).
			for _, ev := range sess.Events {
				eventID++
				rec := map[string]any{
					"id":          eventID,
					"event_id":    ev.ID,
					"category":    ev.Type,
					"summary":     ev.Content,
					"importance":  ev.Importance,
					"tags":        ev.Tags,
					"reasoning":   ev.Rationale,
					"session_id":  sess.ID,
					"source_type": "journey",
					"created_at":  base.Add(time.Duration(eventID) * time.Second),
				}
				_ = enc.Encode(rec)
			}
			continue

		case sess.Task != nil:
			// Sync writes before the agent reads them via cortex_search.
			_ = jsonlFile.Sync()
			sr := executeTaskSession(ctx, harness, tempDir, &sess)
			rep.Sessions = append(rep.Sessions, sr)
			if !sr.OK {
				overallOK = false
			}
			if cellSink != nil {
				_ = writeCellResult(cellSink, s.ID, model, sr)
			}

		case len(sess.Queries) > 0:
			_ = jsonlFile.Sync()
			sr := executeQuerySession(ctx, tempDir, &sess)
			rep.Sessions = append(rep.Sessions, sr)
			if !sr.OK {
				overallOK = false
			}
			if cellSink != nil {
				_ = writeCellResult(cellSink, s.ID, model, sr)
			}
		}
	}

	rep.OverallOK = overallOK
	rep.LatencyMs = time.Since(start).Milliseconds()
	return rep
}

// executeTaskSession runs the harness against one task and scores
// acceptance.
func executeTaskSession(ctx context.Context, h *evalv2.CortexHarness, workdir string, sess *Session) SessionResult {
	start := time.Now()
	sr := SessionResult{SessionID: sess.ID, Phase: sess.Phase, Kind: "task"}

	if sess.Task.MaxTurns > 0 {
		h.SetMaxTurns(sess.Task.MaxTurns)
	}

	// Per-task context bound: cap at the YAML's timeout, or 5min default.
	timeout := 5 * time.Minute
	if sess.Task.Timeout != "" {
		if d, err := time.ParseDuration(sess.Task.Timeout); err == nil {
			timeout = d
		}
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	prompt := buildTaskPrompt(sess.Task)
	hr, runErr := h.RunSessionWithResult(tctx, prompt, workdir)
	sr.HarnessTurns = hr.AgentTurnsTotal
	sr.HarnessTokensIn = hr.TokensIn
	sr.HarnessTokensOut = hr.TokensOut
	sr.HarnessCostUSD = hr.CostUSD

	if runErr != nil {
		sr.ErrorMessage = fmt.Sprintf("harness run: %v", runErr)
		sr.LatencyMs = time.Since(start).Milliseconds()
		return sr
	}

	// Test pass check: run `go test ./...` in workdir.
	if len(sess.Task.Acceptance.TestsPass) > 0 {
		sr.TestsPassed = runGoTests(workdir)
	} else {
		sr.TestsPassed = true // not asserted; treat as pass
	}

	// Pattern checks across files_to_modify.
	if len(sess.Task.Acceptance.PatternsRequired) > 0 || len(sess.Task.Acceptance.PatternsForbidden) > 0 {
		corpus := readFilesCorpus(workdir, sess.Task.FilesToModify)
		for _, p := range sess.Task.Acceptance.PatternsRequired {
			if strings.Contains(corpus, p) {
				sr.PatternsRequired = append(sr.PatternsRequired, p)
			}
		}
		for _, p := range sess.Task.Acceptance.PatternsForbidden {
			if strings.Contains(corpus, p) {
				sr.PatternsForbidden = append(sr.PatternsForbidden, p)
			}
		}
	}

	// OK iff: tests pass AND all required patterns matched AND no
	// forbidden patterns found.
	requiredOK := len(sr.PatternsRequired) == len(sess.Task.Acceptance.PatternsRequired)
	forbiddenOK := len(sr.PatternsForbidden) == 0
	sr.OK = sr.TestsPassed && requiredOK && forbiddenOK
	sr.LatencyMs = time.Since(start).Milliseconds()
	return sr
}

// executeQuerySession runs Reflex against the seeded storage and
// verifies that each query's expected_recall IDs surface.
func executeQuerySession(_ context.Context, workdir string, sess *Session) SessionResult {
	start := time.Now()
	sr := SessionResult{SessionID: sess.ID, Phase: sess.Phase, Kind: "queries", QueriesTotal: len(sess.Queries)}

	store, err := storage.New(&config.Config{ContextDir: filepath.Join(workdir, ".cortex")})
	if err != nil {
		sr.ErrorMessage = fmt.Sprintf("open storage: %v", err)
		sr.LatencyMs = time.Since(start).Milliseconds()
		return sr
	}
	defer store.Close()
	r := intcognition.NewReflex(store, nil)

	for _, q := range sess.Queries {
		results, qerr := r.Reflex(context.Background(), cognition.Query{Text: q.Text, Limit: 10})
		if qerr != nil {
			continue
		}
		gotIDs := make(map[string]bool, len(results))
		for _, res := range results {
			gotIDs[res.ID] = true
		}
		allFound := true
		for _, want := range q.ExpectedRecall {
			if !gotIDs[want] {
				allFound = false
				break
			}
		}
		if allFound {
			sr.QueriesPassed++
		}
	}
	sr.OK = sr.QueriesPassed == sr.QueriesTotal && sr.QueriesTotal > 0
	sr.LatencyMs = time.Since(start).Milliseconds()
	return sr
}

// buildTaskPrompt assembles the harness prompt from the task's
// description + hints. Hints are appended as a bulleted "Hints:" block
// so the model can use them but isn't required to.
func buildTaskPrompt(t *Task) string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(t.Description))
	if len(t.FilesToModify) > 0 {
		sb.WriteString("\n\nFiles to modify:\n")
		for _, f := range t.FilesToModify {
			fmt.Fprintf(&sb, "  - %s\n", f)
		}
	}
	if len(t.Hints) > 0 {
		sb.WriteString("\nHints:\n")
		for _, h := range t.Hints {
			fmt.Fprintf(&sb, "  - %s\n", h)
		}
	}
	return sb.String()
}

// runGoTests runs `go test ./...` in workdir. Returns true iff exit 0.
// Uses a 90-second hard timeout so a hung test doesn't pin the suite.
func runGoTests(workdir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = workdir
	return cmd.Run() == nil
}

// readFilesCorpus returns the concatenated text content of the given
// paths (relative to workdir). Missing files are skipped silently —
// the caller scores presence/absence of patterns, not file existence.
func readFilesCorpus(workdir string, paths []string) string {
	var sb strings.Builder
	for _, p := range paths {
		data, err := os.ReadFile(filepath.Join(workdir, p))
		if err != nil {
			continue
		}
		sb.Write(data)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// copyTree copies src/* into dst/*. Skips .git and .cortex directories
// since both are recreated per-run. Symlinks are followed (the eval
// scaffolds don't use them today).
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" || rel == ".cortex" {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_RDWR|os.O_CREATE|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// writeCellResult emits one cell_results.jsonl row per session. Schema
// is the minimal CellResult subset other paths emit: scenario_id +
// model + per-session telemetry. The grid runner's full schema is
// richer; this writes only what the journey path can populate.
func writeCellResult(w io.Writer, scenarioID, model string, sr SessionResult) error {
	rec := map[string]any{
		"scenario_id":         scenarioID,
		"sub_id":              sr.SessionID,
		"phase":               sr.Phase,
		"kind":                sr.Kind,
		"harness":             "cortex",
		"provider":            "openrouter",
		"model":               model,
		"ok":                  sr.OK,
		"tests_passed":        sr.TestsPassed,
		"patterns_required":   sr.PatternsRequired,
		"patterns_forbidden":  sr.PatternsForbidden,
		"queries_passed":      sr.QueriesPassed,
		"queries_total":       sr.QueriesTotal,
		"agent_turns_total":   sr.HarnessTurns,
		"tokens_in":           sr.HarnessTokensIn,
		"tokens_out":          sr.HarnessTokensOut,
		"cost_usd":            sr.HarnessCostUSD,
		"latency_ms":          sr.LatencyMs,
		"error_message":       sr.ErrorMessage,
		"source":              "journey",
	}
	enc := json.NewEncoder(w)
	return enc.Encode(rec)
}

// RunSuiteWithExecution runs RunSuite, then for every pending_adapter
// scenario also runs ExecuteJourney. Returns the suite result + a list
// of per-scenario execution reports.
//
// Requires an OpenRouter key. When no key is reachable, returns the
// suite result with a nil execution-reports slice and an error so
// callers can fall back to seed-only mode.
//
// model is the OpenRouter model id (e.g. "anthropic/claude-3-5-haiku"
// or "qwen/qwen-2.5-coder-32b-instruct"). The harness defaults are
// suitable for haiku-class models; smaller models may need
// SetMinimalTools.
//
// scenarioFilter limits execution to scenarios whose ID is in the
// filter (empty = all). Useful for iteration on a single journey.
//
// cellSink (if non-nil) receives one JSONL row per session.
func RunSuiteWithExecution(ctx context.Context, dir, model string, scenarioFilter []string, cellSink io.Writer) (*SuiteResult, []ExecutionReport, error) {
	if _, _, err := secret.MustOpenRouterKey(); err != nil {
		return nil, nil, fmt.Errorf("execution adapter requires OpenRouter key: %w", err)
	}

	suite, err := RunSuite(dir)
	if err != nil {
		return nil, nil, err
	}

	filterSet := make(map[string]bool, len(scenarioFilter))
	for _, id := range scenarioFilter {
		filterSet[id] = true
	}

	pattern := filepath.Join(dir, "*.yaml")
	matches, _ := filepath.Glob(pattern)
	reports := make([]ExecutionReport, 0, len(matches))
	for _, path := range matches {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			continue
		}
		var s Scenario
		if uerr := yaml.Unmarshal(data, &s); uerr != nil {
			continue
		}
		if s.Project.Scaffold == "" {
			continue
		}
		if _, ferr := os.Stat(s.Project.Scaffold); ferr != nil {
			continue
		}
		if len(filterSet) > 0 && !filterSet[s.ID] {
			continue
		}
		rep := ExecuteJourney(ctx, &s, model, cellSink)
		reports = append(reports, rep)
	}

	return suite, reports, nil
}
