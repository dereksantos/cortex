// Package commands — `cortex study DURATION`.
//
// Spends a wall-clock duration learning the project via the study DAG.
// DURATION is the primary user-facing knob; chunk size + chunk count
// are derived from the model's context window and observed per-call
// latency (probed once per model, cached for 7 days). Hard cap: when
// DURATION elapses, study halts immediately (in-flight call is
// context-canceled via context.WithDeadline).
//
// Power-user overrides (--target-coverage, --budget, --window-lines,
// --window-overlap, --batch, --salt) win over the duration-derived
// plan — this replaces the older `cortex study` knob surface.
//
// Every run is tagged with a unique run_id and a duration shorthand
// ("study-5m") so multiple studies stay comparable in the journal —
// see docs/cortex-study-plan.md.
//
// Engineering: the duration → controller-knobs translation lives in
// internal/study.MakePlan. Auto-accumulation is per-file (see
// study.Controller); the gate is drift, not a one-shot completion bit.
package commands

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	intllm "github.com/dereksantos/cortex/internal/llm"
	"github.com/dereksantos/cortex/internal/study"
)

func init() {
	Register(&StudyCommand{})
}

// StudyCommand exposes `cortex study DURATION`.
type StudyCommand struct{}

// Name returns the command name.
func (c *StudyCommand) Name() string { return "study" }

// Description returns the command description.
func (c *StudyCommand) Description() string {
	return "Spend N seconds/minutes/hours learning the project via the study DAG"
}

// DescribeFlags surfaces study's flag set into tools.json. Knob
// overrides default to sentinel values (negative / empty) so the
// Execute path can tell "user didn't pass this" from "user passed zero."
func (c *StudyCommand) DescribeFlags(fs *flag.FlagSet) {
	fs.String("model", "", "Model id (default cfg.OllamaModel)")
	fs.String("endpoint", "", "OpenAI-compat base URL transient override (bypasses model_routes)")
	fs.Bool("force", false, "Wipe per-file coverage before running (re-extract everything)")
	fs.String("extract-op", study.ExtractOpAuto, "auto | extract_insight | extract_overview")
	fs.Bool("dry-run", false, "Plan only — print derivation, don't call LLM")
	fs.Bool("verbose", false, "Print derivation trace + per-iteration banners")
	fs.Float64("fill", 0, "Target context-window fill fraction (advanced; default 0.4)")
	fs.Float64("target-coverage", -1, "Override the derived halt-target (eff_loc AND files ≥ this)")
	fs.Int("budget", -1, "Override the derived iteration cap")
	fs.Int("batch", -1, "Override the derived chunks-per-iteration count")
	fs.Int("window-lines", -1, "Override the derived chunk window size in lines")
	fs.Int("window-overlap", -1, "Override the derived adjacent-chunk overlap in lines")
	fs.String("salt", "", "Override the RNG salt (default: run_id)")
	// Mechanical study_file checkpoint: `cortex study FILE|DIR --sample-only`.
	fs.Bool("sample-only", false, "Mechanically sample a FILE or DIR (sampler only, no LLM) and print the chunk table")
	fs.String("density", "", "Sample density for --sample-only: sparse | normal | dense | <int k>")
	fs.Int("window", 0, "Consuming-model context window in tokens for --sample-only (default: conservative)")
	fs.String("focus-lines", "", "Bias the sample toward a START,END line range")
	fs.String("focus-path", "", "Bias the sample toward a file or subdirectory (DIR studies)")
	fs.String("goal", "", "Task hint passed to inference / the curator when studying a FILE or DIR")
	fs.Int("max-passes", 4, "Max deepening passes when studying a FILE or DIR")
}

// Execute runs the study subcommand. Positional arg: DURATION.
func (c *StudyCommand) Execute(ctx *Context) error {
	durationStr := ""
	modelID := ""
	endpoint := ""
	force := false
	extractOp := study.ExtractOpAuto
	dryRun := false
	verbose := false
	fill := 0.0
	// Power-user overrides. -1 / "" = "not set; use plan default."
	overrideTarget := -1.0
	overrideBudget := -1
	overrideBatch := -1
	overrideWindow := -1
	overrideOverlap := -1
	overrideSalt := ""
	// Mechanical --sample-only checkpoint knobs.
	sampleOnly := false
	densityStr := ""
	sampleWindow := 0
	focusLinesStr := ""
	focusPathStr := ""
	goalStr := ""
	maxPasses := 4

	args := ctx.Args
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-h", "--help":
			printStudyHelp()
			return nil
		case "--sample-only":
			sampleOnly = true
		case "--density":
			if i+1 < len(args) {
				densityStr = strings.TrimSpace(args[i+1])
				i++
			}
		case "--window":
			if i+1 < len(args) {
				v, err := strconv.Atoi(args[i+1])
				if err != nil {
					return fmt.Errorf("--window: %w", err)
				}
				sampleWindow = v
				i++
			}
		case "--focus-lines":
			if i+1 < len(args) {
				focusLinesStr = strings.TrimSpace(args[i+1])
				i++
			}
		case "--focus-path":
			if i+1 < len(args) {
				focusPathStr = strings.TrimSpace(args[i+1])
				i++
			}
		case "--goal":
			if i+1 < len(args) {
				goalStr = strings.TrimSpace(args[i+1])
				i++
			}
		case "--max-passes":
			if i+1 < len(args) {
				v, err := strconv.Atoi(args[i+1])
				if err != nil {
					return fmt.Errorf("--max-passes: %w", err)
				}
				maxPasses = v
				i++
			}
		case "--force":
			force = true
		case "--dry-run":
			dryRun = true
		case "--verbose":
			verbose = true
		case "--model", "-m":
			if i+1 < len(args) {
				modelID = args[i+1]
				i++
			}
		case "--endpoint":
			if i+1 < len(args) {
				endpoint = strings.TrimSpace(args[i+1])
				i++
			}
		case "--extract-op":
			if i+1 < len(args) {
				extractOp = strings.TrimSpace(args[i+1])
				i++
			}
		case "--fill":
			if i+1 < len(args) {
				v, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					return fmt.Errorf("--fill: %w", err)
				}
				fill = v
				i++
			}
		case "--target-coverage":
			if i+1 < len(args) {
				v, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					return fmt.Errorf("--target-coverage: %w", err)
				}
				overrideTarget = v
				i++
			}
		case "--budget":
			if i+1 < len(args) {
				v, err := strconv.Atoi(args[i+1])
				if err != nil {
					return fmt.Errorf("--budget: %w", err)
				}
				overrideBudget = v
				i++
			}
		case "--batch":
			if i+1 < len(args) {
				v, err := strconv.Atoi(args[i+1])
				if err != nil {
					return fmt.Errorf("--batch: %w", err)
				}
				overrideBatch = v
				i++
			}
		case "--window-lines":
			if i+1 < len(args) {
				v, err := strconv.Atoi(args[i+1])
				if err != nil {
					return fmt.Errorf("--window-lines: %w", err)
				}
				overrideWindow = v
				i++
			}
		case "--window-overlap":
			if i+1 < len(args) {
				v, err := strconv.Atoi(args[i+1])
				if err != nil {
					return fmt.Errorf("--window-overlap: %w", err)
				}
				overrideOverlap = v
				i++
			}
		case "--salt":
			if i+1 < len(args) {
				overrideSalt = args[i+1]
				i++
			}
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "warning: unknown flag %q\n", arg)
				continue
			}
			if durationStr == "" {
				durationStr = arg
			} else {
				fmt.Fprintf(os.Stderr, "warning: ignoring extra positional %q\n", arg)
			}
		}
	}

	// Mechanical checkpoint: `cortex study TARGET --sample-only` routes
	// to the no-LLM sampler (byte grid for a file, universal analyzer
	// for a dir) instead of the duration planner. The positional here is
	// a FILE or DIR path, not a DURATION.
	if sampleOnly {
		if durationStr == "" {
			return fmt.Errorf("study --sample-only: FILE argument required")
		}
		focus, err := parseFocus(focusLinesStr, focusPathStr)
		if err != nil {
			return err
		}
		var density study.Density
		if densityStr != "" {
			density = densityStr
		}
		return runSampleOnly(durationStr, density, sampleWindow, focus, os.Stdout)
	}

	// Target-study mode: when the positional is an existing FILE or DIR
	// (not a duration), run the LLM-backed study → curate → deepen loop
	// over it. A duration like "5m" isn't a path and falls through to
	// the planner.
	if durationStr != "" {
		if _, statErr := os.Stat(durationStr); statErr == nil {
			focus, ferr := parseFocus(focusLinesStr, focusPathStr)
			if ferr != nil {
				return ferr
			}
			var density study.Density
			if densityStr != "" {
				density = densityStr
			}
			return runFileStudy(ctx, fileStudyOpts{
				path:      durationStr,
				density:   density,
				window:    sampleWindow,
				focus:     focus,
				goal:      goalStr,
				maxPasses: maxPasses,
				modelID:   modelID,
				endpoint:  endpoint,
			}, os.Stdout)
		}
	}

	if durationStr == "" {
		printStudyHelp()
		return fmt.Errorf("study: DURATION argument required (e.g. 30s, 5m, 1h)")
	}
	d, err := time.ParseDuration(durationStr)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", durationStr, err)
	}
	if d <= 0 {
		return fmt.Errorf("duration must be > 0, got %s", d)
	}
	if !study.IsValidExtractOp(extractOp) {
		return fmt.Errorf("--extract-op: %q is not one of auto|extract_insight|extract_overview", extractOp)
	}

	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cwd: %w", err)
	}
	cortexDir := filepath.Join(projectRoot, ".cortex")

	// Resolve provider (model id = routing key; --endpoint transient
	// override). Same wiring as `cortex study`.
	provider := intllm.BuildProvider(ctx.Config, modelID, intllm.WithEndpointOverride(endpoint))
	if provider == nil && !dryRun {
		fmt.Fprintln(os.Stderr, "warning: no LLM provider configured; falling back to mechanical extract")
	}

	resolvedModel := modelID
	if resolvedModel == "" && ctx.Config != nil {
		resolvedModel = ctx.Config.DefaultGenerationModel()
	}

	// Probe + cache. A 7-day TTL means the second study run within a
	// week pays zero startup cost.
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 45*time.Second)
	probe, err := study.Probe(probeCtx, provider, resolvedModel, endpoint, cortexDir, study.DefaultProbeTTL)
	probeCancel()
	if err != nil {
		// Probe cache write failures are non-fatal — the in-memory
		// probe still drives the planner.
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	if verbose {
		fmt.Printf("[study] probe: model=%s endpoint=%s ctx=%d tokens latency=%dms source=%s\n",
			probe.ModelID, probe.Endpoint, probe.CtxWindowTokens, probe.LatencyMS, probe.Source)
	}

	// We don't know project_eff_loc until the analyzer runs; pass 0 and
	// let the planner fall back to the 0.80 default. The controller's
	// BudgetMax = plan.MaxCalls is the operative cap; ctx.WithDeadline
	// enforces the wall-clock hard stop.
	plan := study.MakePlan(d, probe.CtxWindowTokens, probe.LatencyMS, 0, fill)

	fmt.Println("[study] " + plan.Reasoning)

	if dryRun {
		fmt.Println("[study] --dry-run: plan complete; not invoking LLM.")
		return nil
	}

	// With auto-accumulation, study is always runnable: the controller
	// detects drift on its own (per-file content hashes against
	// CoveredFiles) and short-circuits with reason="no_drift" if there
	// is nothing new to study. --force is the escape hatch: wipe the
	// covered set entirely so every file is re-extracted from scratch.
	if force {
		if err := study.WipeCoverage(cortexDir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: --force wipe failed: %v\n", err)
		} else if verbose {
			fmt.Println("[study] --force: cleared per-file coverage; re-extracting from scratch.")
		}
	} else if verbose {
		sum := study.LoadDeficitSummary(cortexDir)
		if sum.HasState {
			fmt.Printf("[study] resuming: last coverage eff_loc=%.0f%% files=%.0f%% (drift checked on scan)\n",
				100*sum.LastCoveredEff, 100*sum.LastCoveredFile)
		} else {
			fmt.Println("[study] first run: no prior state.")
		}
	}

	insightFn := wrapInsightFn(provider)
	overviewFn := wrapOverviewFn(provider)

	bannerFn := func(line string) {
		if verbose {
			fmt.Println("[study] " + line)
		}
	}

	// Apply power-user overrides on top of the plan. Any explicit knob
	// the user passed (--target-coverage / --budget / --batch /
	// --window-lines / --window-overlap / --salt) wins over the
	// duration-derived value.
	target := plan.TargetCoverage
	if overrideTarget >= 0 {
		target = overrideTarget
	}
	budget := plan.MaxCalls
	if overrideBudget >= 0 {
		budget = overrideBudget
	}
	batch := plan.BatchSize
	if overrideBatch >= 0 {
		batch = overrideBatch
	}
	windowLines := plan.WindowLines
	if overrideWindow >= 0 {
		windowLines = overrideWindow
	}
	windowOverlap := plan.WindowOverlap
	if overrideOverlap >= 0 {
		windowOverlap = overrideOverlap
	}
	salt := plan.RunID
	if overrideSalt != "" {
		salt = overrideSalt
	}

	cc := study.ControllerConfig{
		Config: study.Config{
			ProjectRoot:    projectRoot,
			ContextDir:     cortexDir,
			Provider:       provider,
			TargetCoverage: target,
			BudgetMax:      budget,
			BatchSize:      batch,
			WindowLines:    windowLines,
			WindowOverlap:  windowOverlap,
			ExtractOp:      extractOp,
			Salt:           salt,
			Banner:         bannerFn,
			RunID:          plan.RunID,
			RunShorthand:   plan.Shorthand,
		},
		ExtractInsightFn:  insightFn,
		ExtractOverviewFn: overviewFn,
	}
	controller, err := study.NewController(cc)
	if err != nil {
		return fmt.Errorf("controller: %w", err)
	}

	// Hard cap. context.WithDeadline fires at exactly Duration; the
	// controller's per-iteration ctx.Err() check halts the loop, and
	// the OpenAI-compat client honors ctx through http.Request so any
	// in-flight call cancels mid-flight.
	runCtx, runCancel := context.WithDeadline(context.Background(), time.Now().Add(plan.Duration))
	defer runCancel()

	start := time.Now()
	if err := controller.Run(runCtx); err != nil {
		return fmt.Errorf("study run: %w", err)
	}
	elapsed := time.Since(start)

	st := controller.State()
	if st == nil {
		// pidlock skipped this invocation.
		return nil
	}
	out := controller.Boundaries()

	// Relabel a deadline-driven halt so journal readers can grep by
	// reason. "canceled" alone loses the "this was wall-clock budget,
	// not user Ctrl-C" distinction.
	if st.Halted == "canceled" {
		st.Halted = "study_duration_elapsed"
		_ = study.SaveState(study.StatePath(cortexDir), st)
	}

	covEff := 0.0
	covFiles := 0.0
	if out != nil {
		if out.EffTotalLines > 0 {
			covEff = float64(st.CoveredEffLines) / float64(out.EffTotalLines)
		}
		if out.TotalFiles > 0 {
			covFiles = float64(st.CoveredFileN) / float64(out.TotalFiles)
		}
	}

	// Closing meta-insight — gives `cortex search "study:done"` a
	// one-liner for finding a run's outcome after the fact.
	if err := emitStudyClosingInsight(cortexDir, plan, st, out, elapsed); err != nil {
		fmt.Fprintf(os.Stderr, "warning: closing insight failed: %v\n", err)
	}

	fmt.Printf("\nStudy %s in %s (%d iterations, %d insights).\n",
		st.Halted, elapsed.Round(time.Second), st.Iteration, st.InsightsEmitted)
	fmt.Printf("  effective LOC coverage : %.1f%%\n", 100*covEff)
	fmt.Printf("  file coverage          : %.1f%%\n", 100*covFiles)
	fmt.Printf("  run_id                 : %s\n", plan.RunID)
	fmt.Printf("  shorthand              : %s\n", plan.Shorthand)
	if probe.Source == "cached" {
		fmt.Printf("  probe                  : cached (ctx=%d, latency=%dms)\n", probe.CtxWindowTokens, probe.LatencyMS)
	} else {
		fmt.Printf("  probe                  : %s (ctx=%d, latency=%dms)\n", probe.Source, probe.CtxWindowTokens, probe.LatencyMS)
	}
	fmt.Printf("  state file             : %s\n", study.StatePath(cortexDir))
	return nil
}

// emitStudyClosingInsight appends a final dream.insight to the study
// journal summarizing the run. Kept out of the controller because the
// controller's writer closes inside Run() — we open a fresh writer
// post-run for this single entry.
func emitStudyClosingInsight(cortexDir string, plan study.Plan, st *study.State, out *study.BoundaryOutput, elapsed time.Duration) error {
	dreamDir := filepath.Join(cortexDir, "journal", "dream")
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: dreamDir,
		Fsync:    journal.FsyncPerBatch,
	})
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	defer w.Close()

	totalEff := 0
	totalFiles := 0
	if out != nil {
		totalEff = out.EffTotalLines
		totalFiles = out.TotalFiles
	}
	covPct := 0.0
	if totalEff > 0 {
		covPct = 100 * float64(st.CoveredEffLines) / float64(totalEff)
	}
	filePct := 0.0
	if totalFiles > 0 {
		filePct = 100 * float64(st.CoveredFileN) / float64(totalFiles)
	}
	content := fmt.Sprintf(
		"Study halted: reason=%s, elapsed=%s, iterations=%d, insights=%d, eff_loc_covered=%.1f%%, file_coverage=%.1f%%, ctx=%d, latency=%dms",
		st.Halted, elapsed.Round(time.Second), st.Iteration, st.InsightsEmitted,
		covPct, filePct, plan.CtxWindowTokens, plan.LatencyMS,
	)

	tags := []string{"study", "meta", plan.RunID, plan.Shorthand, "halt:" + st.Halted}
	insightID := "study:done:" + plan.RunID
	payload := journal.DreamInsightPayload{
		InsightID:    insightID,
		Category:     "pattern",
		Content:      content,
		Importance:   4,
		Tags:         tags,
		SourceItemID: insightID,
		SourceName:   "study",
	}
	entry, err := journal.NewDreamInsightEntry(payload)
	if err != nil {
		return fmt.Errorf("build entry: %w", err)
	}
	if _, err := w.Append(entry); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	return w.Flush()
}

func printStudyHelp() {
	fmt.Println("Usage: cortex study DURATION|FILE|DIR [flags]")
	fmt.Println("")
	fmt.Println("  DURATION: a Go time.Duration string — e.g. 30s, 5m, 1h, 2h30m.")
	fmt.Println("  FILE|DIR: an existing path runs the size-adaptive study → curate →")
	fmt.Println("            deepen loop over that file or directory instead (use")
	fmt.Println("            --goal, --max-passes, --density; --sample-only for no-LLM).")
	fmt.Println("")
	fmt.Println("Spends DURATION of wall-clock budget extracting overview insights about")
	fmt.Println("the project. Chunk size and chunk count are derived from your model's")
	fmt.Println("context window and observed per-call latency. Hard cap: when DURATION")
	fmt.Println("elapses, study halts immediately (in-flight call is context-canceled).")
	fmt.Println("")
	fmt.Println("Each run is tagged with a unique run_id so multiple studies are")
	fmt.Println("comparable. To compare understanding gained:")
	fmt.Println("")
	fmt.Println("  cortex study 1m  --model X")
	fmt.Println("  cortex study 5m  --model X --force")
	fmt.Println("  cortex study 30m --model X --force")
	fmt.Println("")
	fmt.Println("Flags:")
	fmt.Println("  --model M             Model id (default cfg.OllamaModel)")
	fmt.Println("  --endpoint URL        OpenAI-compat base URL (one-off; prefer model_routes)")
	fmt.Println("  --force               Wipe per-file coverage before running (re-extract everything)")
	fmt.Println("  --extract-op X        auto | extract_insight | extract_overview")
	fmt.Println("  --dry-run             Plan only — print the derivation, don't call the LLM")
	fmt.Println("  --verbose             Print each iteration's banner + probe details")
	fmt.Println("  --fill F              Target context-window fill fraction (default 0.4)")
	fmt.Println("")
	fmt.Println("Power-user overrides (win over the duration-derived plan):")
	fmt.Println("  --target-coverage F   Halt when both signals ≥ F")
	fmt.Println("  --budget N            Max iterations")
	fmt.Println("  --batch N             Chunks per iteration")
	fmt.Println("  --window-lines N      Chunk window size in lines")
	fmt.Println("  --window-overlap N    Adjacent-chunk overlap in lines")
	fmt.Println("  --salt S              RNG salt for variation across runs")
	fmt.Println("  -h, --help            Show this help message")
}
