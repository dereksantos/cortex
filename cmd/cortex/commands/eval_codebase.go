// `cortex eval codebase` — runs the codebase-reading eval suite
// (docs/eval-suite-codebase-reading.md). One row per fixture; mechanical
// metrics extracted from dag_traces.jsonl + answer text.
//
// Slice 1 ships the runner + R1/Q1/Q3 cortex fixtures. Slices 2–5 add
// the rest of the matrix, an LLM judge, a baseline/regression dashboard,
// and Python/Rust fixture projects.
package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/eval/codebase"
	intllm "github.com/dereksantos/cortex/internal/llm"
	"github.com/dereksantos/cortex/pkg/llm"
)

const codebaseUsage = `Usage: cortex eval codebase [flags]

Run the codebase-reading eval suite (R/Q/B fixtures under
test/evals/scenarios/codebase-reading). Each fixture drives one cortex
--prompt invocation and the runner extracts mechanical metrics from
dag_traces.jsonl + the final answer text.

Flags:
  -d, --dir DIR        Fixture directory (default: test/evals/scenarios/codebase-reading)
  -m, --model NAME     Override the REPL model (forwarded as --model)
      --binary PATH    Path to the cortex binary (default: "cortex" via PATH)
      --only ID        Run only fixtures with the given id or eval shorthand (e.g. Q3).
                       Repeatable; matches by full id, eval shorthand, or project name.
      --project NAME   Restrict to one fixture project (cortex|leanjs|python-todo|rust-weather).
      --fixture-root D Override the fixture-project root (slice-5 Python/Rust projects)
      --timeout SECS   Per-fixture wall-clock cap (default: 600)
      --temperature T  Pin sampling temperature (e.g. 0) for every LLM call —
                       judge AND cell subprocesses — via CORTEX_TEMPERATURE.
                       Omitted: each backend's default (~0.7), which makes
                       ~1/3 of cells flip run-to-run.
      --local-only     Keep every LLM call on the local fleet (sets
                       CORTEX_LOCAL_ONLY=1): excludes OpenRouter from the
                       registry and drops remote routing pins, so a remote
                       model's degradation can't collapse the suite.
      --judge-model M  Enable the slice-3 LLM judge for Q-class fixtures
                       carrying judge_rubric. Resolves via the same provider
                       resolver as the REPL — slash-prefixed routes to
                       OpenRouter, bare names route to local backends.
      --baseline       Persist the run as a baseline snapshot under
                       .cortex/db/eval_baselines/<commit>/<ts>.jsonl.
                       Future --compare runs diff against the newest
                       snapshot for the matching ref.
      --compare REF    Diff this run against a baseline ref (full/short SHA,
                       "HEAD", or "latest"). Prints a regression report
                       and exits non-zero if any fixture regressed.
  -h, --help           Show this help

Examples:
  cortex eval codebase
  cortex eval codebase --only Q3
  cortex eval codebase --binary ./bin/cortex --model anthropic/claude-haiku-4.5
`

func executeCodebase(args []string) error {
	dir := "test/evals/scenarios/codebase-reading"
	fixtureRoot := ""
	model := ""
	binary := ""
	only := []string{}
	project := ""
	timeoutSec := 600
	judgeModel := ""
	persistBaseline := false
	compareRef := ""
	temperature := ""
	localOnly := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d", "--dir":
			if i+1 < len(args) {
				dir = args[i+1]
				i++
			}
		case "-m", "--model":
			if i+1 < len(args) {
				model = args[i+1]
				i++
			}
		case "--binary":
			if i+1 < len(args) {
				binary = args[i+1]
				i++
			}
		case "--only":
			if i+1 < len(args) {
				only = append(only, args[i+1])
				i++
			}
		case "--project":
			if i+1 < len(args) {
				project = args[i+1]
				i++
			}
		case "--fixture-root":
			if i+1 < len(args) {
				fixtureRoot = args[i+1]
				i++
			}
		case "--timeout":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &timeoutSec)
				i++
			}
		case "--judge-model":
			if i+1 < len(args) {
				judgeModel = args[i+1]
				i++
			}
		case "--temperature":
			if i+1 < len(args) {
				temperature = args[i+1]
				i++
			}
		case "--local-only":
			localOnly = true
		case "--baseline":
			persistBaseline = true
		case "--compare":
			if i+1 < len(args) {
				compareRef = args[i+1]
				i++
			}
		case "-h", "--help":
			fmt.Print(codebaseUsage)
			return nil
		}
	}

	// Subprocess binary resolution: an explicit --binary wins; otherwise
	// fall back to CORTEX_BINARY. Without this, a baseline run invoked as
	// bare `cortex eval codebase --baseline` shells out to whatever
	// `cortex` is on PATH for each cell — which is easily a stale build
	// while iterations use the freshly built HEAD binary. That mismatch
	// silently produced budget_tokens=0 baselines (a stale bin/cortex
	// doesn't emit sense.estimate_scope), comparing HEAD iterations
	// against a stale-binary floor. The env fallback closes that hole.
	if binary == "" {
		if env := os.Getenv("CORTEX_BINARY"); env != "" {
			binary = env
		}
	}
	if binary != "" {
		fmt.Printf("[binary] cell subprocess = %s\n", binary)
	}

	// Determinism knob: --temperature pins sampling temperature for every
	// LLM call in the run. os.Setenv reaches BOTH levels through one
	// mechanism — the judge built in THIS process (BuildProvider reads
	// CORTEX_TEMPERATURE at construction) and each cell SUBPROCESS, which
	// inherits it because the runner's mergeEnv starts from os.Environ().
	// Without this, every provider sends no temperature and the backend
	// default (~0.7) makes ~1/3 of cells flip run-to-run.
	if temperature != "" {
		os.Setenv(llm.TemperatureEnv, temperature)
		fmt.Printf("[determinism] CORTEX_TEMPERATURE=%s (judge + cell subprocesses)\n", temperature)
	}

	// Local-only: keep every LLM call on the local fleet. Excludes the
	// OpenRouter probe from the registry and drops remote routing pins, so
	// nodes like sense.estimate_scope can't be routed to a remote frontier
	// model whose degradation would collapse the suite. os.Setenv reaches
	// the cell subprocesses via the runner's os.Environ() merge.
	if localOnly {
		os.Setenv(llm.LocalOnlyEnv, "1")
		fmt.Printf("[local-only] CORTEX_LOCAL_ONLY=1 (no remote/OpenRouter routing)\n")
	}

	fxs, err := codebase.LoadDir(dir)
	if err != nil {
		return fmt.Errorf("load fixtures from %s: %w", dir, err)
	}
	if project != "" {
		filtered := fxs[:0]
		for _, fx := range fxs {
			if fx.Project == project {
				filtered = append(filtered, fx)
			}
		}
		fxs = filtered
	}
	if len(only) > 0 {
		want := map[string]bool{}
		for _, o := range only {
			want[o] = true
		}
		filtered := fxs[:0]
		for _, fx := range fxs {
			if want[fx.ID] || want[fx.Eval] || want[fx.Project] {
				filtered = append(filtered, fx)
			}
		}
		fxs = filtered
	}
	if len(fxs) == 0 {
		return fmt.Errorf("no fixtures matched (dir=%s only=%v project=%s)", dir, only, project)
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	judgeOpts := codebase.JudgeOptions{}
	if judgeModel != "" {
		// Load the repo's .cortex/config.json so endpoint-routed model
		// ids (chatterbox aliases like "reasoner" / "coder") resolve to
		// their backend instead of falling through to OpenRouter.
		cfg, _ := loadEvalConfig()
		jp := intllm.BuildProvider(cfg, judgeModel)
		if jp == nil {
			return fmt.Errorf("judge provider unavailable for model %q (check OpenRouter key, Ollama, or endpoint config)", judgeModel)
		}
		judgeOpts.Provider = jp
		judgeOpts.Model = judgeModel
		fmt.Printf("[judge] enabled — model=%s\n", judgeModel)
	}

	ctx := context.Background()
	commit := codebase.CurrentGitSHA()

	rows := make([]codebase.BaselineRow, 0, len(fxs))
	total, passed, invalid, budgetZero := 0, 0, 0, 0
	for _, fx := range fxs {
		total++
		workdir := codebase.ResolveFixturePath(fixtureRoot, repoRoot, fx.Project)
		fmt.Printf("\n=== %s (%s/%s) — project=%s ===\n", fx.ID, fx.Group, fx.Eval, fx.Project)
		fmt.Printf("  workdir: %s\n", workdir)
		fmt.Printf("  prompt:  %s\n", oneLine(fx.Prompt))

		start := time.Now()
		res, m, bounds, err := codebase.Run(ctx, fx, codebase.RunOptions{
			CortexBinary: binary,
			Model:        model,
			Workdir:      workdir,
			Timeout:      time.Duration(timeoutSec) * time.Second,
			Judge:        judgeOpts,
		})
		elapsed := time.Since(start)
		if err != nil {
			fmt.Printf("  RUN ERROR: %v\n", err)
			continue
		}
		if res.CortexExitErr != nil {
			fmt.Printf("  cortex exit warn: %v\n", res.CortexExitErr)
		}
		if res.Judge != nil {
			fmt.Printf("  judge: pass=%v hallucination=%v reason=%s\n",
				res.Judge.Pass, res.Judge.HallucinationFlag, oneLine(res.Judge.Reason))
		}
		printMetricsAndBounds(m, bounds, elapsed)
		fixturePass := codebase.AllPass(bounds)
		// Quarantine harness failures (killed/timed-out/empty) as INVALID
		// instead of scoring them FAIL — a fleet stall isn't a quality
		// result and must not count against the pass rate.
		switch {
		case res.Invalid:
			invalid++
			fmt.Printf("  → INVALID (%s)\n", res.InvalidReason)
		case fixturePass:
			passed++
			fmt.Printf("  → PASS\n")
		default:
			fmt.Printf("  → FAIL\n")
		}
		// sense.estimate_scope is supposed to emit a nonzero budget on
		// every scoreable cell. A high budget=0 rate means the scope
		// estimator is degraded (e.g. a remote model returning garbage) —
		// a compromise signal the INVALID count alone misses, since these
		// score as FAIL, not INVALID.
		if !res.Invalid && m.BudgetTokens == 0 {
			budgetZero++
		}

		row := codebase.BaselineRow{
			FixtureID:    fx.ID,
			Group:        string(fx.Group),
			Eval:         fx.Eval,
			Project:      fx.Project,
			Language:     fx.Language,
			Timestamp:    time.Now().UTC().Format(time.RFC3339),
			GitCommitSHA: commit,
			Model:        model,
			WallTimeMs:   elapsed.Milliseconds(),
			Metrics:      m,
			Bounds:        bounds,
			Pass:          fixturePass && !res.Invalid,
			Invalid:       res.Invalid,
			InvalidReason: res.InvalidReason,
			AnswerSample:  oneLine(res.AnswerText),
		}
		if judgeOpts.Model != "" {
			row.JudgeModel = judgeOpts.Model
		}
		if res.Judge != nil {
			jp := res.Judge.Pass
			row.JudgePass = &jp
			row.JudgeReason = res.Judge.Reason
		}
		if res.CortexExitErr != nil {
			row.CortexExit = res.CortexExitErr.Error()
		}
		rows = append(rows, row)
	}

	// Pass rate is over SCOREABLE cells (total minus quarantined INVALID),
	// so a fleet stall can't deflate it. When INVALID dominates the run is
	// compromised — its pass rate is not a trustworthy result.
	scoreable := total - invalid
	if invalid > 0 {
		fmt.Printf("\nsummary: %d/%d passing (%d invalid, excluded)\n", passed, scoreable, invalid)
	} else {
		fmt.Printf("\nsummary: %d/%d passing\n", passed, total)
	}
	// Compromise detection — two independent signals that the run isn't a
	// trustworthy result:
	//   1. mass INVALID (killed/timed-out/empty) — overt harness failure.
	//   2. mass budget=0 among scoreable cells — sense.estimate_scope
	//      degraded (e.g. a remote model returning garbage). These score
	//      as FAIL, so the INVALID count alone misses them; without this
	//      check a degraded-estimator run reads as a real low score.
	invalidHigh := total > 0 && float64(invalid) > 0.15*float64(total)
	budgetZeroHigh := scoreable > 0 && float64(budgetZero) > 0.5*float64(scoreable)
	if invalidHigh {
		fmt.Printf("⚠ RUN COMPROMISED: %d/%d cells INVALID (killed/timed-out/empty — likely a fleet stall). Pass rate is NOT trustworthy; re-run before drawing conclusions.\n", invalid, total)
	}
	if budgetZeroHigh {
		fmt.Printf("⚠ RUN COMPROMISED: %d/%d scoreable cells have budget_tokens=0 (sense.estimate_scope degraded — often a remote/fleet issue). Pass rate is NOT trustworthy; check routing (try --local-only) and re-run.\n", budgetZero, scoreable)
	}

	if persistBaseline {
		path, err := codebase.WriteBaseline(repoRoot, commit, rows)
		if err != nil {
			fmt.Printf("baseline write error: %v\n", err)
		} else {
			fmt.Printf("baseline written: %s\n", path)
		}
	}

	if compareRef != "" {
		prev, prevPath, err := codebase.LoadBaseline(repoRoot, compareRef)
		if err != nil {
			fmt.Printf("compare: load baseline %s: %v\n", compareRef, err)
		} else if len(prev) == 0 {
			fmt.Printf("compare: no baseline found for ref %q\n", compareRef)
		} else {
			fmt.Printf("compare against: %s\n", prevPath)
			diffs := codebase.Compare(prev, rows)
			fmt.Print(codebase.FormatCompareReport(diffs))
			regressed := 0
			for _, d := range diffs {
				if d.Regressed {
					regressed++
				}
			}
			if regressed > 0 {
				return fmt.Errorf("codebase eval compare: %d fixture(s) regressed", regressed)
			}
		}
	}

	if passed < total {
		return fmt.Errorf("codebase eval: %d/%d fixtures failed", total-passed, total)
	}
	return nil
}

func printMetricsAndBounds(m codebase.Metrics, bounds []codebase.Bound, elapsed time.Duration) {
	fmt.Printf("  metrics: hop=%d read=%d shell=%d need_more=%d budget_tokens=%d  citation_rate=%.2f hedge=%d unverified=%d  (elapsed=%s)\n",
		m.HopCount, m.ReadCount, m.ShellCount, m.NeedMore, m.BudgetTokens,
		m.CitationRate, m.HedgeCount, m.UnverifiedTailCount, elapsed.Round(time.Millisecond))
	for _, b := range bounds {
		mark := "ok"
		if !b.Pass {
			mark = "FAIL"
		}
		fmt.Printf("    [%s] %-26s want=%s got=%s\n", mark, b.Name, b.Want, b.Got)
	}
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

