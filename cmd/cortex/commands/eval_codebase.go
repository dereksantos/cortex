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
	total, passed := 0, 0
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
		if fixturePass {
			passed++
			fmt.Printf("  → PASS\n")
		} else {
			fmt.Printf("  → FAIL\n")
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
			Bounds:       bounds,
			Pass:         fixturePass,
			AnswerSample: oneLine(res.AnswerText),
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

	fmt.Printf("\nsummary: %d/%d passing\n", passed, total)

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

