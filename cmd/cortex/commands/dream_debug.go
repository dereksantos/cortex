package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/cognition/sources"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/llm"
)

func init() {
	Register(&DreamDebugCommand{})
}

// DreamDebugCommand runs Dream cycles synchronously and prints the
// items they sample, with full fractal metadata, so the operator can
// see what regions are emitted and how follow-ups propagate.
type DreamDebugCommand struct{}

// Name returns the command name.
func (c *DreamDebugCommand) Name() string { return "dream-debug" }

// Description returns the command description.
func (c *DreamDebugCommand) Description() string {
	return "Run Dream cycles synchronously and dump regions + fractal metadata"
}

// Execute runs two Dream cycles back-to-back and reports per-item
// metadata so the user can verify region sampling and fractal recursion.
func (c *DreamDebugCommand) Execute(ctx *Context) error {
	cfg := ctx.Config
	store := ctx.Storage
	if cfg == nil || store == nil {
		return fmt.Errorf("project not initialized; run `cortex init` first")
	}

	// Build LLM provider via the unified surface (OpenRouter primary,
	// Anthropic fallback). Graceful: nil is allowed — analysis will
	// short-circuit and we still see source emissions. Ollama remains
	// a separate local fallback below.
	var llmProvider llm.Provider
	if p, _, err := llm.NewLLMClient(cfg); err == nil {
		llmProvider = p
	}
	ollama := llm.NewOllamaClient(cfg)
	if llmProvider == nil && ollama.IsAvailable() {
		llmProvider = ollama
	}

	// Build embedder (Reflex/Reflect don't run in this command, but
	// Cortex.New requires the constructor to succeed).
	hugotEmbedder := llm.NewHugotEmbedder()
	embedder := llm.NewFallbackEmbedder(ollama, hugotEmbedder)

	cortex, err := intcognition.New(store, llmProvider, embedder, cfg)
	if err != nil {
		return fmt.Errorf("init cortex: %w", err)
	}

	observer := sources.NewObserver(cfg.ContextDir)
	projSrc := sources.NewProjectSource(cfg.ProjectRoot)
	projSrc.SetObserver(observer)
	cortex.RegisterSource(projSrc)
	cortex.RegisterSource(sources.NewCortexSource(store))
	gitSrc := sources.NewGitSource(cfg.ProjectRoot)
	gitSrc.SetObserver(observer)
	cortex.RegisterSource(gitSrc)
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		claudeSrc := sources.NewClaudeHistorySource(filepath.Join(homeDir, ".claude", "projects"))
		claudeSrc.SetObserver(observer)
		cortex.RegisterSource(claudeSrc)
	}

	fmt.Println("== Dream debug ==")
	fmt.Printf("project: %s\n", cfg.ProjectRoot)
	fmt.Printf("llm: %s\n", llmProviderLabel(llmProvider))
	fmt.Println()

	// Stream insights so we can see them as they happen.
	insights := make(chan cognition.Result, 32)
	go func() {
		for ins := range cortex.Insights() {
			insights <- ins
		}
	}()

	// Sample directly to show what ProjectSource is emitting now.
	fmt.Println("-- Direct ProjectSource sample (12 items) --")
	ps := sources.NewProjectSource(cfg.ProjectRoot)
	directItems, err := ps.Sample(context.Background(), 12)
	if err != nil {
		return fmt.Errorf("project sample: %w", err)
	}
	for _, item := range directItems {
		printItem(item)
	}
	fmt.Println()

	// Force two MaybeDream cycles back-to-back, bypassing the idle gates.
	for cycle := 1; cycle <= 2; cycle++ {
		cortex.ForceIdle()
		cortex.ResetForTesting()

		fmt.Printf("-- Cycle %d: MaybeDream() --\n", cycle)
		start := time.Now()
		res, derr := cortex.MaybeDream(context.Background())
		dur := time.Since(start)
		if derr != nil {
			fmt.Printf("  error: %v\n", derr)
			continue
		}
		fmt.Printf("  status=%v ops=%d insights=%d duration=%v\n",
			res.Status, res.Operations, res.Insights, dur.Truncate(time.Millisecond))
		fmt.Printf("  sources covered: %v\n", res.SourcesCovered)
		drain := drainPending(insights, 200*time.Millisecond)
		if len(drain) > 0 {
			fmt.Printf("  insights this cycle: %d\n", len(drain))
			for _, ins := range drain {
				printInsight(ins)
			}
		}
		fmt.Println()
	}

	return nil
}

func llmProviderLabel(p llm.Provider) string {
	if p == nil {
		return "(none — analysis disabled)"
	}
	if !p.IsAvailable() {
		return p.Name() + " (not available)"
	}
	return p.Name()
}

func printItem(item cognition.DreamItem) {
	fmt.Printf("  id=%s\n", item.ID)
	fmt.Printf("    source=%s path=%s\n", item.Source, item.Path)
	if off, ok := item.Metadata["region_offset"]; ok {
		fmt.Printf("    region offset=%v len=%v size=%v\n",
			off, item.Metadata["region_len"], item.Metadata["file_size"])
	}
	if parent, ok := item.Metadata["parent_item_id"].(string); ok && parent != "" {
		fmt.Printf("    parent=%s depth=%v\n", parent, item.Metadata["fractal_depth"])
	}
	if churn, ok := item.Metadata["git_churn"]; ok {
		fmt.Printf("    git_churn=%v\n", churn)
	}
}

func printInsight(ins cognition.Result) {
	fmt.Printf("    [%s score=%.2f] %s\n", ins.Category, ins.Score, ins.Content)
	if parent, ok := ins.Metadata["parent_item_id"].(string); ok && parent != "" {
		fmt.Printf("      ↳ from parent %s (depth=%v)\n", parent, ins.Metadata["fractal_depth"])
	}
}

// drainPending pulls everything currently buffered, waiting at most
// `wait` for late arrivals.
func drainPending(ch <-chan cognition.Result, wait time.Duration) []cognition.Result {
	var out []cognition.Result
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		select {
		case ins := <-ch:
			out = append(out, ins)
		case <-deadline.C:
			return out
		}
	}
}
