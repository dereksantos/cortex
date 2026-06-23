package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	intllm "github.com/dereksantos/cortex/internal/llm"
	"github.com/dereksantos/cortex/internal/projectindex"
	"github.com/dereksantos/cortex/internal/study"
)

// cliStudyMapBudget caps the structural map so it never dominates the
// study digest on large trees. Mirrors the loop harness's studyMapBudget.
const cliStudyMapBudget = 4000

// fileStudyOpts bundles the knobs for `cortex study FILE|DIR` (the
// LLM-backed study → curate → deepen loop over a file or directory).
type fileStudyOpts struct {
	path      string
	density   study.Density
	window    int
	focus     *study.Focus
	goal      string
	maxPasses int
	modelID   string
	endpoint  string
}

// runFileStudy runs the deepening loop over one file or directory and
// prints each pass's digest + citations + the curator's decision, then
// a summary. It
// reuses the command's provider/probe wiring; inference and the curator
// are both backed by the resolved provider.
func runFileStudy(c *Context, opts fileStudyOpts, w io.Writer) error {
	abs, err := filepath.Abs(opts.path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", opts.path, err)
	}

	provider := intllm.BuildProvider(c.Config, opts.modelID, intllm.WithEndpointOverride(opts.endpoint))
	if provider == nil {
		return fmt.Errorf("study %s: no LLM provider configured (set --model/--endpoint or .cortex/config.json); for a no-LLM sample use --sample-only", opts.path)
	}

	resolvedModel := opts.modelID
	if resolvedModel == "" && c.Config != nil {
		resolvedModel = c.Config.DefaultGenerationModel()
	}

	cwd, _ := os.Getwd()
	cortexDir := filepath.Join(cwd, ".cortex")

	window := opts.window
	if window <= 0 {
		pctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		probe, perr := study.Probe(pctx, provider, resolvedModel, opts.endpoint, cortexDir, study.DefaultProbeTTL)
		cancel()
		if perr != nil {
			fmt.Fprintf(os.Stderr, "warning: probe: %v\n", perr)
		}
		window = probe.CtxWindowTokens
	}

	req := study.StudyRequest{
		Path:    abs,
		RelPath: opts.path,
		Density: opts.density,
		Window:  window,
		Focus:   opts.focus,
		Goal:    opts.goal,
		Infer:   study.ProviderInfer(provider),
	}
	// Build the structural map for directories so the director and the
	// inference/curator prompts see the full terrain. The loop harness
	// does the same; the CLI needs it here for the director to aim from.
	if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
		if ix, err := projectindex.Build(abs); err == nil {
			m := ix.Render()
			if len(m) > cliStudyMapBudget {
				m = m[:cliStudyMapBudget] + "\n… (map truncated)"
			}
			req.ProjectMap = m
		}
	}
	// Pre-pass direction: the first pass samples where the goal points
	// (via the project map) instead of a goal-blind mechanical draw.
	// Degrades to nil (→ mechanical sampling) with no goal or no map.
	req.Director = study.ModelDirector{Provider: provider, ProjectMap: req.ProjectMap}
	res, err := study.StudyLoop(context.Background(), req, study.ModelCurator{Provider: provider}, opts.maxPasses)
	if err != nil {
		return err
	}

	for i, p := range res.Passes {
		fmt.Fprintf(w, "── pass %d ──\n", i+1)
		if p.Response.Mode == "read" {
			fmt.Fprintf(w, "read whole target (%d bytes; it fits the window)\n\n", len(p.Response.ReadContent))
			continue
		}
		fmt.Fprintf(w, "coverage %.1f%%   sampled %d chunks   exhausted=%t\n",
			100*p.Response.Coverage.Pct, len(p.Response.Sampled), p.Response.Exhausted)
		if p.Response.Digest != "" {
			fmt.Fprintf(w, "digest: %s\n", p.Response.Digest)
		}
		for _, cit := range p.Response.Citations {
			fmt.Fprintf(w, "  cite %s:%d-%d  %s\n", cit.RelPath, cit.LineStart, cit.LineEnd, cit.Claim)
		}
		for _, l := range p.Response.Leads {
			fmt.Fprintf(w, "  lead %s ~line %d  %s\n", l.RelPath, l.NearLine, l.Why)
		}
		if p.Decision.Kind != "" {
			fmt.Fprintf(w, "curator → %s\n", decisionStr(p.Decision))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "stopped: %s   cumulative coverage: %.1f%%   passes: %d\n",
		res.Stopped, 100*res.CoveragePct, len(res.Passes))
	return nil
}

func decisionStr(d study.Decision) string {
	switch d.Kind {
	case study.DecisionTarget:
		switch {
		case d.Focus != nil && d.Focus.Path != "":
			return fmt.Sprintf("TARGET %s lines %d-%d", d.Focus.Path, d.Focus.Lines[0], d.Focus.Lines[1])
		case d.Focus != nil:
			return fmt.Sprintf("TARGET lines %d-%d", d.Focus.Lines[0], d.Focus.Lines[1])
		}
		return "TARGET"
	case study.DecisionDensify:
		return fmt.Sprintf("DENSIFY (density=%v)", d.Density)
	default:
		return d.Kind
	}
}
