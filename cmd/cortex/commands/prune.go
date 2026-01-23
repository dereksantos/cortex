// Package commands provides CLI command implementations.
package commands

import (
	"context"
	"fmt"
	"time"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
)

// PruneCommand implements the prune functionality.
type PruneCommand struct{}

func init() {
	Register(&PruneCommand{})
}

// Name returns the command name.
func (c *PruneCommand) Name() string { return "prune" }

// Description returns the command description.
func (c *PruneCommand) Description() string { return "Check/manage context size relative to project" }

// Execute runs the prune command.
func (c *PruneCommand) Execute(ctx *Context) error {
	cfg := ctx.Config
	store := ctx.Storage

	// Parse flags
	dryRun := false
	force := false
	showReport := true

	for _, arg := range ctx.Args {
		switch arg {
		case "--dry-run":
			dryRun = true
		case "--force":
			force = true
		case "-h", "--help":
			fmt.Println("Usage: cortex prune [flags]")
			fmt.Println("\nManage context size relative to project code size.")
			fmt.Println("Default: Context can be up to 3x project size.")
			fmt.Println("\nFlags:")
			fmt.Println("  --dry-run    Show what would be pruned without doing it")
			fmt.Println("  --force      Prune even if under limit")
			fmt.Println("\nExamples:")
			fmt.Println("  cortex prune           # Show size report")
			fmt.Println("  cortex prune --dry-run # Preview pruning")
			fmt.Println("  cortex prune --force   # Force prune even if under limit")
			return nil
		}
	}

	pruner := intcognition.NewPruner(store, cfg)

	// Always show report first
	if showReport {
		report, err := pruner.GetSizeReport()
		if err != nil {
			return fmt.Errorf("failed to get size report: %w", err)
		}
		fmt.Println(report)
		fmt.Println()
	}

	// Check if pruning needed
	shouldPrune, ratio, err := pruner.ShouldPrune()
	if err != nil {
		return fmt.Errorf("failed to check prune status: %w", err)
	}

	if !shouldPrune && !force {
		fmt.Printf("Context is within limits (%.1fx project size, max 3.0x).\n", ratio)
		fmt.Println("Use --force to prune anyway.")
		return nil
	}

	if dryRun {
		fmt.Printf("Would prune context (currently %.1fx project size).\n", ratio)
		fmt.Println("Remove --dry-run to execute.")
		return nil
	}

	// Execute pruning
	fmt.Println("Pruning low-value context...")
	result, err := pruner.MaybePrune(context.Background())
	if err != nil {
		return fmt.Errorf("prune failed: %w", err)
	}

	if result.Skipped {
		fmt.Printf("Skipped: %s\n", result.SkipReason)
		return nil
	}

	fmt.Printf("Pruned %d items in %v\n", result.Pruned, result.Duration.Round(time.Millisecond))
	fmt.Printf("Size: %s -> %s (%.1fx project)\n",
		formatBytesShort(result.CortexSize),
		formatBytesShort(result.NewSize),
		result.Ratio)

	return nil
}

// formatBytesShort formats bytes as compact human-readable string.
func formatBytesShort(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(b)/float64(div), "KMGTPE"[exp])
}
