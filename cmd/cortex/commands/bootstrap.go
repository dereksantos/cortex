// Package commands — cortex bootstrap entry point.
//
// Scans the project and seeds dream.insight entries via the bootstrap
// DAG (sense.scan_project_boundaries → attend.fractal_sample →
// maintain.extract_{insight,overview} per chunk → journal). The
// controller loops until ≥80% effective LOC AND ≥80% of files have
// at least one insight emitted, OR the iteration budget runs out.
//
// Surface: both this standalone subcommand AND auto-invocation from
// the REPL on first run (see repl.go's shouldRunBootstrap path).
// Both paths hit the same controller, which makes the run
// re-runnable and testable in isolation.
package commands

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dereksantos/cortex/internal/bootstrap"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/cognition/dag/ops"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
)

func init() {
	Register(&BootstrapCommand{})
}

// BootstrapCommand exposes `cortex bootstrap`.
type BootstrapCommand struct{}

// Name returns the command name.
func (c *BootstrapCommand) Name() string { return "bootstrap" }

// Description returns the command description.
func (c *BootstrapCommand) Description() string {
	return "Scan the project and seed dream.insight entries via the bootstrap DAG"
}

// DescribeFlags surfaces bootstrap's flag set into tools.json.
func (c *BootstrapCommand) DescribeFlags(fs *flag.FlagSet) {
	fs.Bool("force", false, "Re-run even if bootstrap_state.json indicates completed")
	fs.Int("budget", 200, "Max iterations (each iteration = BatchSize chunks)")
	fs.Float64("target-coverage", 0.80, "Halt when both signals (effective LOC + file coverage) reach target")
	fs.Int("batch", 4, "Chunks per iteration")
	fs.Int("window-lines", bootstrap.DefaultWindowLines, "Chunk window size in lines")
	fs.Int("window-overlap", bootstrap.DefaultWindowOverlap, "Adjacent-chunk overlap in lines")
	fs.String("extract-op", bootstrap.ExtractOpAuto, "auto | extract_insight | extract_overview")
	fs.String("salt", "", "Optional RNG salt for variation across runs")
	fs.Bool("dry-run", false, "Sampler + analyzer only; skip LLM + journal writes")
	fs.String("provider", "ollama", "LLM provider: ollama | openrouter")
	fs.String("model", "", "Model id (provider-specific; falls back to provider default)")
}

// Execute runs the bootstrap subcommand.
func (c *BootstrapCommand) Execute(ctx *Context) error {
	args := ctx.Args
	force := false
	budget := 200
	target := 0.80
	batch := 4
	windowLines := bootstrap.DefaultWindowLines
	windowOverlap := bootstrap.DefaultWindowOverlap
	extractOp := bootstrap.ExtractOpAuto
	salt := ""
	dryRun := false
	providerName := "ollama"
	modelID := ""

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-h", "--help":
			printBootstrapHelp()
			return nil
		case "--force":
			force = true
		case "--dry-run":
			dryRun = true
		case "--budget":
			if i+1 < len(args) {
				v, err := strconv.Atoi(args[i+1])
				if err != nil {
					return fmt.Errorf("--budget: %w", err)
				}
				budget = v
				i++
			}
		case "--target-coverage":
			if i+1 < len(args) {
				v, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					return fmt.Errorf("--target-coverage: %w", err)
				}
				target = v
				i++
			}
		case "--batch":
			if i+1 < len(args) {
				v, err := strconv.Atoi(args[i+1])
				if err != nil {
					return fmt.Errorf("--batch: %w", err)
				}
				batch = v
				i++
			}
		case "--window-lines":
			if i+1 < len(args) {
				v, err := strconv.Atoi(args[i+1])
				if err != nil {
					return fmt.Errorf("--window-lines: %w", err)
				}
				windowLines = v
				i++
			}
		case "--window-overlap":
			if i+1 < len(args) {
				v, err := strconv.Atoi(args[i+1])
				if err != nil {
					return fmt.Errorf("--window-overlap: %w", err)
				}
				windowOverlap = v
				i++
			}
		case "--extract-op":
			if i+1 < len(args) {
				extractOp = strings.TrimSpace(args[i+1])
				i++
			}
		case "--salt":
			if i+1 < len(args) {
				salt = args[i+1]
				i++
			}
		case "--provider":
			if i+1 < len(args) {
				providerName = strings.TrimSpace(args[i+1])
				i++
			}
		case "--model", "-m":
			if i+1 < len(args) {
				modelID = args[i+1]
				i++
			}
		default:
			fmt.Fprintf(os.Stderr, "warning: unknown flag %q\n", arg)
		}
	}

	if !bootstrap.IsValidExtractOp(extractOp) {
		return fmt.Errorf("--extract-op: %q is not one of auto|extract_insight|extract_overview", extractOp)
	}

	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cwd: %w", err)
	}
	cortexDir := filepath.Join(projectRoot, ".cortex")

	// First-run check (unless --force).
	if !force {
		if run, reason := bootstrap.ShouldRunBootstrap(cortexDir); !run {
			fmt.Println("bootstrap already completed; use --force to re-run.")
			return nil
		} else if reason != "never_run" {
			fmt.Printf("bootstrap state present (%s); resuming.\n", reason)
		}
	}

	provider := buildBootstrapProvider(providerName, modelID)
	if provider == nil && !dryRun {
		fmt.Fprintf(os.Stderr, "warning: no LLM provider configured (provider=%s); falling back to mechanical extract\n", providerName)
	}

	insightFn := wrapInsightFn(provider)
	overviewFn := wrapOverviewFn(provider)

	cc := bootstrap.ControllerConfig{
		Config: bootstrap.Config{
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
			DryRun:         dryRun,
			Banner: func(line string) {
				fmt.Println("[bootstrap] " + line)
			},
		},
		ExtractInsightFn:  insightFn,
		ExtractOverviewFn: overviewFn,
	}
	controller, err := bootstrap.NewController(cc)
	if err != nil {
		return fmt.Errorf("controller: %w", err)
	}
	if err := controller.Run(context.Background()); err != nil {
		return fmt.Errorf("bootstrap run: %w", err)
	}

	st := controller.State()
	if st == nil {
		// pidlock skipped this invocation; banner already explained.
		return nil
	}
	out := controller.Boundaries()
	if out == nil {
		return nil
	}
	covEff := 0.0
	if out.EffTotalLines > 0 {
		covEff = float64(st.CoveredEffLines) / float64(out.EffTotalLines)
	}
	covFiles := 0.0
	if out.TotalFiles > 0 {
		covFiles = float64(st.CoveredFiles) / float64(out.TotalFiles)
	}
	fmt.Printf("\nBootstrap %s in %d iterations.\n", st.Halted, st.Iteration)
	fmt.Printf("  effective LOC coverage : %d / %d (%.1f%%)\n", st.CoveredEffLines, out.EffTotalLines, 100*covEff)
	fmt.Printf("  file coverage          : %d / %d (%.1f%%)\n", st.CoveredFiles, out.TotalFiles, 100*covFiles)
	fmt.Printf("  insights emitted       : %d\n", st.InsightsEmitted)
	if len(st.ExtractOpUsed) > 0 {
		fmt.Printf("  extract ops            :")
		for k, v := range st.ExtractOpUsed {
			fmt.Printf(" %s=%d", k, v)
		}
		fmt.Println()
	}
	fmt.Printf("  state file             : %s\n", bootstrap.StatePath(cortexDir))
	return nil
}

func printBootstrapHelp() {
	fmt.Println("Usage: cortex bootstrap [flags]")
	fmt.Println("\nScans the project and seeds dream.insight entries via the bootstrap DAG.")
	fmt.Println("Loops until both coverage signals (effective LOC + file coverage) hit target,")
	fmt.Println("or the iteration budget runs out.")
	fmt.Println("\nFlags:")
	fmt.Println("  --force                Re-run even if previously completed")
	fmt.Println("  --target-coverage f    Halt when both signals ≥ f (default 0.80)")
	fmt.Println("  --budget N             Max iterations (default 200)")
	fmt.Println("  --batch N              Chunks per iteration (default 4)")
	fmt.Println("  --window-lines N       Chunk window size in lines (default 400)")
	fmt.Println("  --window-overlap N     Adjacent-chunk overlap (default 40)")
	fmt.Println("  --extract-op X         auto | extract_insight | extract_overview")
	fmt.Println("  --salt S               RNG salt for run variation")
	fmt.Println("  --dry-run              Skip LLM + journal writes")
	fmt.Println("  --provider P           ollama | openrouter (default ollama)")
	fmt.Println("  --model M              Provider-specific model id")
	fmt.Println("  -h, --help             Show this help message")
	fmt.Println("\nCost projection (rough, default knobs, ~50K-LOC repo):")
	fmt.Println("  ~125 chunks at 400 lines/chunk; 80%% target ≈ 100 LLM calls.")
	fmt.Println("  Sequential ≈ 30 min @ 18s/call (Haiku-class). batch=4 cuts to ~7 min.")
	fmt.Println("  Ollama is free; OpenRouter Haiku is single-digit cents.")
}

// buildBootstrapProvider constructs an LLM provider for the bootstrap
// extract ops. Defaults to Ollama (local); --provider=openrouter uses
// the keychain key (managed by the existing secret package).
//
// Returns nil when the requested provider can't be built — the caller
// degrades to the mechanical fallback inside each extract op.
func buildBootstrapProvider(providerName, modelID string) llm.Provider {
	cfg := &config.Config{}
	switch strings.ToLower(providerName) {
	case "openrouter":
		// Lazy import secret to avoid pulling keychain into tests
		// that don't need it; here we rely on the same construction
		// path the REPL uses.
		client, _, err := llm.NewLLMClient(cfg,
			llm.WithBackend(llm.BackendOpenRouter),
			llm.WithModel(modelID),
		)
		if err != nil {
			return nil
		}
		return client
	default: // ollama
		c, _, err := llm.NewLLMClient(cfg, llm.WithBackend(llm.BackendOllama))
		if err != nil {
			return nil
		}
		if modelID != "" {
			if setter, ok := c.(interface{ SetModel(string) }); ok {
				setter.SetModel(modelID)
			}
		}
		return c
	}
}

// wrapInsightFn adapts maintain.extract_insight's NodeSpec.Handler to
// the controller's ExtractFunc shape. The conversion is one-to-many
// in principle (insight emits 0-2); here we forward every entry.
func wrapInsightFn(provider llm.Provider) bootstrap.ExtractFunc {
	spec := ops.ExtractInsightSpec(ops.ExtractInsightConfig{Provider: provider})
	return func(ctx context.Context, content, source, langHint, fileRoleHint string) ([]bootstrap.ExtractedInsight, bool, error) {
		in := map[string]any{
			"content": content,
			"source":  source,
		}
		// extract_insight does not take lang/role hints, ignore them.
		_ = langHint
		_ = fileRoleHint
		res, err := spec.Handler(ctx, in, dag.Budget{LatencyMS: 60000, Tokens: 1500, Depth: 5})
		if err != nil {
			return nil, false, err
		}
		fb, _ := res.Out["fallback"].(bool)
		insights, _ := res.Out["insights"].([]ops.Insight)
		out := make([]bootstrap.ExtractedInsight, 0, len(insights))
		for _, i := range insights {
			out = append(out, bootstrap.ExtractedInsight{
				Content:    i.Content,
				Category:   i.Category,
				Importance: i.Importance,
			})
		}
		return out, fb, nil
	}
}

// wrapOverviewFn adapts maintain.extract_overview's handler to the
// controller's ExtractFunc shape. The Overview struct collapses into
// a single ExtractedInsight whose Tags carry exports + dependencies
// and Category encodes the role.
func wrapOverviewFn(provider llm.Provider) bootstrap.ExtractFunc {
	spec := ops.ExtractOverviewSpec(ops.ExtractOverviewConfig{Provider: provider})
	return func(ctx context.Context, content, source, langHint, fileRoleHint string) ([]bootstrap.ExtractedInsight, bool, error) {
		in := map[string]any{
			"content":        content,
			"source":         source,
			"lang_hint":      langHint,
			"file_role_hint": fileRoleHint,
		}
		res, err := spec.Handler(ctx, in, dag.Budget{LatencyMS: 60000, Tokens: 1500, Depth: 5})
		if err != nil {
			return nil, false, err
		}
		fb, _ := res.Out["fallback"].(bool)
		ov, _ := res.Out["overview"].(ops.Overview)
		if strings.TrimSpace(ov.Summary) == "" {
			return nil, fb, nil
		}
		tags := make([]string, 0, len(ov.Exports)+len(ov.Dependencies))
		tags = append(tags, ov.Exports...)
		tags = append(tags, ov.Dependencies...)
		category := "overview"
		if ov.Role != "" {
			category = "overview:" + ov.Role
		}
		return []bootstrap.ExtractedInsight{{
			Content:    ov.Summary,
			Category:   category,
			Importance: ov.Importance,
			Tags:       tags,
		}}, fb, nil
	}
}
