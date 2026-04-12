package commands

import (
	"context"
	"fmt"
	"os"

	"strconv"


	"github.com/dereksantos/cortex/pkg/llm"
)

// ReembedCommand re-generates embeddings with the current model.
type ReembedCommand struct{}

func init() {
	Register(&ReembedCommand{})
}

// Name returns the command name.
func (c *ReembedCommand) Name() string { return "reembed" }

// Description returns the command description.
func (c *ReembedCommand) Description() string { return "Re-generate embeddings with current model" }

// Execute runs the reembed command.
func (c *ReembedCommand) Execute(ctx *Context) error {
	dryRun := false
	for _, arg := range ctx.Args {
		if arg == "--dry-run" {
			dryRun = true
		}
		if arg == "-h" || arg == "--help" {
			fmt.Println("Usage: cortex reembed [flags]")
			fmt.Println("\nRe-generate all embeddings with the current embedding model.")
			fmt.Println("\nFlags:")
			fmt.Println("  --dry-run     Show count only, don't re-embed")
			fmt.Println("  -h, --help    Show this help message")
			return nil
		}
	}

	store := ctx.Storage

	// Get total count
	count, err := store.GetEmbeddingCount()
	if err != nil {
		return fmt.Errorf("failed to count embeddings: %w", err)
	}

	if count == 0 {
		fmt.Println("No embeddings found. Run 'cortex ingest' first to capture events.")
		return nil
	}

	if dryRun {
		fmt.Printf("Found %d embeddings to re-generate.\n", count)
		fmt.Printf("Model: %s\n", llm.DefaultHugotModel)
		fmt.Println("Run without --dry-run to re-embed.")
		return nil
	}

	// Create embedder
	embedder := llm.NewFallbackEmbedder(
		llm.NewOllamaClient(ctx.Config),
		llm.NewHugotEmbedder(),
	)

	if !embedder.IsEmbeddingAvailable() {
		return fmt.Errorf("no embedding model available. Install Ollama or ensure HuggingFace model can load")
	}

	// Set up cancellation
	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	notifyTermSignals(sigCh)
	go func() {
		<-sigCh
		fmt.Fprintf(os.Stderr, "\nInterrupted, finishing current embedding...\n")
		cancel()
	}()

	// Get all content IDs
	contents, err := store.GetAllEmbeddingContentIDs()
	if err != nil {
		return fmt.Errorf("failed to get embedding content IDs: %w", err)
	}

	fmt.Printf("Re-embedding %d items with %s...\n", len(contents), llm.DefaultHugotModel)

	var reembedded, skipped, errors int

	for i, ec := range contents {
		// Check for cancellation
		select {
		case <-bgCtx.Done():
			fmt.Printf("\nStopped. Re-embedded %d/%d (skipped: %d, errors: %d)\n",
				reembedded, len(contents), skipped, errors)
			return nil
		default:
		}

		// Load source content
		var text string
		switch ec.ContentType {
		case "event":
			event, err := store.GetEvent(ec.ContentID)
			if err != nil {
				skipped++
				continue
			}
			text = event.ToolResult
		case "insight":
			// Insight IDs are stored as string representations of int64
			insightID, err := strconv.ParseInt(ec.ContentID, 10, 64)
			if err != nil {
				skipped++
				continue
			}
			insight, err := store.GetInsightByID(insightID)
			if err != nil {
				skipped++
				continue
			}
			text = insight.Summary
		default:
			skipped++
			continue
		}

		if text == "" {
			skipped++
			continue
		}

		// Generate new embedding
		vec, err := embedder.Embed(bgCtx, text)
		if err != nil {
			errors++
			continue
		}

		// Store with model name
		if err := store.StoreEmbeddingWithModel(ec.ContentID, ec.ContentType, vec, llm.DefaultHugotModel); err != nil {
			errors++
			continue
		}

		reembedded++

		// Progress every 10 items or at the end
		if (i+1)%10 == 0 || i+1 == len(contents) {
			fmt.Printf("Re-embedded %d/%d (skipped: %d, errors: %d)\n",
				reembedded, len(contents), skipped, errors)
		}
	}

	fmt.Printf("\nDone. Re-embedded %d/%d (skipped: %d, errors: %d)\n",
		reembedded, len(contents), skipped, errors)

	return nil
}
