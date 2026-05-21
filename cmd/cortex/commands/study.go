// Package commands — `cortex study DURATION`.
//
// Spends a wall-clock duration learning the project via the bootstrap
// DAG. The duration is the single user-facing knob; chunk size + chunk
// count are derived from the model's context window and observed
// per-call latency (probed once per model, cached for 7 days). Hard
// cap: when DURATION elapses, study halts immediately (in-flight call
// is context-canceled via context.WithDeadline).
//
// Every run is tagged with a unique run_id and a duration shorthand
// ("study-5m") so multiple studies stay comparable in the journal —
// the comparison workflow described in docs/cortex-study-plan.md.
//
// Engineering: this command reuses the same controller as
// `cortex bootstrap` (no parallel surface). The duration → controller
// knobs translation lives in internal/bootstrap.PlanStudy.
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

	"github.com/dereksantos/cortex/internal/bootstrap"
	"github.com/dereksantos/cortex/internal/journal"
	intllm "github.com/dereksantos/cortex/internal/llm"
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
	return "Spend N seconds/minutes/hours learning the project via the bootstrap DAG"
}

// DescribeFlags surfaces study's flag set into tools.json.
func (c *StudyCommand) DescribeFlags(fs *flag.FlagSet) {
	fs.String("model", "", "Model id (default cfg.OllamaModel)")
	fs.String("endpoint", "", "OpenAI-compat base URL transient override (bypasses model_routes)")
	fs.Bool("force", false, "Re-run even if previously completed")
	fs.String("extract-op", bootstrap.ExtractOpAuto, "auto | extract_insight | extract_overview")
	fs.Bool("dry-run", false, "Plan only — print derivation, don't call LLM")
	fs.Bool("verbose", false, "Print derivation trace + per-iteration banners")
	fs.Float64("fill", 0, "Target context-window fill fraction (advanced; default 0.5)")
}

// Execute runs the study subcommand. Positional arg: DURATION.
func (c *StudyCommand) Execute(ctx *Context) error {
	durationStr := ""
	modelID := ""
	endpoint := ""
	force := false
	extractOp := bootstrap.ExtractOpAuto
	dryRun := false
	verbose := false
	fill := 0.0

	args := ctx.Args
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-h", "--help":
			printStudyHelp()
			return nil
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
	if !bootstrap.IsValidExtractOp(extractOp) {
		return fmt.Errorf("--extract-op: %q is not one of auto|extract_insight|extract_overview", extractOp)
	}

	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cwd: %w", err)
	}
	cortexDir := filepath.Join(projectRoot, ".cortex")

	// Resolve provider (model id = routing key; --endpoint transient
	// override). Same wiring as `cortex bootstrap`.
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
	probe, err := bootstrap.Probe(probeCtx, provider, resolvedModel, endpoint, cortexDir, bootstrap.DefaultProbeTTL)
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
	plan := bootstrap.PlanStudy(d, probe.CtxWindowTokens, probe.LatencyMS, 0, fill)

	fmt.Println("[study] " + plan.Reasoning)

	if dryRun {
		fmt.Println("[study] --dry-run: plan complete; not invoking LLM.")
		return nil
	}

	// First-run check (unless --force). Study uses the same state file
	// as bootstrap.
	if !force {
		if run, reason := bootstrap.ShouldRunBootstrap(cortexDir); !run {
			fmt.Println("[study] bootstrap state present (completed); use --force to re-run.")
			return nil
		} else if reason != "never_run" && verbose {
			fmt.Printf("[study] state present (%s); resuming.\n", reason)
		}
	}

	insightFn := wrapInsightFn(provider)
	overviewFn := wrapOverviewFn(provider)

	bannerFn := func(line string) {
		if verbose {
			fmt.Println("[study] " + line)
		}
	}

	cc := bootstrap.ControllerConfig{
		Config: bootstrap.Config{
			ProjectRoot:    projectRoot,
			ContextDir:     cortexDir,
			Provider:       provider,
			TargetCoverage: plan.TargetCoverage,
			BudgetMax:      plan.MaxCalls,
			BatchSize:      plan.BatchSize,
			WindowLines:    plan.WindowLines,
			WindowOverlap:  plan.WindowOverlap,
			ExtractOp:      extractOp,
			Salt:           plan.RunID, // unique RNG variation per run
			Banner:         bannerFn,
			RunID:          plan.RunID,
			RunShorthand:   plan.Shorthand,
		},
		ExtractInsightFn:  insightFn,
		ExtractOverviewFn: overviewFn,
	}
	controller, err := bootstrap.NewController(cc)
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
		_ = bootstrap.SaveState(bootstrap.StatePath(cortexDir), st)
	}

	covEff := 0.0
	covFiles := 0.0
	if out != nil {
		if out.EffTotalLines > 0 {
			covEff = float64(st.CoveredEffLines) / float64(out.EffTotalLines)
		}
		if out.TotalFiles > 0 {
			covFiles = float64(st.CoveredFiles) / float64(out.TotalFiles)
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
	fmt.Printf("  state file             : %s\n", bootstrap.StatePath(cortexDir))
	return nil
}

// emitStudyClosingInsight appends a final dream.insight to the study
// journal summarizing the run. Kept out of the controller because the
// controller's writer closes inside Run() — we open a fresh writer
// post-run for this single entry.
func emitStudyClosingInsight(cortexDir string, plan bootstrap.StudyPlan, st *bootstrap.BootstrapState, out *bootstrap.BoundaryOutput, elapsed time.Duration) error {
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
		filePct = 100 * float64(st.CoveredFiles) / float64(totalFiles)
	}
	content := fmt.Sprintf(
		"Study halted: reason=%s, elapsed=%s, iterations=%d, insights=%d, eff_loc_covered=%.1f%%, file_coverage=%.1f%%, ctx=%d, latency=%dms",
		st.Halted, elapsed.Round(time.Second), st.Iteration, st.InsightsEmitted,
		covPct, filePct, plan.CtxWindowTokens, plan.LatencyMS,
	)

	tags := []string{"bootstrap", "meta", "study", plan.RunID, plan.Shorthand, "halt:" + st.Halted}
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
	fmt.Println("Usage: cortex study DURATION [flags]")
	fmt.Println("")
	fmt.Println("  DURATION: a Go time.Duration string — e.g. 30s, 5m, 1h, 2h30m.")
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
	fmt.Println("  --model M         Model id (default cfg.OllamaModel)")
	fmt.Println("  --endpoint URL    OpenAI-compat base URL (one-off; prefer model_routes)")
	fmt.Println("  --force           Re-run even if previously completed")
	fmt.Println("  --extract-op X    auto | extract_insight | extract_overview")
	fmt.Println("  --dry-run         Plan only — print the derivation, don't call the LLM")
	fmt.Println("  --verbose         Print each iteration's banner + probe details")
	fmt.Println("  --fill F          Target context-window fill fraction (default 0.5)")
	fmt.Println("  -h, --help        Show this help message")
}
