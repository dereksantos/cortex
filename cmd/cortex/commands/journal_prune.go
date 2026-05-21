// Package commands — `cortex journal prune` subcommand.
//
// Time-based retention for the append-only journal: closed segments
// whose newest entry is older than --older-than are deleted. Per-
// class --max-bytes evicts oldest-first when a class directory
// exceeds the byte budget. The active segment (highest-numbered) is
// always retained — pruning it would break Append's monotonic-offset
// invariant.
//
// Pairs naturally with `cortex journal verify` (offset integrity)
// and the existing journal.CompactClosedSegments path (gzip closed
// segments before they age out). Run `cortex journal compact` style
// passes more frequently than prune so old segments are small when
// retention catches them.
package commands

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
)

// defaultJournalRetention is the default --older-than horizon. 90
// days matches the design discussion in docs/salience-budgets.md
// (failure mode #2 mitigation). Override via --older-than DUR.
const defaultJournalRetention = 90 * 24 * time.Hour

// runPrune parses `cortex journal prune [flags] [--class NAME]` and
// applies a PruneOptions pass over either one class directory or
// every class directory under .cortex/journal/.
func (c *JournalCommand) runPrune(ctx *Context) error {
	args := ctx.Args[1:]
	// --help short-circuit before flag parsing so flag.ContinueOnError
	// doesn't surface "flag: help requested" on stderr.
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			printJournalPruneHelp()
			return nil
		}
	}

	fs := flag.NewFlagSet("journal prune", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	olderThan := fs.Duration("older-than", defaultJournalRetention,
		"Delete closed segments whose newest entry is older than this (default 90d)")
	maxBytes := fs.Int64("max-bytes", 0,
		"Per-class byte budget; oldest segments evicted until size fits (0 = disabled)")
	class := fs.String("class", "",
		"Limit to one writer-class (default: all classes under .cortex/journal/)")
	root := fs.String("journal-root", ".cortex/journal",
		"Override the journal root path")
	dryRun := fs.Bool("dry-run", false,
		"Report what would be removed without touching disk")

	if err := fs.Parse(args); err != nil {
		printJournalPruneHelp()
		return err
	}

	opts := journal.PruneOptions{
		MaxAge:   *olderThan,
		MaxBytes: *maxBytes,
		DryRun:   *dryRun,
	}

	var reports []journal.PruneReport
	if *class != "" {
		cd := filepath.Join(*root, *class)
		if _, err := os.Stat(cd); err != nil {
			return fmt.Errorf("class dir %s: %w", cd, err)
		}
		r, err := journal.Prune(cd, opts)
		if err != nil {
			return fmt.Errorf("prune %s: %w", cd, err)
		}
		reports = append(reports, r)
	} else {
		rs, err := journal.PruneAll(*root, opts)
		if err != nil {
			return fmt.Errorf("prune all classes: %w", err)
		}
		reports = rs
	}

	reportJournalPrune(reports, opts)
	return nil
}

func reportJournalPrune(reports []journal.PruneReport, opts journal.PruneOptions) {
	tag := ""
	if opts.DryRun {
		tag = " (dry-run)"
	}
	fmt.Printf("=== journal prune%s ===\n", tag)
	fmt.Printf("Horizon: %s   MaxBytes: %s\n\n", formatDuration(opts.MaxAge), formatBytes(opts.MaxBytes))

	totalRemoved := 0
	var totalFreed int64
	for _, r := range reports {
		class := filepath.Base(r.ClassDir)
		if len(r.Removed) == 0 {
			fmt.Printf("  %-15s  segments=%d  removed=0\n", class, r.SegmentsBefore)
			continue
		}
		fmt.Printf("  %-15s  segments=%d  removed=%d  freed=%s\n",
			class, r.SegmentsBefore, len(r.Removed), formatBytes(r.BytesFreed))
		for i, n := range r.Removed {
			reason := ""
			if i < len(r.Reasons) {
				reason = r.Reasons[i]
			}
			fmt.Printf("                   - segment %04d  %s\n", n, reason)
		}
		totalRemoved += len(r.Removed)
		totalFreed += r.BytesFreed
	}
	fmt.Printf("\nTotal: %d segments %s, %s freed%s\n",
		totalRemoved,
		map[bool]string{true: "would be removed", false: "removed"}[opts.DryRun],
		formatBytes(totalFreed),
		tag,
	)
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "disabled"
	}
	days := int(d / (24 * time.Hour))
	if days >= 1 && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return d.String()
}

func formatBytes(b int64) string {
	if b <= 0 {
		return "disabled"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	suffixes := []string{"K", "M", "G", "T"}
	if exp >= len(suffixes) {
		exp = len(suffixes) - 1
	}
	return fmt.Sprintf("%.1f%sB", float64(b)/float64(div), suffixes[exp])
}

func printJournalPruneHelp() {
	fmt.Println(strings.TrimSpace(`
Usage: cortex journal prune [flags]

Delete closed journal segments past a retention horizon. The active
(highest-numbered) segment per class is always retained.

Flags:
  --older-than DUR    Age horizon (default 90d). Closed segments whose
                      newest entry is older than this are removed.
  --max-bytes N       Per-class byte budget; oldest segments evicted
                      until total class size fits. 0 = disabled.
  --class NAME        Limit to one writer-class (default: all).
  --journal-root DIR  Override journal root (default .cortex/journal).
  --dry-run           Report selections without deleting.

Examples:
  cortex journal prune                         # 90-day default, all classes
  cortex journal prune --older-than 30d        # tighter retention
  cortex journal prune --max-bytes 1G          # per-class size cap
  cortex journal prune --class capture --dry-run
`))
}
